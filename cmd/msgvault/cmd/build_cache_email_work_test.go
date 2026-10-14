package cmd

import (
	"context"
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
	"go.kenn.io/msgvault/internal/testutil"
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

func TestBuildCacheLocalWorkRecoveryNativeAndCSV(t *testing.T) {
	for _, mode := range []string{"native", "csv"} {
		t.Run(mode, func(t *testing.T) {
			requirements := require.New(t)
			assertions := assert.New(t)
			ctx := context.Background()
			if mode == "csv" {
				t.Setenv("MSGVAULT_FORCE_CSV_SNAPSHOT", "1")
			}
			root := t.TempDir()
			dbPath, cache := filepath.Join(root, "synthetic.db"), filepath.Join(root, "cache")
			st, err := store.Open(dbPath)
			requirements.NoError(err)
			defer func() { requirements.NoError(st.Close()) }()
			requirements.NoError(st.InitSchema())
			var targets []store.EmailWorkActionTarget
			var sourceIDs []int64
			var ids []int64
			var originalFrom, originalTo int64
			for i := range 3 {
				owner := fmt.Sprintf("account-%d@example.test", i)
				source, err := st.GetOrCreateSource("gmail", owner)
				requirements.NoError(err)
				sourceIDs = append(sourceIDs, source.ID)
				requirements.NoError(st.AddAccountIdentity(source.ID, owner, "manual"))
				conversation, err := st.EnsureConversation(source.ID, "source-thread", "[QA] independent mail")
				requirements.NoError(err)
				from, err := st.EnsureParticipant("requester@example.test", "[QA] Requester", "example.test")
				requirements.NoError(err)
				to, err := st.EnsureParticipant(owner, "[QA] Account", "example.test")
				requirements.NoError(err)
				if i == 0 {
					originalFrom = from
					originalTo = to
				}
				id, err := st.PersistMessage(&store.MessagePersistData{Message: &store.Message{SourceID: source.ID, ConversationID: conversation, SourceMessageID: fmt.Sprintf("message-%d", i), MessageType: "email", Subject: sql.NullString{String: "[QA] Same subject is not identity", Valid: true}, SentAt: sql.NullTime{Time: time.Date(2026, 1, 1, 12, i, 0, 0, time.UTC), Valid: true}}, RawMIME: []byte(fmt.Sprintf("Message-ID: <message-%d@example.test>\r\n\r\n[QA]", i)), Recipients: []store.RecipientSet{{Type: "from", ParticipantIDs: []int64{from}, EmailAddresses: []string{"requester@example.test"}}, {Type: "to", ParticipantIDs: []int64{to}, EmailAddresses: []string{owner}}}})
				requirements.NoError(err)
				ids = append(ids, id)
				seed, err := st.EmailWorkSeedForMessage(ctx, id)
				requirements.NoError(err)
				targets = append(targets, store.EmailWorkActionTarget{UID: seed.UID, SourceID: source.ID})
			}
			_, err = buildCache(dbPath, cache, true)
			requirements.NoError(err)
			engine, err := query.NewDuckDBEngine(cache, dbPath, st.DB())
			requirements.NoError(err)
			defer func() { requirements.NoError(engine.Close()) }()
			request := query.ExploreRequest{Context: query.Context{RowUnit: query.RowUnitEmailWork, WorkAuthorityKey: "synthetic-reader"}}
			before, err := engine.Explore(ctx, request)
			requirements.NoError(err)
			assertions.Equal(int64(3), before.TotalCount, "same subject never automatically merges unrelated mail")
			snapshot, err := st.EmailWorkRecoverySnapshot(ctx)
			requirements.NoError(err)
			linked, err := st.CreateEmailWorkAction(ctx, store.EmailWorkActionInput{UID: strings.Repeat("a", 32), Kind: "link", Targets: targets[:2], Snapshot: snapshot, ActorKind: "user", ActorUserID: 7})
			requirements.NoError(err)
			published, err := buildCache(dbPath, cache, false)
			requirements.NoError(err)
			assertions.False(published.Skipped, "local change invalidates a cache with unchanged max message ID")
			grouped, err := engine.Explore(ctx, request)
			requirements.NoError(err)
			assertions.Equal(int64(2), grouped.TotalCount)
			for _, inspectorCase := range []struct {
				name    string
				context query.Context
				count   int64
			}{
				{name: "all work", context: request.Context, count: 2},
				{name: "linked sources", context: query.Context{RowUnit: query.RowUnitEmailWork, WorkAuthorityKey: "synthetic-reader", SourceIDs: sourceIDs[:2]}, count: 1},
				{name: "excluded bridge", context: query.Context{RowUnit: query.RowUnitEmailWork, WorkAuthorityKey: "synthetic-reader", SourceIDs: []int64{sourceIDs[0], sourceIDs[2]}}, count: 2},
				{name: "individual default", context: query.Context{}, count: 3},
			} {
				people, err := engine.SearchPeople(ctx, query.PersonSearchRequest{Explore: query.ExploreRequest{Context: inspectorCase.context}, Query: "requester@example.test"})
				requirements.NoError(err, inspectorCase.name)
				requirements.Len(people.Rows, 1, inspectorCase.name)
				assertions.Equal(inspectorCase.count, people.Rows[0].ActivityCount, inspectorCase.name+" people search")
				person, err := engine.GetPerson(ctx, originalFrom, inspectorCase.context, nil)
				requirements.NoError(err, inspectorCase.name)
				requirements.NotNil(person)
				assertions.Equal(inspectorCase.count, person.ActivityCount, inspectorCase.name+" person detail")
				summary, err := engine.GetPersonSummary(ctx, originalFrom, query.ExploreRequest{Context: inspectorCase.context}, nil)
				requirements.NoError(err, inspectorCase.name)
				requirements.Len(summary.Rows, 1)
				assertions.Equal(inspectorCase.count, summary.Rows[0].ActivityCount, inspectorCase.name+" person summary")
				domains, err := engine.SearchDomains(ctx, query.DomainSearchRequest{Explore: query.ExploreRequest{Context: inspectorCase.context}, Query: "example.test"})
				requirements.NoError(err, inspectorCase.name)
				requirements.Len(domains.Rows, 1)
				assertions.Equal(inspectorCase.count, domains.Rows[0].ActivityCount, inspectorCase.name+" domain search")
				relationships, err := engine.Relationships(ctx, query.RelationshipsRequest{Context: inspectorCase.context, ShowAll: true, Now: time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)})
				requirements.NoError(err, inspectorCase.name)
				requirements.Len(relationships.Rows, 1)
				assertions.InDelta(float64(inspectorCase.count), relationships.Rows[0].Signals.ReceivedFromThem, 0, inspectorCase.name+" relationship units")

				domain, err := engine.GetDomain(ctx, "example.test", inspectorCase.context)
				requirements.NoError(err, inspectorCase.name)
				requirements.NotNil(domain)
				assertions.Equal(inspectorCase.count, domain.ActivityCount, inspectorCase.name+" domain detail")
			}

			scoped := request
			scoped.Context.SourceIDs = []int64{sourceIDs[0], sourceIDs[2]}
			separate, err := engine.Explore(ctx, scoped)
			requirements.NoError(err)
			assertions.Equal(int64(2), separate.TotalCount)
			for _, row := range separate.Rows {
				assertions.Equal(int64(1), row.MessageCount, "hidden B cannot supply a local bridge")
			}
			snapshot, err = st.EmailWorkRecoverySnapshot(ctx)
			requirements.NoError(err)
			_, err = st.CreateEmailWorkAction(ctx, store.EmailWorkActionInput{UID: strings.Repeat("b", 32), Kind: "handled", Targets: targets[:2], Snapshot: snapshot, ActorKind: "user", ActorUserID: 7})
			requirements.NoError(err)
			_, err = buildCache(dbPath, cache, false)
			requirements.NoError(err)
			members, err := engine.EmailWorkMembers(ctx, query.EmailWorkMembersRequest{Explore: request, UID: targets[0].UID})
			requirements.NoError(err)
			nativeSnapshot, err := st.EmailWorkRecoverySnapshot(ctx)
			requirements.NoError(err)
			assertions.Equal(nativeSnapshot, members.RecoverySnapshot, "publication uses the exact pinned authoritative native snapshot")
			assertions.Equal("handled", members.Summary.State)
			assertions.Empty(members.EvidenceMessageIDs, "local assertion is distinct from a sent reply")
			assertions.Contains(members.Summary.Reasons, "local_confirmation")
			assertions.Len(members.LocalActions, 2)
			assertions.Len(members.Members, 2)
			_, err = st.DB().Exec("UPDATE messages SET is_read=FALSE WHERE id=?", ids[0])
			requirements.NoError(err)
			preference, err := engine.EmailWorkMembers(ctx, query.EmailWorkMembersRequest{Explore: request, UID: targets[0].UID})
			requirements.NoError(err)
			requirements.NotNil(preference.RequestedAnchor)
			assertions.Equal("unread", preference.RequestedAnchor.LocalReadState)
			assertions.Equal("handled", preference.Summary.State, "local read marker must not change local handling")
			assertions.Equal(members.RequestedAnchor.ReadState, preference.RequestedAnchor.ReadState, "local read marker must not replace published mailbox read state")

			// A later real incoming message is outside the fixed local confirmation.
			incoming, err := st.PersistMessage(&store.MessagePersistData{Message: &store.Message{SourceID: sourceIDs[0], ConversationID: members.Members[0].ConversationID, SourceMessageID: "later-inbound", MessageType: "email", SentAt: sql.NullTime{Time: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC), Valid: true}}, RawMIME: []byte("Message-ID: <later@example.test>\r\nIn-Reply-To: <message-0@example.test>\r\n\r\n[QA] new request"), Recipients: []store.RecipientSet{{Type: "from", ParticipantIDs: []int64{originalFrom}, EmailAddresses: []string{"requester@example.test"}}, {Type: "to", ParticipantIDs: []int64{originalTo}, EmailAddresses: []string{"account-0@example.test"}}}})
			requirements.NoError(err)
			_, err = buildCache(dbPath, cache, false)
			requirements.NoError(err)
			_, err = engine.EmailWorkMembers(ctx, query.EmailWorkMembersRequest{Explore: request, UID: targets[0].UID, ExpectedCacheRevision: members.CacheRevision})
			requirements.ErrorIs(err, query.ErrEmailWorkRevisionChanged)
			current, err := engine.EmailWorkMembers(ctx, query.EmailWorkMembersRequest{Explore: request, UID: targets[0].UID})
			requirements.NoError(err)
			assertions.Equal("actionable", current.Summary.State)
			assertions.Equal([]int64{incoming}, current.OutstandingMessageIDs)
			snapshot, err = st.EmailWorkRecoverySnapshot(ctx)
			requirements.NoError(err)
			_, err = st.UndoEmailWorkAction(ctx, linked.UID, snapshot, linked.Revision)
			requirements.NoError(err)
			_, err = buildCache(dbPath, cache, false)
			requirements.NoError(err)
			split, err := engine.EmailWorkMembers(ctx, query.EmailWorkMembersRequest{Explore: request, UID: targets[1].UID})
			requirements.NoError(err)
			assertions.Equal(targets[1].UID, split.UID, "undo recomputes the second immutable alias from its own anchor")
			assertions.Len(split.Members, 1)
			assertions.Equal(ids[1], split.Members[0].MessageID)
			individual, err := engine.Explore(ctx, query.ExploreRequest{})
			requirements.NoError(err)
			assertions.Equal(int64(4), individual.TotalCount, "default individual-message unit remains unchanged")
		})
	}
}

// A zero watermark is a real committed legacy population, not a sentinel for
// exporting all rows again during the next incremental publication.
func TestBuildCacheLegacySignedIDIncremental(t *testing.T) {
	for _, mode := range []string{"native", "csv"} {
		t.Run(mode, func(t *testing.T) {
			r := require.New(t)
			a := assert.New(t)
			if mode == "csv" {
				t.Setenv("MSGVAULT_FORCE_CSV_SNAPSHOT", "1")
			}
			st, path := testutil.NewLegacyNullableSQLiteEmailStore(t)
			cache := filepath.Join(t.TempDir(), "cache")
			source, err := st.GetOrCreateSource("gmail", "qa-signed-cache@example.test")
			r.NoError(err)
			conv, err := st.EnsureConversation(source.ID, "legacy", "[QA]")
			r.NoError(err)
			insert := func(id int64) {
				_, err := st.DB().Exec(`INSERT INTO messages(id,source_id,conversation_id,source_message_id,message_type,sent_at) VALUES (?,?,?,?,NULL,?)`, id, source.ID, conv, fmt.Sprintf("signed-%d", id), "2026-01-01T12:00:00Z")
				r.NoError(err)
			}
			insert(-7)
			insert(0)
			_, err = buildCache(path, cache, true)
			r.NoError(err)
			state, err := query.ReadCacheSyncState(cache)
			r.NoError(err)
			a.Zero(state.LastMessageID)
			insert(10)
			result, err := buildCache(path, cache, false)
			r.NoError(err)
			a.False(result.Skipped)
			engine, err := query.NewDuckDBEngine(cache, path, st.DB())
			r.NoError(err)
			defer func() { r.NoError(engine.Close()) }()
			rows, err := engine.Explore(context.Background(), query.ExploreRequest{})
			r.NoError(err)
			a.Equal(int64(3), rows.TotalCount, "incremental publication must not duplicate zero/negative existing rows")
			ids := []int64{}
			for _, row := range rows.Rows {
				r.NotNil(row.AnchorMessageID)
				ids = append(ids, *row.AnchorMessageID)
			}
			a.ElementsMatch([]int64{-7, 0, 10}, ids)
		})
	}
}
