package cmd

import (
	"context"
	"database/sql"
	"fmt"
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
	unreadLabel, err := st.EnsureLabel(source.ID, "UNREAD", "UNREAD", "system")
	require.NoError(t, err)
	require.NoError(t, st.AddMessageLabels(id, []int64{label, unreadLabel}))
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
	state := func() []string {
		engine, err := query.NewDuckDBEngine(cache, dbPath, nil)
		require.NoError(t, err)
		defer func() { require.NoError(t, engine.Close()) }()
		result, err := engine.Explore(context.Background(), query.ExploreRequest{})
		require.NoError(t, err)
		require.Len(t, result.Rows, 1)
		assert.Equal(t, source.ID, result.Rows[0].SourceID)
		require.NotNil(t, result.Rows[0].AnchorMessageID)
		assert.Equal(t, id, *result.Rows[0].AnchorMessageID)
		return []string{result.Rows[0].InboxState, result.Rows[0].ReadState}
	}
	assert.Equal(t, int64(1), countInbox())
	assert.Equal(t, []string{"inbox", "unread"}, state())
	removed, err := st.RemoveLabelBySourceMessageIDs(source.ID, "INBOX", []string{"mailbox-message"})
	require.NoError(t, err)
	require.Equal(t, int64(1), removed)
	rebuilt, err := buildCache(dbPath, cache, false)
	require.NoError(t, err)
	require.False(t, rebuilt.Skipped, "the ordinary build must refresh changed existing mailbox facts without a new message ID")
	assert.Equal(t, first.MaxMessageID, rebuilt.MaxMessageID)
	assert.Zero(t, countInbox())
	assert.Equal(t, []string{"outside", "unread"}, state())
	repeated, err := buildCache(dbPath, cache, false)
	require.NoError(t, err)
	assert.True(t, repeated.Skipped, "unchanged mailbox facts keep the ordinary no-op path")
}

func TestBuildCacheThreeAccountInboxStateFilters(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "inboxes.db")
	cache := filepath.Join(dir, "analytics")
	st, err := store.Open(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()
	require.NoError(t, st.InitSchema())
	var sources, inboxIDs, unreadIDs, combinedIDs []int64
	for account := range 3 {
		source, err := st.GetOrCreateSource("gmail", fmt.Sprintf("account-%d@example.com", account))
		require.NoError(t, err)
		sources = append(sources, source.ID)
		conv, err := st.EnsureConversation(source.ID, "same-provider-thread", "[QA] Same subject across accounts")
		require.NoError(t, err)
		inbox, err := st.EnsureLabel(source.ID, "INBOX", "INBOX", "system")
		require.NoError(t, err)
		unread, err := st.EnsureLabel(source.ID, "UNREAD", "UNREAD", "system")
		require.NoError(t, err)
		for item := range 3 {
			id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conv, SourceMessageID: fmt.Sprintf("item-%d", item), MessageType: "email", Subject: sql.NullString{String: "[QA] Independent inbox copies", Valid: true}, SentAt: sql.NullTime{Time: time.Date(2026, 1, 1, 12, item, 0, 0, time.UTC), Valid: true}})
			require.NoError(t, err)
			var labels []int64
			if item != 2 {
				labels = append(labels, inbox)
				inboxIDs = append(inboxIDs, id)
			}
			if item != 1 {
				labels = append(labels, unread)
				unreadIDs = append(unreadIDs, id)
			}
			if item == 0 {
				combinedIDs = append(combinedIDs, id)
			}
			require.NoError(t, st.AddMessageLabels(id, labels))
		}
	}
	_, err = buildCache(dbPath, cache, false)
	require.NoError(t, err)
	engine, err := query.NewDuckDBEngine(cache, dbPath, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, engine.Close()) }()
	for _, test := range []struct {
		name    string
		context query.Context
		want    []int64
	}{
		{"Inbox only", query.Context{SourceIDs: sources, InboxStates: []string{"inbox"}}, inboxIDs},
		{"Unread only", query.Context{SourceIDs: sources, ReadStates: []string{"unread"}}, unreadIDs},
		{"Inbox and Unread", query.Context{SourceIDs: sources, InboxStates: []string{"inbox"}, ReadStates: []string{"unread"}}, combinedIDs},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := engine.Explore(context.Background(), query.ExploreRequest{Context: test.context})
			require.NoError(t, err)
			var ids []int64
			for _, row := range result.Rows {
				require.NotNil(t, row.AnchorMessageID)
				ids = append(ids, *row.AnchorMessageID)
				assert.Contains(t, sources, row.SourceID)
				assert.Equal(t, "gmail", row.SourceType)
				assert.NotEmpty(t, row.SourceIdentifier)
			}
			assert.ElementsMatch(t, test.want, ids)
			assert.Equal(t, int64(len(test.want)), result.TotalCount)
		})
	}
}

func TestBuildCacheIMAPFlagsRefreshAndCSVFallback(t *testing.T) {
	for _, csv := range []bool{false, true} {
		name := "native"
		if csv {
			name = "csv-fallback"
		}
		// '=' in a temp-directory component is inferred as a Hive partition.
		t.Run(name, func(t *testing.T) {
			if csv {
				t.Setenv("MSGVAULT_FORCE_CSV_SNAPSHOT", "1")
			}
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "imap.db")
			cache := filepath.Join(dir, "analytics")
			st, err := store.Open(dbPath)
			require.NoError(t, err)
			defer func() { require.NoError(t, st.Close()) }()
			require.NoError(t, st.InitSchema())
			source, err := st.GetOrCreateSource("imap", "imap@example.com")
			require.NoError(t, err)
			conv, err := st.EnsureConversation(source.ID, "imap-thread", "[QA] IMAP flags")
			require.NoError(t, err)
			id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conv, SourceMessageID: "INBOX|1", MessageType: "email", SentAt: sql.NullTime{Time: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), Valid: true}})
			require.NoError(t, err)
			delta := store.IMAPMailboxDelta{Mailbox: "INBOX", Reset: true, State: store.IMAPFolderState{Mailbox: "INBOX", UIDValidity: 1, UIDNext: 2}, Memberships: []store.IMAPMembershipObservation{{UID: 1, SourceMessageID: "INBOX|1", Flags: []string{"\\Seen"}}}}
			require.NoError(t, st.ApplyIMAPMailboxDeltas(source.ID, []store.IMAPMailboxDelta{delta}))
			first, err := buildCache(dbPath, cache, false)
			require.NoError(t, err)
			require.False(t, first.Skipped)
			state := func() []string {
				engine, err := query.NewDuckDBEngine(cache, dbPath, nil)
				require.NoError(t, err)
				defer func() { require.NoError(t, engine.Close()) }()
				result, err := engine.Explore(context.Background(), query.ExploreRequest{})
				require.NoError(t, err)
				require.Len(t, result.Rows, 1)
				require.NotNil(t, result.Rows[0].AnchorMessageID)
				assert.Equal(t, id, *result.Rows[0].AnchorMessageID)
				return []string{result.Rows[0].InboxState, result.Rows[0].ReadState}
			}
			assert.Equal(t, []string{"inbox", "read"}, state())
			delta.Memberships[0].Flags = []string{}
			require.NoError(t, st.ApplyIMAPMailboxDeltas(source.ID, []store.IMAPMailboxDelta{delta}))
			rebuilt, err := buildCache(dbPath, cache, false)
			require.NoError(t, err)
			require.False(t, rebuilt.Skipped)
			assert.Equal(t, first.MaxMessageID, rebuilt.MaxMessageID)
			assert.Equal(t, []string{"inbox", "unread"}, state())
			require.NoError(t, st.ApplyIMAPMailboxDeltas(source.ID, []store.IMAPMailboxDelta{delta}))
			repeated, err := buildCache(dbPath, cache, false)
			require.NoError(t, err)
			assert.True(t, repeated.Skipped)
		})
	}
}
