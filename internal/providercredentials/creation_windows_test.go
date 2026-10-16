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
