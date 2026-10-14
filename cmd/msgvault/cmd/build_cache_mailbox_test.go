package cmd

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

func TestBuildCacheMailboxMutationRefreshesAtSameMessageID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mailbox.db")
	cache := filepath.Join(dir, "analytics")
	st, err := store.Open(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()
	require.NoError(t, st.InitSchema())
	source, err := st.GetOrCreateSource("gmail", "mailbox@example.com")
	require.NoError(t, err)
	conv, err := st.EnsureConversation(source.ID, "mailbox-thread", "[QA] Mailbox")
	require.NoError(t, err)
	id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conv, SourceMessageID: "mailbox-message", MessageType: "email", Subject: sql.NullString{String: "[QA] mailbox", Valid: true}, SentAt: sql.NullTime{Time: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), Valid: true}})
	require.NoError(t, err)
	label, err := st.EnsureLabel(source.ID, "INBOX", "INBOX", "system")
	require.NoError(t, err)
	require.NoError(t, st.AddMessageLabels(id, []int64{label}))
	first, err := buildCache(dbPath, cache, false)
	require.NoError(t, err)
	require.False(t, first.Skipped)
	countInbox := func() int64 {
		engine, err := query.NewDuckDBEngine(cache, dbPath, nil)
		require.NoError(t, err)
		defer func() { require.NoError(t, engine.Close()) }()
		rows, err := engine.Aggregate(context.Background(), query.ViewLabels, query.DefaultAggregateOptions())
		require.NoError(t, err)
		for _, row := range rows {
			if row.Key == "INBOX" {
				return row.Count
			}
		}
		return 0
	}
	assert.Equal(t, int64(1), countInbox())
	removed, err := st.RemoveLabelBySourceMessageIDs(source.ID, "INBOX", []string{"mailbox-message"})
	require.NoError(t, err)
	require.Equal(t, int64(1), removed)
	rebuilt, err := buildCache(dbPath, cache, false)
	require.NoError(t, err)
	require.False(t, rebuilt.Skipped, "the ordinary build must refresh changed existing mailbox facts without a new message ID")
	assert.Equal(t, first.MaxMessageID, rebuilt.MaxMessageID)
	assert.Zero(t, countInbox())
	repeated, err := buildCache(dbPath, cache, false)
	require.NoError(t, err)
	assert.True(t, repeated.Skipped, "unchanged mailbox facts keep the ordinary no-op path")
}
