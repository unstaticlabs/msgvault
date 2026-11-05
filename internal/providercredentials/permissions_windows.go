//go:build windows

package providercredentials

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"go.kenn.io/msgvault/internal/fileutil"
	"golang.org/x/sys/windows"
)

type nativePermissions struct{}

func (nativePermissions) prepareDirectory(path string) error {
	if err := fileutil.SecureMkdirAll(path, 0o700); err != nil {
		return err
	}
	handle, err := openSecurityHandle(path, true, windows.READ_CONTROL)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // ownership check is authoritative
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read credential directory owner: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return errors.New("credential directory owner is unavailable")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("get current user SID: %w", err)
	}
	return verifyWindowsOwner(owner, user.User.Sid)
}

func (nativePermissions) secureDirectory(path string) error {
	if err := fileutil.SecureMkdirAll(path, 0o700); err != nil {
		return err
	}
	handle, err := openSecurityHandle(path, true, windows.READ_CONTROL|windows.WRITE_DAC)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // security result is authoritative
	// Older protected directory ACLs can pass the exact owner verifier while
	// withholding inheritance from sibling stores. Republish the directory
	// form; protected credential files retain their independent descriptors.
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
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("inspect provider credential object for DACL: %w", err)
	}
	access, inheritance := windows.ACCESS_MASK(windows.GENERIC_ALL), uint32(windows.NO_INHERITANCE)
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		// The directory is owner-only, but ordinary sibling lock files must
		// retain owner access when its ACL is published. Files remain protected
		// at birth and never inherit a later directory permission change.
		access = fileutil.FileAllAccess
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("get current user SID: %w", err)
	}
	entries := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: access,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       inheritance,
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
	fullControl := ace.Mask == windows.GENERIC_ALL || ace.Mask == fileutil.FileAllAccess
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
// verify/harden an existing lock under the directory-identity mutex before
// requesting data access, including when inheritance propagation removed it.
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

// secureExistingCredentialLock repairs legacy inherited or empty DACLs using
// only security rights. An owner can restore those rights even when the file
// no longer grants read/write access. The directory-identity mutex is held.
func secureExistingCredentialLock(tokenDir string) error {
	handle, err := openSecurityHandle(filepath.Join(tokenDir, ".provider-credentials.lock"), false,
		windows.READ_CONTROL|windows.WRITE_DAC)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open existing provider credential lock for repair: %w", err)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // security result is authoritative
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read provider credential lock owner: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return errors.New("provider credential lock owner is unavailable")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("get current user SID: %w", err)
	}
	if err := verifyWindowsOwner(owner, user.User.Sid); err != nil {
		return fmt.Errorf("verify provider credential lock owner: %w", err)
	}
	if err := secureOwnerOnlyHandle(handle); err != nil {
		return fmt.Errorf("secure existing provider credential lock: %w", err)
	}
	return nil
}

func withStoreLock(tokenDir string, fn func() error) error {
	// Directory ACL updates can propagate to a writer's unfinished candidate.
	// Serialize repair and writes before opening the lock file, since creating
	// that file may itself require repairing the directory's write access.
	directory, err := openSecurityHandle(tokenDir, true, windows.READ_CONTROL|windows.WRITE_DAC)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(directory) //nolint:errcheck // lock result is authoritative
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(directory, &info); err != nil {
		return fmt.Errorf("identify provider credential directory: %w", err)
	}
	// Use file identity so case differences and short path aliases share a lock.
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`Global\msgvault-provider-credentials-%08x-%08x%08x`,
		info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow))
	if err != nil {
		return fmt.Errorf("encode provider credential mutex: %w", err)
	}
	mutex, err := windows.CreateMutexEx(nil, name, 0, windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("open provider credential mutex: %w", err)
	}
	defer windows.CloseHandle(mutex) //nolint:errcheck // lock result is authoritative
	// Windows mutex ownership belongs to the OS thread, not the goroutine.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	status, err := windows.WaitForSingleObject(mutex, windows.INFINITE)
	if err != nil {
		return fmt.Errorf("wait for provider credential mutex: %w", err)
	}
	if status != windows.WAIT_OBJECT_0 && status != windows.WAIT_ABANDONED {
		return fmt.Errorf("unexpected provider credential mutex wait result: %d", status)
	}
	defer windows.ReleaseMutex(mutex) //nolint:errcheck // lock result is authoritative
	// Repair the legacy lock before directory DACL propagation can strip its
	// inherited access, and before OPEN_ALWAYS requests read/write rights.
	if err := secureExistingCredentialLock(tokenDir); err != nil {
		return err
	}
	if err := secureOwnerOnlyHandle(directory); err != nil {
		return fmt.Errorf("secure provider credential directory: %w", err)
	}

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
	// The named mutex serializes ACL repair; this file lock protects the data
	// critical section. New objects were protected at birth and existing ones
	// were repaired before requesting data access. Keep the exact verifier.
	if err := verifyOwnerOnlyHandle(windows.Handle(file.Fd()), false); err != nil {
		return fmt.Errorf("verify provider credential lock: %w", err)
	}
	return fn()
}
