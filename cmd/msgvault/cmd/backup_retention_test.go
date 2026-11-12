package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/backup"
	"go.kenn.io/msgvault/internal/backupapp"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestBackupRetentionRestoresBeforeAndAfterCleanup(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	data := t.TempDir()
	db := filepath.Join(data, "msgvault.db")
	st := testutil.NewSQLiteTestStoreAt(t, db)
	attachments := filepath.Join(data, "attachments")
	require.NoError(os.Mkdir(attachments, 0700))
	source, err := st.GetOrCreateSource("gmail", "retention@example.test")
	require.NoError(err)
	conversation, err := st.EnsureConversation(source.ID, "retention", "Retention fixture")
	require.NoError(err)
	message, err := st.UpsertMessage(&store.Message{ConversationID: conversation, SourceID: source.ID, SourceMessageID: "retention", MessageType: "email"})
	require.NoError(err)
	content := []byte("synthetic retained attachment")
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	relative := digest[:2] + "/" + digest
	require.NoError(os.MkdirAll(filepath.Join(attachments, digest[:2]), 0700))
	require.NoError(os.WriteFile(filepath.Join(attachments, relative), content, 0600))
	require.NoError(st.UpsertAttachment(message, "fixture.txt", "text/plain", relative, digest, len(content)))
	repo, err := backup.Init(filepath.Join(t.TempDir(), "repo"))
	require.NoError(err)
	app := backupapp.New("test")
	// Thirty-one captures cross Kit's automatic keyframe boundary, allowing
	// obsolete incremental ancestors to be removed without breaking the latest.
	var first, latest string
	for i := range 31 {
		_, err := st.DB().Exec("CREATE TABLE IF NOT EXISTS retention_fixture (n INTEGER)")
		require.NoError(err)
		_, err = st.DB().Exec("INSERT INTO retention_fixture VALUES (?)", i)
		require.NoError(err)
		m, err := backup.Create(t.Context(), repo, app, backup.CreateOptions{DBPath: db, DataDir: data, ContentDir: attachments})
		require.NoError(err)
		if i == 0 {
			first = m.SnapshotID
		}
		latest = m.SnapshotID
	}
	restore := func(id string) {
		target := filepath.Join(t.TempDir(), "restore")
		_, err := backup.Restore(t.Context(), repo, app, backup.RestoreOptions{SnapshotID: id, TargetDir: target})
		require.NoError(err)
		restored, err := sql.Open("sqlite3", filepath.Join(target, "msgvault.db")+"?mode=ro")
		require.NoError(err)
		defer func() { require.NoError(restored.Close()) }()
		var rows int
		require.NoError(restored.QueryRow("SELECT COUNT(*) FROM retention_fixture").Scan(&rows))
		want := 31
		if id == first {
			want = 1
		}
		assert.Equal(want, rows)
		restoredContent, err := os.ReadFile(filepath.Join(target, "attachments", relative))
		require.NoError(err)
		assert.Equal(content, restoredContent)
	}
	restore(first)
	restore(latest)
	run := func(args ...string) error {
		c := newBackupRetainCommand()
		c.SetContext(testInvocationContext(context.Background(), &config.Config{Data: config.DataConfig{DataDir: data}}, invocationOptions{}))
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		c.SetArgs(append([]string{"--repo", repo.Path("."), "--keep-last", "1"}, args...))
		return c.Execute()
	}
	size := func() int64 {
		var total int64
		require.NoError(filepath.WalkDir(repo.Path("."), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				info, err := entry.Info()
				if err != nil {
					return fmt.Errorf("measure repository file: %w", err)
				}
				total += info.Size()
			}
			return nil
		}))
		return total
	}
	beforeBytes := size()
	before, err := repo.ListSnapshots()
	require.NoError(err)
	require.NoError(run("--dry-run", "--protect", first))
	afterDry, err := repo.ListSnapshots()
	require.NoError(err)
	assert.Len(afterDry, len(before))
	require.NoError(run("--protect", first))
	retained, err := repo.ListSnapshots()
	require.NoError(err)
	require.Less(len(retained), len(before))
	restore(first)
	restore(latest)
	// A failed upload leaves the existing protected anchor in place; retrying
	// retention is safe and cannot advance that externally managed anchor.
	require.NoError(run("--protect", first))
	restore(first)
	afterBytes := size()
	assert.Less(afterBytes, beforeBytes, "retention actually reclaims repository bytes")
	t.Logf("synthetic repository bytes before=%d after=%d reclaimed=%d", beforeBytes, afterBytes, beforeBytes-afterBytes)
	result, err := backup.Verify(t.Context(), repo, app, backup.VerifyOptions{All: true})
	require.NoError(err)
	assert.Empty(result.Problems)
	require.Error(run("--protect", "missing-anchor"))
}

func TestBackupRetentionCommandRegistered(t *testing.T) {
	for _, name := range []string{"forget", "prune", "retain"} {
		cmd, _, err := backupCmd.Find([]string{name})
		require.NoError(t, err)
		assert.Equal(t, name, cmd.Name())
	}
}

func TestBackupRetentionRefusesLastAndRequiredSnapshots(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	data := t.TempDir()
	db := filepath.Join(data, "msgvault.db")
	st := testutil.NewSQLiteTestStoreAt(t, db)
	content := filepath.Join(data, "attachments")
	require.NoError(os.Mkdir(content, 0700))
	repo, err := backup.Init(filepath.Join(t.TempDir(), "repo"))
	require.NoError(err)
	opts := backup.CreateOptions{DBPath: db, DataDir: data, ContentDir: content}
	first, err := backup.Create(t.Context(), repo, backupapp.New("test"), opts)
	require.NoError(err)
	execute := func(ids ...string) error {
		c := newBackupForgetCommand()
		c.SetContext(testInvocationContext(t.Context(), &config.Config{Data: config.DataConfig{DataDir: data}}, invocationOptions{}))
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		c.SetArgs(append([]string{"--repo", repo.Path(".")}, ids...))
		return c.Execute()
	}
	require.ErrorIs(execute(first.SnapshotID), backup.ErrLastSnapshot)
	_, err = st.DB().Exec("CREATE TABLE retention_dependency (n INTEGER)")
	require.NoError(err)
	second, err := backup.Create(t.Context(), repo, backupapp.New("test"), opts)
	require.NoError(err)
	require.Positive(second.DB.MapChainDepth)
	require.ErrorIs(execute(first.SnapshotID), backup.ErrSnapshotRequired)
	snapshots, err := repo.ListSnapshots()
	require.NoError(err)
	assert.Len(snapshots, 2)
	// An exclusive native backup lock excludes cleanup; cancellation exits
	// without changing any recovery point rather than stealing the lock.
	lock, err := repo.AcquireExclusiveLock("test-inflight", false)
	require.NoError(err)
	defer func() { require.NoError(lock.Release()) }()
	blocked := newBackupPruneCommand()
	blocked.SetContext(testInvocationContext(t.Context(), &config.Config{Data: config.DataConfig{DataDir: data}}, invocationOptions{}))
	blocked.SetArgs([]string{"--repo", repo.Path(".")})
	blocked.SetOut(&bytes.Buffer{})
	blocked.SetErr(&bytes.Buffer{})
	require.ErrorIs(blocked.Execute(), backup.ErrRepoLocked)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c := newBackupPruneCommand()
	c.SetContext(testInvocationContext(ctx, &config.Config{Data: config.DataConfig{DataDir: data}}, invocationOptions{}))
	c.SetArgs([]string{"--repo", repo.Path(".")})
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	require.ErrorIs(c.Execute(), context.Canceled)
	snapshots, err = repo.ListSnapshots()
	require.NoError(err)
	assert.Len(snapshots, 2)
}

func TestBackupRetainRequiresExplicitPositiveWindow(t *testing.T) {
	for _, value := range []string{"0", "-1"} {
		t.Run(value, func(t *testing.T) {
			cmd := newBackupRetainCommand()
			cmd.SetArgs([]string{"--keep-last", value})
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			require.ErrorContains(t, cmd.Execute(), "--keep-last must be at least one")
		})
	}
}
