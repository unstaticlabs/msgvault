package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

func TestBuildCacheMailboxMutationRefreshesAtSameMessageID(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mailbox.db")
	cache := filepath.Join(dir, "analytics")
	st, err := store.Open(dbPath)
	requirements.NoError(err)
	defer func() { requirements.NoError(st.Close()) }()
	requirements.NoError(st.InitSchema())
	source, err := st.GetOrCreateSource("gmail", "mailbox@example.com")
	requirements.NoError(err)
	conv, err := st.EnsureConversation(source.ID, "mailbox-thread", "[QA] Mailbox")
	requirements.NoError(err)
	id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conv, SourceMessageID: "mailbox-message", MessageType: "email", Subject: sql.NullString{String: "[QA] mailbox", Valid: true}, SentAt: sql.NullTime{Time: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), Valid: true}})
	requirements.NoError(err)
	label, err := st.EnsureLabel(source.ID, "INBOX", "INBOX", "system")
	requirements.NoError(err)
	unreadLabel, err := st.EnsureLabel(source.ID, "UNREAD", "UNREAD", "system")
	requirements.NoError(err)
	requirements.NoError(st.AddMessageLabels(id, []int64{label, unreadLabel}))
	first, err := buildCache(dbPath, cache, false)
	requirements.NoError(err)
	requirements.False(first.Skipped)
	countInbox := func() int64 {
		engine, err := query.NewDuckDBEngine(cache, dbPath, nil)
		requirements.NoError(err)
		defer func() { requirements.NoError(engine.Close()) }()
		rows, err := engine.Aggregate(context.Background(), query.ViewLabels, query.DefaultAggregateOptions())
		requirements.NoError(err)
		for _, row := range rows {
			if row.Key == "INBOX" {
				return row.Count
			}
		}
		return 0
	}
	state := func() []string {
		engine, err := query.NewDuckDBEngine(cache, dbPath, nil)
		requirements.NoError(err)
		defer func() { requirements.NoError(engine.Close()) }()
		result, err := engine.Explore(context.Background(), query.ExploreRequest{})
		requirements.NoError(err)
		requirements.Len(result.Rows, 1)
		assertions.Equal(source.ID, result.Rows[0].SourceID)
		requirements.NotNil(result.Rows[0].AnchorMessageID)
		assertions.Equal(id, *result.Rows[0].AnchorMessageID)
		return []string{result.Rows[0].InboxState, result.Rows[0].ReadState}
	}
	assertions.Equal(int64(1), countInbox())
	assertions.Equal([]string{"inbox", "unread"}, state())
	removed, err := st.RemoveLabelBySourceMessageIDs(source.ID, "INBOX", []string{"mailbox-message"})
	requirements.NoError(err)
	requirements.Equal(int64(1), removed)
	rebuilt, err := buildCache(dbPath, cache, false)
	requirements.NoError(err)
	requirements.False(rebuilt.Skipped, "the ordinary build must refresh changed existing mailbox facts without a new message ID")
	assertions.Equal(first.MaxMessageID, rebuilt.MaxMessageID)
	assertions.Zero(countInbox())
	assertions.Equal([]string{"outside", "unread"}, state())
	repeated, err := buildCache(dbPath, cache, false)
	requirements.NoError(err)
	assertions.True(repeated.Skipped, "unchanged mailbox facts keep the ordinary no-op path")
}

func TestBuildCacheThreeAccountInboxStateFilters(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "inboxes.db")
	cache := filepath.Join(dir, "analytics")
	st, err := store.Open(dbPath)
	requirements.NoError(err)
	defer func() { requirements.NoError(st.Close()) }()
	requirements.NoError(st.InitSchema())
	var sources, inboxIDs, unreadIDs, combinedIDs []int64
	for account := range 3 {
		source, err := st.GetOrCreateSource("gmail", fmt.Sprintf("account-%d@example.com", account))
		requirements.NoError(err)
		sources = append(sources, source.ID)
		conv, err := st.EnsureConversation(source.ID, "same-provider-thread", "[QA] Same subject across accounts")
		requirements.NoError(err)
		inbox, err := st.EnsureLabel(source.ID, "INBOX", "INBOX", "system")
		requirements.NoError(err)
		unread, err := st.EnsureLabel(source.ID, "UNREAD", "UNREAD", "system")
		requirements.NoError(err)
		for item := range 3 {
			id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conv, SourceMessageID: fmt.Sprintf("item-%d", item), MessageType: "email", Subject: sql.NullString{String: "[QA] Independent inbox copies", Valid: true}, SentAt: sql.NullTime{Time: time.Date(2026, 1, 1, 12, item, 0, 0, time.UTC), Valid: true}})
			requirements.NoError(err)
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
			requirements.NoError(st.AddMessageLabels(id, labels))
		}
	}
	_, err = buildCache(dbPath, cache, false)
	requirements.NoError(err)
	engine, err := query.NewDuckDBEngine(cache, dbPath, nil)
	requirements.NoError(err)
	defer func() { requirements.NoError(engine.Close()) }()
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
			requirements.NoError(err)
			var ids []int64
			for _, row := range result.Rows {
				requirements.NotNil(row.AnchorMessageID)
				ids = append(ids, *row.AnchorMessageID)
				assertions.Contains(sources, row.SourceID)
				assertions.Equal("gmail", row.SourceType)
				assertions.NotEmpty(row.SourceIdentifier)
			}
			assertions.ElementsMatch(test.want, ids)
			assertions.Equal(int64(len(test.want)), result.TotalCount)
		})
	}
}

func TestBuildCacheIMAPFlagsRefreshAndCSVFallback(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
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
			requirements.NoError(err)
			defer func() { requirements.NoError(st.Close()) }()
			requirements.NoError(st.InitSchema())
			source, err := st.GetOrCreateSource("imap", "imap@example.com")
			requirements.NoError(err)
			conv, err := st.EnsureConversation(source.ID, "imap-thread", "[QA] IMAP flags")
			requirements.NoError(err)
			id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conv, SourceMessageID: "INBOX|1", MessageType: "email", SentAt: sql.NullTime{Time: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), Valid: true}})
			requirements.NoError(err)
			delta := store.IMAPMailboxDelta{Mailbox: "INBOX", Reset: true, State: store.IMAPFolderState{Mailbox: "INBOX", UIDValidity: 1, UIDNext: 2}, Memberships: []store.IMAPMembershipObservation{{UID: 1, SourceMessageID: "INBOX|1", Flags: []string{"\\Seen"}}}}
			requirements.NoError(st.ApplyIMAPMailboxDeltas(source.ID, []store.IMAPMailboxDelta{delta}))
			first, err := buildCache(dbPath, cache, false)
			requirements.NoError(err)
			requirements.False(first.Skipped)
			state := func() []string {
				engine, err := query.NewDuckDBEngine(cache, dbPath, nil)
				requirements.NoError(err)
				defer func() { requirements.NoError(engine.Close()) }()
				result, err := engine.Explore(context.Background(), query.ExploreRequest{})
				requirements.NoError(err)
				requirements.Len(result.Rows, 1)
				requirements.NotNil(result.Rows[0].AnchorMessageID)
				assertions.Equal(id, *result.Rows[0].AnchorMessageID)
				return []string{result.Rows[0].InboxState, result.Rows[0].ReadState}
			}
			assertions.Equal([]string{"inbox", "read"}, state())
			delta.Memberships[0].Flags = []string{}
			requirements.NoError(st.ApplyIMAPMailboxDeltas(source.ID, []store.IMAPMailboxDelta{delta}))
			rebuilt, err := buildCache(dbPath, cache, false)
			requirements.NoError(err)
			requirements.False(rebuilt.Skipped)
			assertions.Equal(first.MaxMessageID, rebuilt.MaxMessageID)
			assertions.Equal([]string{"inbox", "unread"}, state())
			requirements.NoError(st.ApplyIMAPMailboxDeltas(source.ID, []store.IMAPMailboxDelta{delta}))
			repeated, err := buildCache(dbPath, cache, false)
			requirements.NoError(err)
			assertions.True(repeated.Skipped)
		})
	}
}

// A prior publication has neither state column. Rebuilding at the same message
// watermark must replace its shape before an analytical reader uses it.
func TestBuildCacheSchema27MailboxStateUpgrade(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	dir := t.TempDir()
	dbPath, cache := filepath.Join(dir, "mail.db"), filepath.Join(dir, "cache")
	st, err := store.Open(dbPath)
	requirements.NoError(err)
	defer func() { requirements.NoError(st.Close()) }()
	requirements.NoError(st.InitSchema())
	source, err := st.GetOrCreateSource("gmail", "upgrade@example.com")
	requirements.NoError(err)
	conv, err := st.EnsureConversation(source.ID, "upgrade-thread", "[QA] Upgrade")
	requirements.NoError(err)
	id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conv, SourceMessageID: "upgrade-id", MessageType: "email", SentAt: sql.NullTime{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Valid: true}})
	requirements.NoError(err)
	for _, name := range []string{"INBOX", "UNREAD"} {
		label, err := st.EnsureLabel(source.ID, name, name, "system")
		requirements.NoError(err)
		requirements.NoError(st.AddMessageLabels(id, []int64{label}))
	}
	first, err := buildCache(dbPath, cache, false)
	requirements.NoError(err)
	files, err := filepath.Glob(filepath.Join(cache, "messages", "*", "*.parquet"))
	requirements.NoError(err)
	requirements.NotEmpty(files)
	db, err := sql.Open("duckdb", "")
	requirements.NoError(err)
	for _, path := range files {
		old := path + ".previous"
		escape := func(s string) string { return strings.ReplaceAll(filepath.ToSlash(s), "'", "''") }
		_, err = db.Exec(fmt.Sprintf("COPY (SELECT * EXCLUDE(inbox_state,read_state) FROM read_parquet('%s')) TO '%s' (FORMAT PARQUET)", escape(path), escape(old)))
		requirements.NoError(err)
		requirements.NoError(os.Rename(old, path))
	}
	requirements.NoError(db.Close())
	state, err := query.ReadCacheSyncState(cache)
	requirements.NoError(err)
	state.SchemaVersion = 27
	bytes, err := json.Marshal(state)
	requirements.NoError(err)
	requirements.NoError(os.WriteFile(filepath.Join(cache, "_last_sync.json"), bytes, 0o600))
	stale := cacheNeedsBuild(dbPath, cache)
	requirements.True(stale.NeedsBuild)
	requirements.True(stale.FullRebuild)
	rebuilt, err := buildCache(dbPath, cache, false)
	requirements.NoError(err)
	requirements.False(rebuilt.Skipped)
	assertions.Equal(first.MaxMessageID, rebuilt.MaxMessageID)
	engine, err := query.NewDuckDBEngine(cache, dbPath, nil)
	requirements.NoError(err)
	defer func() { requirements.NoError(engine.Close()) }()
	result, err := engine.Explore(t.Context(), query.ExploreRequest{Context: query.Context{InboxStates: []string{"inbox"}, ReadStates: []string{"unread"}}})
	requirements.NoError(err)
	requirements.Len(result.Rows, 1)
	assertions.Equal(id, *result.Rows[0].AnchorMessageID)
	state, err = query.ReadCacheSyncState(cache)
	requirements.NoError(err)
	assertions.Equal(query.CacheSchemaVersion, state.SchemaVersion)
}
