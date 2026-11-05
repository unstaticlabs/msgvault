//go:build !windows

package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func releaseArchive(t *testing.T, members map[string]string) []byte {
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

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func TestLinuxUpdateStagesBothMembersBeforeReplacingMain(t *testing.T) {
	for _, scenario := range []string{"success", "missing helper", "changed second download", "helper install fails"} {
		t.Run(scenario, func(t *testing.T) {
			assertChecks := assert.New(t)
			requireChecks := require.New(t)
			installDir := t.TempDir()
			mainPath := filepath.Join(installDir, binaryName)
			requireChecks.NoError(os.WriteFile(mainPath, []byte("old main"), 0o755))
			oldHelper := filepath.Join(installDir, codexBridgeName)
			requireChecks.NoError(os.WriteFile(oldHelper, []byte("old helper"), 0o755))
			members := map[string]string{binaryName: "new main", codexBridgeName: "new helper"}
			if scenario == "missing helper" {
				delete(members, codexBridgeName)
			}
			archive := releaseArchive(t, members)
			newHelper := filepath.Join(installDir, codexBridgeName+"."+digestBytes([]byte("new helper")))
			if scenario == "helper install fails" {
				requireChecks.NoError(os.Mkdir(newHelper, 0o755))
			}
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// Neither member may replace the current installation during staging.
				request := requests.Add(1)
				current, err := os.ReadFile(mainPath)
				assertChecks.NoError(err)
				expectedMain := "old main"
				if request > 2 {
					expectedMain = "new main"
				}
				assertChecks.Equal(expectedMain, string(current))
				body := append([]byte(nil), archive...)
				if request == 2 && scenario == "changed second download" {
					body[len(body)-1] ^= 1
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				_, _ = w.Write(body)
			}))
			t.Cleanup(server.Close)
			u := NewUpdater(Deps{Client: server.Client(), GOOS: "linux", GOARCH: "amd64",
				Executable: func() (string, error) { return mainPath, nil }, CacheDir: t.TempDir})
			info := &UpdateInfo{Owner: releaseOwner, Repo: releaseRepo, LatestVersion: "v0.23.0-usl.1",
				AssetName: "msgvault_0.23.0-usl.1_linux_amd64.tar.gz", DownloadURL: server.URL + "/release.tar.gz",
				GOOS: "linux", GOARCH: "amd64", Checksum: digestBytes(archive), Size: int64(len(archive))}
			err := u.PerformUpdate(info, nil)
			if scenario == "success" {
				requireChecks.NoError(err)
				main, readErr := os.ReadFile(mainPath)
				requireChecks.NoError(readErr)
				assertChecks.Equal("new main", string(main))
				helper, readErr := os.ReadFile(newHelper)
				requireChecks.NoError(readErr)
				assertChecks.Equal("new helper", string(helper))
				// Repeating the release retains helpers used by earlier daemons.
				olderDigestPath := filepath.Join(installDir, codexBridgeName+"."+digestBytes([]byte("older helper")))
				requireChecks.NoError(os.WriteFile(olderDigestPath, []byte("older helper"), 0o755))
				requireChecks.NoError(u.PerformUpdate(info, nil))
				older, readErr := os.ReadFile(olderDigestPath)
				requireChecks.NoError(readErr)
				assertChecks.Equal("older helper", string(older))
			} else {
				requireChecks.Error(err)
				main, readErr := os.ReadFile(mainPath)
				requireChecks.NoError(readErr)
				assertChecks.Equal("old main", string(main))
			}
			old, readErr := os.ReadFile(oldHelper)
			requireChecks.NoError(readErr)
			assertChecks.Equal("old helper", string(old))
			assertChecks.GreaterOrEqual(requests.Load(), int32(2))
		})
	}
}

func TestLinuxUpdateRetainsHelpersAcrossDifferentReleases(t *testing.T) {
	assertChecks := assert.New(t)
	requireChecks := require.New(t)
	installDir := t.TempDir()
	mainPath := filepath.Join(installDir, binaryName)
	requireChecks.NoError(os.WriteFile(mainPath, []byte("old main"), 0o755))
	archives := [][]byte{
		releaseArchive(t, map[string]string{binaryName: "first main", codexBridgeName: "first helper"}),
		releaseArchive(t, map[string]string{binaryName: "second main", codexBridgeName: "second helper"}),
	}
	var version atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := archives[version.Load()]
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	u := NewUpdater(Deps{Client: server.Client(), GOOS: "linux", GOARCH: "amd64",
		Executable: func() (string, error) { return mainPath, nil }, CacheDir: t.TempDir})
	for index, archive := range archives {
		version.Store(int32(index))
		tag := []string{"v0.23.0-usl.1", "v0.23.0-usl.2"}[index]
		requireChecks.NoError(u.PerformUpdate(&UpdateInfo{
			Owner: releaseOwner, Repo: releaseRepo, LatestVersion: tag,
			AssetName: "msgvault_" + tag[1:] + "_linux_amd64.tar.gz", DownloadURL: server.URL + "/release.tar.gz",
			GOOS: "linux", GOARCH: "amd64", Checksum: digestBytes(archive), Size: int64(len(archive)),
		}, nil))
	}
	main, err := os.ReadFile(mainPath)
	requireChecks.NoError(err)
	assertChecks.Equal("second main", string(main))
	for _, contents := range []string{"first helper", "second helper"} {
		helper, readErr := os.ReadFile(filepath.Join(installDir, codexBridgeName+"."+digestBytes([]byte(contents))))
		requireChecks.NoError(readErr)
		assertChecks.Equal(contents, string(helper))
	}
}
