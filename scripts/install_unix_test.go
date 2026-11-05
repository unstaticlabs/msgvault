//go:build !windows

package scripts

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func installerArchive(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	for name, content := range members {
		require.NoError(t, archive.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}))
		_, err := archive.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, archive.Close())
	require.NoError(t, compressed.Close())
	return buffer.Bytes()
}

func installerDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// Exercise the repository script itself with real curl, tar, checksum and file
// operations. Only the release server is replaced by a disposable HTTP fixture.
func TestLinuxInstallerPublishesVerifiedHelperBeforeMain(t *testing.T) {
	for _, scenario := range []string{"success", "missing helper", "missing main", "corrupt archive", "checksum unavailable", "helper destination blocked"} {
		t.Run(scenario, func(t *testing.T) {
			assertChecks := assert.New(t)
			requireChecks := require.New(t)
			installDir := t.TempDir()
			mainPath := filepath.Join(installDir, "msgvault")
			requireChecks.NoError(os.WriteFile(mainPath, []byte("old main"), 0o755))
			oldHelper := filepath.Join(installDir, "msgvault-codex-bridge")
			requireChecks.NoError(os.WriteFile(oldHelper, []byte("old helper"), 0o755))
			members := map[string]string{"msgvault": "new main", "msgvault-codex-bridge": "new helper"}
			if scenario == "missing helper" {
				delete(members, "msgvault-codex-bridge")
			}
			if scenario == "missing main" {
				delete(members, "msgvault")
			}
			archive := installerArchive(t, members)
			const tag = "v0.23.0-usl.1"
			const asset = "msgvault_0.23.0-usl.1_linux_amd64.tar.gz"
			const releases = "/unstaticlabs/msgvault/releases"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case releases + "/latest":
					http.Redirect(w, r, releases+"/tag/"+tag, http.StatusFound)
				case releases + "/tag/" + tag:
					w.WriteHeader(http.StatusOK)
				case releases + "/download/" + tag + "/" + asset:
					body := append([]byte(nil), archive...)
					if scenario == "corrupt archive" {
						body[len(body)-1] ^= 1
					}
					_, _ = w.Write(body)
				case releases + "/download/" + tag + "/SHA256SUMS":
					if scenario == "checksum unavailable" {
						http.NotFound(w, r)
						return
					}
					_, _ = fmt.Fprintf(w, "%s  %s\n", installerDigest(archive), asset)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			helperPath := filepath.Join(installDir, "msgvault-codex-bridge."+installerDigest([]byte("new helper")))
			if scenario == "helper destination blocked" {
				requireChecks.NoError(os.Mkdir(helperPath, 0o755))
			}
			script, err := filepath.Abs("install.sh")
			requireChecks.NoError(err)
			run := func() ([]byte, error) {
				command := exec.CommandContext(t.Context(), "bash", "-c",
					`source "$1"; GITHUB_BASE_URL="$2"; install_from_release linux amd64 "$3"`,
					"installer-fixture", script, server.URL, installDir)
				command.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
				return command.CombinedOutput()
			}
			output, err := run()
			if scenario == "success" {
				requireChecks.NoError(err, "%s", output)
				main, readErr := os.ReadFile(mainPath)
				requireChecks.NoError(readErr)
				assertChecks.Equal("new main", string(main))
				helper, readErr := os.ReadFile(helperPath)
				requireChecks.NoError(readErr)
				assertChecks.Equal("new helper", string(helper))
				older := filepath.Join(installDir, "msgvault-codex-bridge."+strings.Repeat("a", 64))
				requireChecks.NoError(os.WriteFile(older, []byte("older helper"), 0o755))
				output, err = run()
				requireChecks.NoError(err, "%s", output)
				retained, readErr := os.ReadFile(older)
				requireChecks.NoError(readErr)
				assertChecks.Equal("older helper", string(retained))
			} else {
				requireChecks.Error(err, "%s", output)
				main, readErr := os.ReadFile(mainPath)
				requireChecks.NoError(readErr)
				assertChecks.Equal("old main", string(main))
			}
			plainHelper, readErr := os.ReadFile(oldHelper)
			requireChecks.NoError(readErr)
			assertChecks.Equal("old helper", string(plainHelper))
		})
	}
}
