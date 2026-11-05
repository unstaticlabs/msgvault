//go:build windows

package providercredentials

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/fileutil"
	"golang.org/x/sys/windows"
)

func TestWindowsCredentialFilesAreProtectedAtBirth(t *testing.T) {
	requirements := require.New(t)
	dir := t.TempDir()
	// This is the former production creation path. Without SECURITY_ATTRIBUTES
	// its default descriptor fails the exact store verifier before any repair.
	previous, err := os.CreateTemp(dir, ".previous-candidate-*.json")
	requirements.NoError(err)
	requirements.Error((nativePermissions{}).verifyFile(previous))
	requirements.NoError(previous.Close())
	for name, create := range map[string]func(string) (*os.File, error){
		"candidate": createCredentialCandidate,
		"lock":      openCredentialLock,
	} {
		t.Run(name, func(t *testing.T) {
			requirements := require.New(t)
			file, err := create(dir)
			requirements.NoError(err)
			defer file.Close() //nolint:errcheck // assertion result is authoritative
			// No secureFile, write, or later ACL publication may precede this check.
			requirements.NoError((nativePermissions{}).verifyFile(file))
		})
	}
}

func TestWindowsProtectedCandidateSurvivesParentDACLPublication(t *testing.T) {
	requirements := require.New(t)
	dir := t.TempDir()
	requirements.NoError((nativePermissions{}).secureDirectory(dir))
	handle, err := openSecurityHandle(dir, true, windows.READ_CONTROL|windows.WRITE_DAC)
	requirements.NoError(err)
	defer windows.CloseHandle(handle) //nolint:errcheck // assertion result is authoritative
	for range 16 {
		file, err := createCredentialCandidate(dir)
		requirements.NoError(err)
		requirements.NoError((nativePermissions{}).verifyFile(file))
		// Reproduce a first initializer publishing its directory ACL after another
		// process has created a child. Protected child descriptors must survive.
		requirements.NoError(secureOwnerOnlyHandle(handle))
		requirements.NoError((nativePermissions{}).verifyFile(file))
		requirements.NoError(file.Close())
	}
}

func TestWindowsCredentialConcurrentFirstInitializationAcrossProcesses(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	root := t.TempDir()
	dir := filepath.Join(root, "tokens") // deliberately absent until the children race
	executable, err := os.Executable()
	requirements.NoError(err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	type child struct {
		command *exec.Cmd
		output  bytes.Buffer
	}
	children := make([]child, 4)
	for i := range children {
		command := exec.CommandContext(ctx, executable, "-test.run=^TestWindowsCredentialProcess$", "-test.v")
		command.Env = []string{"MSGVAULT_CREDENTIAL_TEST_ROOT=" + root, "MSGVAULT_CREDENTIAL_TEST_CHILD=" + strconv.Itoa(i), "HOME=" + root, "USERPROFILE=" + root}
		for _, key := range []string{"SystemRoot", "WINDIR", "TMP", "TEMP"} {
			if value, ok := os.LookupEnv(key); ok {
				command.Env = append(command.Env, key+"="+value)
			}
		}
		children[i].command = command
		command.Stdout = &children[i].output
		command.Stderr = &children[i].output
		requirements.NoError(command.Start())
		t.Cleanup(func() {
			if command.ProcessState == nil {
				_ = command.Process.Kill()
				_ = command.Wait()
			}
		})
	}
	// Every process is ready before any may initialize the missing token directory.
	requirements.Eventually(func() bool {
		for i := range children {
			if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("ready-%d", i))); err != nil {
				return false
			}
		}
		return true
	}, 15*time.Second, 10*time.Millisecond)
	requirements.NoError(os.WriteFile(filepath.Join(root, "start"), nil, 0o600))
	for i := range children {
		requirements.NoError(children[i].command.Wait(), children[i].output.String())
	}
	current, err := Read(dir)
	requirements.NoError(err)
	assertions.Len(current.credentials, len(children), "each process retains exactly its own sentinel after deleting transient imports")
	for i := range children {
		id := PeopleProviderID(fmt.Sprintf("retained-%d", i))
		value, _, err := current.Resolve(id, "https://credentials.example.test/v1", "", nil)
		requirements.NoError(err)
		assertions.Equal("synthetic-retained", value)
	}
	lock, err := openCredentialLock(dir)
	requirements.NoError(err)
	requirements.NoError((nativePermissions{}).verifyFile(lock))
	requirements.NoError(lock.Close())
}

func TestWindowsCredentialProcess(t *testing.T) {
	index, child := os.LookupEnv("MSGVAULT_CREDENTIAL_TEST_CHILD")
	if !child {
		return
	}
	requirements := require.New(t)
	root := os.Getenv("MSGVAULT_CREDENTIAL_TEST_ROOT")
	requirements.NotEmpty(root)
	requirements.NoError(os.WriteFile(filepath.Join(root, "ready-"+index), nil, 0o600))
	requirements.Eventually(func() bool { _, err := os.Stat(filepath.Join(root, "start")); return err == nil }, 20*time.Second, 10*time.Millisecond)
	dir := filepath.Join(root, "tokens")
	sentinel := PeopleProviderID("retained-" + index)
	_, err := ImportIfAbsent(dir, sentinel, "https://credentials.example.test/v1", func() (string, bool, error) { return "synthetic-retained", true, nil }, noRetire)
	requirements.NoError(err)
	for round := range 4 {
		id := PeopleProviderID(fmt.Sprintf("temporary-%s-%d", index, round))
		retired := false
		imported, err := ImportIfAbsent(dir, id, "https://credentials.example.test/v1", func() (string, bool, error) { return "synthetic-temporary", true, nil }, func() error { retired = true; return nil })
		requirements.NoError(err)
		requirements.True(retired)
		revision, err := imported.Revision(id)
		requirements.NoError(err)
		deleted, err := DeleteIfRevision(dir, revision, id, noRetire)
		requirements.NoError(err)
		requirements.False(deleted.Stored(id))
		requirements.True(deleted.Stored(sentinel))
	}
}

func TestWindowsExistingCredentialLockIsHardenedUnderLock(t *testing.T) {
	requirements := require.New(t)
	dir := t.TempDir()
	previous, err := os.OpenFile(filepath.Join(dir, ".provider-credentials.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	requirements.NoError(err)
	requirements.Error((nativePermissions{}).verifyFile(previous))
	requirements.NoError(previous.Close())
	requirements.NoError(withStoreLock(dir, func() error {
		file, err := openCredentialLock(dir)
		if err != nil {
			return err
		}
		defer file.Close() //nolint:errcheck // permission result is authoritative
		return (nativePermissions{}).verifyFile(file)
	}))
}

func TestWindowsExistingCredentialLockRecoversAfterInheritedAccessIsRemoved(t *testing.T) {
	assertions, requirements := assert.New(t), require.New(t)
	dir := t.TempDir()
	entry := providerCredentialTestAccess(currentWindowsProviderCredentialSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER)
	entry.Inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	parent := setProviderCredentialTestACL(t, dir, []windows.EXPLICIT_ACCESS{entry}, true)
	path := filepath.Join(dir, ".provider-credentials.lock")
	sentinel := []byte("synthetic legacy lock contents")
	requirements.NoError(os.WriteFile(path, sentinel, 0o600))
	security, err := openSecurityHandle(path, false, windows.READ_CONTROL|windows.WRITE_DAC)
	requirements.NoError(err)
	t.Cleanup(func() {
		requirements.NoError(secureOwnerOnlyHandle(security))
		requirements.NoError(windows.CloseHandle(security))
	})
	dacl, control := readProviderCredentialTestACL(t, security)
	requirements.Equal(uint16(1), dacl.AceCount)
	requirements.Zero(control & windows.SE_DACL_PROTECTED)
	var inherited *windows.ACCESS_ALLOWED_ACE
	requirements.NoError(windows.GetAce(dacl, 0, &inherited))
	requirements.NotZero(inherited.Header.AceFlags & windows.INHERITED_ACE)

	// Reproduce the already-damaged state left by the former ordering:
	// the parent publishes a non-inheriting ACL before the legacy lock opens.
	oldEntry := providerCredentialTestAccess(currentWindowsProviderCredentialSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER)
	oldACL, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{oldEntry}, nil)
	requirements.NoError(err)
	requirements.NoError(windows.SetSecurityInfo(parent, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, oldACL, nil))
	dacl, _ = readProviderCredentialTestACL(t, security)
	requirements.Zero(dacl.AceCount, "parent propagation must remove the inherited access")
	denied, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if denied != nil {
		requirements.NoError(denied.Close())
	}
	requirements.ErrorIs(err, os.ErrPermission, "the fixture must deny ordinary read/write opening")
	entered := false
	requirements.NoError(withStoreLock(dir, func() error {
		entered = true
		file, err := openCredentialLock(dir)
		if err != nil {
			return err
		}
		defer file.Close() //nolint:errcheck // verification result is authoritative
		return (nativePermissions{}).verifyFile(file)
	}))
	assertions.True(entered, "ACL recovery must reach the protected data critical section")
	contents, err := os.ReadFile(path)
	requirements.NoError(err)
	assertions.Equal(sentinel, contents, "repair must preserve all existing lock bytes")
	requirements.NoError(verifyOwnerOnlyHandle(security, false))
}

func TestWindowsSecureDirectoryUpgradesProtectedNonInheritingACL(t *testing.T) {
	assertions, requirements := assert.New(t), require.New(t)
	dir := t.TempDir()
	legacy := providerCredentialTestAccess(currentWindowsProviderCredentialSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER)
	handle := setProviderCredentialTestACL(t, dir, []windows.EXPLICIT_ACCESS{legacy}, true)
	// The old ACL passes the owner-only verifier, but it must still upgrade:
	// publishing it would remove inherited access from sibling store locks.
	requirements.NoError(verifyOwnerOnlyHandle(handle, false))
	requirements.NoError((nativePermissions{}).secureDirectory(dir))
	requirements.NoError((nativePermissions{}).verifyDirectory(dir))
	dacl, control := readProviderCredentialTestACL(t, handle)
	requirements.Equal(uint16(1), dacl.AceCount)
	requirements.NotZero(control & windows.SE_DACL_PROTECTED)
	var ace *windows.ACCESS_ALLOWED_ACE
	requirements.NoError(windows.GetAce(dacl, 0, &ace))
	assertions.Equal(windows.ACCESS_MASK(fileutil.FileAllAccess), ace.Mask)
	assertions.Equal(byte(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE), ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE))
	assertions.Zero(ace.Header.AceFlags & windows.INHERITED_ACE)

	path := filepath.Join(dir, "synthetic-sibling.lock")
	sentinel := []byte("synthetic sibling lock")
	requirements.NoError(os.WriteFile(path, sentinel, 0o600))
	requirements.NoError(secureOwnerOnlyHandle(handle))
	contents, err := os.ReadFile(path)
	requirements.NoError(err)
	assertions.Equal(sentinel, contents, "directory publication must retain ordinary sibling owner access")
}
