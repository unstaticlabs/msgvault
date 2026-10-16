//go:build windows

package providercredentials

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

type nativePermissions struct{}

const providerCredentialFileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1FF

func (nativePermissions) secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil { // #nosec G703 -- Caller supplies the configured token directory; owner/reparse checks follow.
		return err
	}
	handle, err := openSecurityHandle(path, true, windows.READ_CONTROL|windows.WRITE_DAC)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // security result is authoritative
	if err := verifyOwnerOnlyHandle(handle, false); err == nil {
		return nil
	}
	return secureOwnerOnlyHandle(handle)
}

func (nativePermissions) verifyDirectory(path string) error {
	handle, err := openSecurityHandle(path, true, windows.READ_CONTROL)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // read-only handle
	return verifyOwnerOnlyHandle(handle, false)
}

func (nativePermissions) secureFile(file *os.File) error {
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	// os.OpenFile and os.CreateTemp do not request WRITE_DAC on Windows, so
	// their handles cannot publish the owner-only ACL. Reopen the file through
	// the already secured token directory with the exact security rights needed.
	handle, err := openSecurityHandle(file.Name(), false, windows.READ_CONTROL|windows.WRITE_DAC)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // security result is authoritative
	return secureOwnerOnlyHandle(handle)
}

func (nativePermissions) verifyFile(file *os.File) error {
	return verifyOwnerOnlyHandle(windows.Handle(file.Fd()), false)
}

func openSecurityHandle(path string, directory bool, access uint32) (windows.Handle, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("encode provider credential path: %w", err)
	}
	flags := uint32(windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(path16, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return 0, fmt.Errorf("open provider credential security handle: %w", err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, fmt.Errorf("inspect provider credential handle: %w", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return 0, errors.New("provider credential path must not be a reparse point")
	}
	return handle, nil
}

func secureOwnerOnlyHandle(handle windows.Handle) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("get current user SID: %w", err)
	}
	entries := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build owner-only provider credential DACL: %w", err)
	}
	securityInfo := windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.SECURITY_INFORMATION(securityInfo), nil, nil, acl, nil); err != nil {
		return fmt.Errorf("set owner-only provider credential DACL: %w", err)
	}
	return verifyOwnerOnlyHandleForUser(handle, user.User.Sid, false)
}

func verifyOwnerOnlyHandle(handle windows.Handle, allowReadOnly bool) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("get current user SID: %w", err)
	}
	return verifyOwnerOnlyHandleForUser(handle, user.User.Sid, allowReadOnly)
}

func verifyOwnerOnlyHandleForUser(handle windows.Handle, user *windows.SID, allowReadOnly bool) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read provider credential DACL: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return errors.New("provider credential owner is unavailable")
	}
	if err := verifyWindowsOwner(owner, user); err != nil {
		return err
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read provider credential DACL control: %w", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("provider credential DACL permits inherited access")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return errors.New("provider credential DACL is unavailable")
	}
	if dacl.AceCount != 1 {
		return errors.New("provider credential DACL must contain exactly one access entry")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return fmt.Errorf("read provider credential ACE: %w", err)
	}
	fullControl := ace.Mask == windows.GENERIC_ALL || ace.Mask == providerCredentialFileAllAccess
	readOnly := allowReadOnly && (ace.Mask == windows.GENERIC_READ || ace.Mask == windows.FILE_GENERIC_READ)
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || (!fullControl && !readOnly) ||
		ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
		return errors.New("provider credential DACL does not grant the required owner access")
	}
	// #nosec G103 -- GetAce supplies an access-allowed ACE whose SidStart is the first word of its contiguous SID.
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.Equals(user) {
		return errors.New("provider credential DACL grants another principal")
	}
	return nil
}

func verifyWindowsOwner(owner, user *windows.SID) error {
	if owner.Equals(user) {
		return nil
	}
	if owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		member, err := windows.Token(0).IsMember(owner)
		if err != nil {
			return fmt.Errorf("check provider credential owner membership: %w", err)
		}
		if member {
			return nil
		}
	}
	return errors.New("provider credential owner is not the current user")
}

func openStoreFile(path string) (*os.File, error) {
	handle, err := openSecurityHandle(path, false, windows.GENERIC_READ|windows.READ_CONTROL)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func openSecretFile(path string) (*os.File, error) { return openStoreFile(path) }

func verifySecretFile(file *os.File) error {
	return verifyOwnerOnlyHandle(windows.Handle(file.Fd()), true)
}

// createOwnerOnlyFile protects a new object in the CreateFile call itself. A
// later parent DACL publication cannot alter a protected child's permissions.
// OPEN_ALWAYS ignores this descriptor for an existing file; callers must still
// verify/harden an existing lock while holding the native cross-process lock.
func createOwnerOnlyFile(path string, disposition uint32) (*os.File, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get current user SID: %w", err)
	}
	sid := user.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;;FA;;;" + sid + ")")
	if err != nil {
		return nil, fmt.Errorf("build provider credential creation descriptor: %w", err)
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode provider credential path: %w", err)
	}
	handle, err := windows.CreateFile(path16,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		&attributes, disposition, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fmt.Errorf("create provider credential file: %w", err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("inspect provider credential handle: %w", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("provider credential path must not be a reparse point")
	}
	return os.NewFile(uintptr(handle), path), nil
}

func createCredentialCandidate(tokenDir string) (*os.File, error) {
	for range 10 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, fmt.Errorf("generate provider credential candidate name: %w", err)
		}
		path := filepath.Join(tokenDir, ".provider-credentials-"+hex.EncodeToString(random[:])+".json")
		file, err := createOwnerOnlyFile(path, windows.CREATE_NEW)
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		return file, err
	}
	return nil, errors.New("could not allocate a unique provider credential candidate")
}

func openCredentialLock(tokenDir string) (*os.File, error) {
	return createOwnerOnlyFile(filepath.Join(tokenDir, ".provider-credentials.lock"), windows.OPEN_ALWAYS)
}

func withStoreLock(tokenDir string, fn func() error) error {
	file, err := openCredentialLock(tokenDir)
	if err != nil {
		return fmt.Errorf("open provider credential lock: %w", err)
	}
	defer file.Close() //nolint:errcheck // lock result is authoritative
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, 1, 0, &overlapped); err != nil {
		return fmt.Errorf("lock provider credential store: %w", err)
	}
	defer windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped) //nolint:errcheck
	// Creation attributes do not change existing objects. Repair old lock files
	// only after native serialization, never concurrently with another holder.
	if err := verifyOwnerOnlyHandle(windows.Handle(file.Fd()), false); err != nil {
		if err := secureOwnerOnlyHandle(windows.Handle(file.Fd())); err != nil {
			return fmt.Errorf("secure provider credential lock: %w", err)
		}
	}
	return fn()
}
