//go:build linux

package peoplesweep

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexBridgeDigestNamedResolution(t *testing.T) {
	assertChecks := assert.New(t)
	requireChecks := require.New(t)
	root := t.TempDir()
	main := filepath.Join(root, "msgvault")
	plain := filepath.Join(root, "msgvault-codex-bridge")
	requireChecks.NoError(os.WriteFile(plain, []byte("previous plain helper"), 0o755))
	helper := []byte("current digest helper")
	staged := filepath.Join(root, "staged-helper")
	requireChecks.NoError(os.WriteFile(staged, helper, 0o755))
	digest, err := hashCodexExecutable(staged)
	requireChecks.NoError(err)
	versioned := plain + "." + digest
	requireChecks.NoError(os.Rename(staged, versioned))
	assertChecks.Equal(versioned, adjacentCodexBridgePath(main, digest))
	chosenDigest, err := hashCodexExecutable(adjacentCodexBridgePath(main, digest))
	requireChecks.NoError(err)
	assertChecks.Equal(digest, chosenDigest)
	// A helper for another daemon revision is never selected as a substitute.
	assertChecks.Equal(plain, adjacentCodexBridgePath(main, strings.Repeat("0", 64)))
	requireChecks.NoError(os.Remove(versioned))
	assertChecks.Equal(plain, adjacentCodexBridgePath(main, digest))
	// An existing invalid candidate remains selected for the launcher's original
	// validation; falling back here would hide an invalid or corrupted update.
	requireChecks.NoError(os.Mkdir(versioned, 0o755))
	assertChecks.Equal(versioned, adjacentCodexBridgePath(main, digest))
}
