//go:build windows

package oauth_test

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/carddav"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/fileutil"
	"go.kenn.io/msgvault/internal/oauth"
	"go.kenn.io/msgvault/internal/providercredentials"
	"golang.org/x/sys/windows"
)

func TestWindowsOAuthTokenLocksSurviveSharedCredentialPublication(t *testing.T) {
	publishers := []struct {
		name     string
		publish  func(*testing.T, string)
		filename string
	}{
		{name: "provider", filename: providercredentials.Filename, publish: func(t *testing.T, root string) {
			t.Helper()
			requirements := require.New(t)
			current, err := providercredentials.Read(root)
			requirements.NoError(err)
			_, err = providercredentials.Put(root, current.ETag, providercredentials.VectorEmbeddingsID, "https://embeddings.example.test/v1", "synthetic-provider-token")
			requirements.NoError(err)
		}},
		{name: "server_key", filename: providercredentials.ServerKeyFilename, publish: func(t *testing.T, root string) {
			t.Helper()
			key, err := providercredentials.EnsureServerKey(root)
			require.NoError(t, err)
			require.NotEmpty(t, key)
		}},
		{name: "default_carddav", filename: "carddav.json", publish: func(t *testing.T, root string) {
			t.Helper()
			credential := carddav.Credential{Password: "synthetic-carddav-token", BaseURL: "https://contacts.example.test/", Username: "owner@example.test", ConnectionGeneration: 1}
			require.NoError(t, carddav.SaveCredential(root, credential))
			loaded, err := carddav.LoadCredential(root)
			require.NoError(t, err)
			assert.Equal(t, credential, loaded)
		}},
	}
	for _, publisher := range publishers {
		for _, legacy := range []bool{false, true} {
			name := publisher.name + "/current_parent"
			if legacy {
				name = publisher.name + "/legacy_parent"
			}
			t.Run(name, func(t *testing.T) {
				assertions, requirements := assert.New(t), require.New(t)
				root := filepath.Join(t.TempDir(), "tokens")
				account := "owner@example.test"
				tokens := oauth.NewTokenStore(root, config.OAuthTokenCommands{})
				before := []byte(`{"access_token":"synthetic-before","refresh_token":"synthetic-refresh"}`)
				after := []byte(`{"access_token":"synthetic-after","refresh_token":"synthetic-refresh"}`)
				requirements.NoError(tokens.Write(t.Context(), account, before))
				t.Cleanup(func() {
					// Keep failed legacy-ACL assertions from stranding an inaccessible temp lock.
					publishSharedTestParentACL(t, root, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE)
				})
				tokenPath := oauth.TokenFilePath(root, account)
				lockPath := tokenPath + ".lock"
				assertSharedCredentialOwnerOnly(t, tokenPath, false)
				assertOAuthLockInherited(t, lockPath)
				if legacy {
					// Reproduce the old publisher exactly, independently of either repaired
					// helper: Windows propagates removal of inheritable parent ACEs.
					publishSharedTestParentACL(t, root, windows.NO_INHERITANCE)
					path16, err := windows.UTF16PtrFromString(lockPath)
					requirements.NoError(err)
					handle, err := windows.CreateFile(path16, windows.GENERIC_READ|windows.GENERIC_WRITE,
						windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
						nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
					if err == nil {
						requirements.NoError(windows.CloseHandle(handle))
					}
					requirements.ErrorIs(err, windows.ERROR_ACCESS_DENIED, "legacy publication stripped the inherited OAuth lock access")
				}
				publisher.publish(t, root)
				assertSharedCredentialOwnerOnly(t, root, true)
				assertSharedCredentialOwnerOnly(t, filepath.Join(root, publisher.filename), false)
				assertOAuthLockInherited(t, lockPath)
				actual, err := tokens.Read(t.Context(), account)
				requirements.NoError(err)
				assertions.Equal(before, actual, "publishing another credential preserves the OAuth token bytes")
				requirements.NoError(tokens.Write(t.Context(), account, after), "the inherited lock remains usable")
				actual, err = tokens.Read(t.Context(), account)
				requirements.NoError(err)
				assertions.Equal(after, actual)
				assertSharedCredentialOwnerOnly(t, tokenPath, false)
				publisher.publish(t, root)
				requirements.NoError(tokens.Delete(t.Context(), account), "repeated publication also preserves delete locking")
				_, err = os.Stat(tokenPath)
				requirements.ErrorIs(err, os.ErrNotExist)
			})
		}
	}
}

func publishSharedTestParentACL(t *testing.T, root string, inheritance uint32) {
	t.Helper()
	requirements := require.New(t)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	requirements.NoError(err)
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.SET_ACCESS, Inheritance: inheritance,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid)},
	}}, nil)
	requirements.NoError(err)
	requirements.NoError(windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
}

func assertOAuthLockInherited(t *testing.T, path string) {
	t.Helper()
	assertions, requirements := assert.New(t), require.New(t)
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	requirements.NoError(err)
	acl, _, err := descriptor.DACL()
	requirements.NoError(err)
	requirements.NotNil(acl)
	requirements.Equal(uint16(1), acl.AceCount)
	var ace *windows.ACCESS_ALLOWED_ACE
	requirements.NoError(windows.GetAce(acl, 0, &ace))
	assertions.NotZero(ace.Header.AceFlags & windows.INHERITED_ACE)
	assertions.Equal(uint32(fileutil.FileAllAccess), uint32(ace.Mask))
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	requirements.NoError(err)
	// #nosec G103 -- GetAce returns a contiguous access-allowed ACE and SID.
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	assertions.True(sid.Equals(user.User.Sid), "OAuth lock grants only the current user")
}

func assertSharedCredentialOwnerOnly(t *testing.T, path string, directory bool) {
	t.Helper()
	assertions, requirements := assert.New(t), require.New(t)
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	requirements.NoError(err)
	control, _, err := descriptor.Control()
	requirements.NoError(err)
	assertions.NotZero(control & windows.SE_DACL_PROTECTED)
	acl, _, err := descriptor.DACL()
	requirements.NoError(err)
	requirements.NotNil(acl)
	requirements.Equal(uint16(1), acl.AceCount)
	var ace *windows.ACCESS_ALLOWED_ACE
	requirements.NoError(windows.GetAce(acl, 0, &ace))
	assertions.Equal(uint8(windows.ACCESS_ALLOWED_ACE_TYPE), ace.Header.AceType)
	assertions.Contains([]uint32{windows.GENERIC_ALL, fileutil.FileAllAccess}, uint32(ace.Mask))
	assertions.Zero(ace.Header.AceFlags & windows.INHERITED_ACE)
	if directory {
		assertions.Equal(uint32(fileutil.FileAllAccess), uint32(ace.Mask))
		assertions.Equal(uint8(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE), ace.Header.AceFlags)
	} else {
		assertions.Zero(ace.Header.AceFlags)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	requirements.NoError(err)
	// #nosec G103 -- GetAce returns a contiguous access-allowed ACE and SID.
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	assertions.True(sid.Equals(user.User.Sid), "credential bytes grant only the current user")
}
