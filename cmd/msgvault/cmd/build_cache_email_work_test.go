package cmd

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

func TestBuildCacheEmailWorkNativeAndCSVPublication(t *testing.T) {
	for _, mode := range []string{"native", "csv"} {
		t.Run(mode, func(t *testing.T) {
			requirements := require.New(t)
			assertions := assert.New(t)
			if mode == "csv" {
				t.Setenv("MSGVAULT_FORCE_CSV_SNAPSHOT", "1")
			}
			dbPath := filepath.Join(t.TempDir(), "work.db")
			cache := filepath.Join(t.TempDir(), "cache")
			st, err := store.Open(dbPath)
			requirements.NoError(err)
			defer func() { requirements.NoError(st.Close()) }()
			requirements.NoError(st.InitSchema())
			var ids []int64
			var imapSource int64
			for _, sourceType := range []string{"gmail", "imap"} {
				source, err := st.GetOrCreateSource(sourceType, "qa-"+sourceType+"@example.test")
				requirements.NoError(err)
				requirements.NoError(st.AddAccountIdentity(source.ID, source.Identifier, "manual"))
				conv, err := st.EnsureConversation(source.ID, "source-thread", "[QA] Published work")
				requirements.NoError(err)
				id, err := st.PersistMessage(&store.MessagePersistData{Message: &store.Message{SourceID: source.ID, ConversationID: conv, SourceMessageID: "qa-mail", MessageType: "email", SentAt: sql.NullTime{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Valid: true}}, RawMIME: []byte("Message-ID: <" + sourceType + "@example.test>\r\n\r\n[QA] body")})
				requirements.NoError(err)
				ids = append(ids, id)
				if sourceType == "imap" {
					imapSource = source.ID
				}
				role := "sent"
				if sourceType == "imap" {
					role = ""
				}
				labels, err := st.EnsureLabelsBatch(source.ID, map[string]store.LabelInfo{"SENT": {Name: "SENT", Type: "system", SystemRole: role}})
				requirements.NoError(err)
				requirements.NoError(st.AddMessageLabels(id, []int64{labels["SENT"]}))
			}
			_, err = buildCache(dbPath, cache, true)
			requirements.NoError(err)
			db, err := sql.Open("duckdb", "")
			requirements.NoError(err)
			defer func() { requirements.NoError(db.Close()) }()
			parquet := func(dataset string) string {
				return strings.ReplaceAll(filepath.ToSlash(filepath.Join(cache, dataset, "*.parquet")), "'", "''")
			}
			var count int
			requirements.NoError(db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM read_parquet('%s')", parquet(query.DatasetEmailWorkFacts))).Scan(&count))
			assertions.Equal(2, count)
			requirements.NoError(db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM read_parquet('%s')", parquet(query.DatasetEmailWorkSeeds))).Scan(&count))
			assertions.Equal(2, count)
			requirements.NoError(db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM read_parquet('%s')", parquet(query.DatasetEmailWorkIdentities))).Scan(&count))
			assertions.Equal(2, count)
			var sent, draft, complete bool
			requirements.NoError(db.QueryRow(fmt.Sprintf("SELECT native_sent,native_draft,provider_complete FROM read_parquet('%s') WHERE message_id=?", parquet(query.DatasetEmailWorkDelivery)), ids[0]).Scan(&sent, &draft, &complete))
			assertions.True(sent)
			assertions.False(draft)
			assertions.True(complete)
			requirements.NoError(db.QueryRow(fmt.Sprintf("SELECT native_sent,native_draft,provider_complete FROM read_parquet('%s') WHERE message_id=?", parquet(query.DatasetEmailWorkDelivery)), ids[1]).Scan(&sent, &draft, &complete))
			assertions.False(sent, "an IMAP folder named SENT is not completed activity evidence")
			assertions.False(complete, "absent authoritative membership stays unavailable")
			// The provider role and actual membership, rather than a folder's
			// display name, establish Sent. Flags change without a new ID.
			_, err = st.EnsureLabelsBatch(imapSource, map[string]store.LabelInfo{"SENT": {Name: "SENT", Type: "system", SystemRole: "sent"}})
			requirements.NoError(err)
			delta := store.IMAPMailboxDelta{Mailbox: "SENT", State: store.IMAPFolderState{Mailbox: "SENT", UIDValidity: 1, UIDNext: 2}, Memberships: []store.IMAPMembershipObservation{{Mailbox: "SENT", UIDValidity: 1, UID: 1, SourceMessageID: "qa-mail", Flags: []string{`\Draft`}}}}
			requirements.NoError(st.ApplyIMAPMailboxDeltas(imapSource, []store.IMAPMailboxDelta{delta}))
			result, err := buildCache(dbPath, cache, false)
			requirements.NoError(err)
			assertions.False(result.Skipped)
			requirements.NoError(db.QueryRow(fmt.Sprintf("SELECT native_sent,native_draft,provider_complete FROM read_parquet('%s') WHERE message_id=?", parquet(query.DatasetEmailWorkDelivery)), ids[1]).Scan(&sent, &draft, &complete))
			assertions.True(sent)
			assertions.True(draft, "a saved provider Draft flag blocks completed activity")
			assertions.True(complete)
			delta.Memberships[0].Flags = []string{}
			requirements.NoError(st.ApplyIMAPMailboxDeltas(imapSource, []store.IMAPMailboxDelta{delta}))
			result, err = buildCache(dbPath, cache, false)
			requirements.NoError(err)
			assertions.False(result.Skipped, "same-max-ID flag change must refresh delivery facts")
			requirements.NoError(db.QueryRow(fmt.Sprintf("SELECT native_sent,native_draft,provider_complete FROM read_parquet('%s') WHERE message_id=?", parquet(query.DatasetEmailWorkDelivery)), ids[1]).Scan(&sent, &draft, &complete))
			assertions.True(sent)
			assertions.False(draft)
			assertions.True(complete)
		})
	}
}
