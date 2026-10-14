package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/mime"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func linkageMessage(t *testing.T, st *store.Store, sourceID int64, key string) *store.Message {
	t.Helper()
	conversationID, err := st.EnsureConversation(sourceID, "thread-"+key, "[QA] Email linkage")
	require.NoError(t, err)
	return &store.Message{SourceID: sourceID, ConversationID: conversationID, SourceMessageID: key, MessageType: store.MessageTypeEmail, Subject: sql.NullString{String: "[QA] Email linkage", Valid: true}}
}

func TestEmailLinkagePersistsWithActualMessageAndRaw(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gmail", "qa-owner@example.test")
	requirements.NoError(err)
	raw := []byte("Message-ID: <incoming@example.test>\r\nIn-Reply-To: <root@example.test>\r\n\r\nOriginal body")
	id, err := st.PersistMessage(&store.MessagePersistData{Message: linkageMessage(t, st, source.ID, "incoming"), RawMIME: raw})
	requirements.NoError(err)
	facts, found, err := st.EmailLinkageEvidence(context.Background(), id)
	requirements.NoError(err)
	requirements.True(found)
	assertions.Equal("incoming@example.test", facts.MessageID)
	assertions.Equal([]string{"root@example.test"}, facts.Replies)
	revision, err := st.DerivedDataRevision()
	requirements.NoError(err)
	requirements.NoError(st.UpsertMessageRaw(id, raw))
	after, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(revision, after)
	// A body change leaves the actual identity facts unchanged.
	requirements.NoError(st.UpsertMessageRaw(id, []byte("Message-ID: <incoming@example.test>\r\nIn-Reply-To: <root@example.test>\r\n\r\nNew body")))
	after, err = st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(revision, after)
	requirements.NoError(st.UpsertMessageRaw(id, []byte("Message-ID: <incoming@example.test>\r\nIn-Reply-To: <different@example.test>\r\n\r\nBody")))
	after, err = st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(revision+1, after)
	facts, found, err = st.EmailLinkageEvidence(context.Background(), id)
	requirements.NoError(err)
	requirements.True(found)
	assertions.Equal([]string{"different@example.test"}, facts.Replies)
	requirements.NoError(st.UpsertMessageRawWithFormat(id, []byte(`{"source":"synthetic"}`), "json"))
	facts, found, err = st.EmailLinkageEvidence(context.Background(), id)
	requirements.NoError(err)
	assertions.Zero(facts.Version)
	assertions.False(found)
}

func TestEmailLinkageRawAndFactsRollbackTogether(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gmail", "qa-owner@example.test")
	requirements.NoError(err)
	original := []byte("Message-ID: <original@example.test>\r\n\r\nBody")
	id, err := st.PersistMessage(&store.MessagePersistData{Message: linkageMessage(t, st, source.ID, "rollback"), RawMIME: original})
	requirements.NoError(err)
	before, err := st.DerivedDataRevision()
	requirements.NoError(err)
	_, err = st.DB().Exec(`DROP TABLE email_linkage_facts`)
	requirements.NoError(err)
	requirements.Error(st.UpsertMessageRaw(id, []byte("Message-ID: <changed@example.test>\r\n\r\nBody")))
	raw, err := st.GetMessageRaw(id)
	requirements.NoError(err)
	assertions.Equal(original, raw)
	after, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(before, after)
}

func TestEmailLinkageBackfillResumesPastCorruptRaw(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gmail", "qa-first@example.test")
	requirements.NoError(err)
	other, err := st.GetOrCreateSource("gmail", "qa-second@example.test")
	requirements.NoError(err)
	var ids []int64
	for i := range 3 {
		id, err := st.UpsertMessage(linkageMessage(t, st, source.ID, fmt.Sprintf("old-%d", i)))
		requirements.NoError(err)
		ids = append(ids, id)
		raw := []byte(fmt.Sprintf("Message-ID: <old-%d@example.test>\r\n\r\nBody", i))
		var compression any
		if i == 1 {
			raw = []byte("[QA] corrupt compression")
			compression = "zlib"
		}
		_, err = st.DB().Exec(st.Rebind(`INSERT INTO message_raw (message_id, raw_data, raw_format, compression) VALUES (?, ?, 'mime', ?)`), id, raw, compression)
		requirements.NoError(err)
	}
	otherID, err := st.UpsertMessage(linkageMessage(t, st, other.ID, "other"))
	requirements.NoError(err)
	_, err = st.DB().Exec(st.Rebind(`INSERT INTO message_raw (message_id, raw_data, raw_format) VALUES (?, ?, 'mime')`), otherID, []byte("Message-ID: <other@example.test>\r\n\r\nBody"))
	requirements.NoError(err)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = st.BackfillEmailLinkage(cancelled, source.ID, 1)
	requirements.Error(err)
	for i, id := range ids {
		result, err := st.BackfillEmailLinkage(context.Background(), source.ID, 1)
		requirements.NoError(err)
		assertions.Equal(1, result.Processed)
		assertions.Equal(id, result.LastMessageID)
		assertions.Equal(i == 2, result.Done)
		facts, found, err := st.EmailLinkageEvidence(context.Background(), id)
		requirements.NoError(err)
		requirements.True(found)
		if i == 1 {
			assertions.Equal("invalid_raw", facts.Outcome)
		} else {
			assertions.Equal(fmt.Sprintf("old-%d@example.test", i), facts.MessageID)
		}
	}
	before, err := st.DerivedDataRevision()
	requirements.NoError(err)
	again, err := st.BackfillEmailLinkage(context.Background(), source.ID, 1)
	requirements.NoError(err)
	assertions.Zero(again.Processed)
	assertions.True(again.Done)
	after, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(before, after)
	facts, found, err := st.EmailLinkageEvidence(context.Background(), otherID)
	requirements.NoError(err)
	assertions.Zero(facts.Version)
	assertions.False(found)
	// A later raw repair updates the previously corrupt message atomically,
	// without resetting its backfill checkpoint or contacting a provider.
	requirements.NoError(st.UpsertMessageRaw(ids[1], []byte("Message-ID: <repaired@example.test>\r\n\r\nBody")))
	facts, found, err = st.EmailLinkageEvidence(context.Background(), ids[1])
	requirements.NoError(err)
	requirements.True(found)
	assertions.Equal("complete", facts.Outcome)
	assertions.Equal("repaired@example.test", facts.MessageID)
}

func TestEmailLinkageSchemaUpgradeAndRepeatPreserveMessages(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gmail", "qa-upgrade@example.test")
	requirements.NoError(err)
	id, err := st.UpsertMessage(linkageMessage(t, st, source.ID, "existing"))
	requirements.NoError(err)
	_, err = st.DB().Exec(`DROP TABLE email_linkage_facts`)
	requirements.NoError(err)
	_, err = st.DB().Exec(`DROP TABLE email_linkage_checkpoints`)
	requirements.NoError(err)
	_, err = st.DB().Exec(`DELETE FROM applied_migrations WHERE name = 'email_linkage_schema'`)
	requirements.NoError(err)
	requirements.NoError(st.InitSchema())
	requirements.NoError(st.InitSchema())
	var count int
	requirements.NoError(st.DB().QueryRow(st.Rebind(`SELECT COUNT(*) FROM messages WHERE id = ?`), id).Scan(&count))
	assertions.Equal(1, count)
	facts, found, err := st.EmailLinkageEvidence(context.Background(), id)
	requirements.NoError(err)
	assertions.Zero(facts.Version)
	assertions.False(found)
	applied, err := st.IsMigrationApplied("email_linkage_schema")
	requirements.NoError(err)
	assertions.True(applied)
	requirements.NoError(st.UpsertMessageRaw(id, []byte("Message-ID: <existing@example.test>\r\n\r\nBody")))
	facts, found, err = st.EmailLinkageEvidence(context.Background(), id)
	requirements.NoError(err)
	requirements.True(found)
	assertions.Equal(mime.EmailLinkageVersion, facts.Version)
}

func TestEmailLinkageBackfillPreservesMissingRawAndRepairsEarlierMissingSeed(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	st := testutil.NewTestStore(t)
	ctx := context.Background()
	source, err := st.GetOrCreateSource("gmail", "qa-old-archive@example.test")
	requirements.NoError(err)
	missing, err := st.UpsertMessage(linkageMessage(t, st, source.ID, "missing"))
	requirements.NoError(err)
	unsupported, err := st.UpsertMessage(linkageMessage(t, st, source.ID, "unsupported"))
	requirements.NoError(err)
	requirements.NoError(st.UpsertMessageRawWithFormat(unsupported, []byte(`{"synthetic":true}`), "json"))
	// Model an archive whose messages predate the additive identity schema.
	_, err = st.DB().Exec(`DELETE FROM email_work_seeds`)
	requirements.NoError(err)
	result, err := st.BackfillEmailLinkage(ctx, source.ID, 10)
	requirements.NoError(err)
	assertions.Equal(2, result.Processed)
	assertions.True(result.Done)
	facts, found, err := st.EmailLinkageEvidence(ctx, missing)
	requirements.NoError(err)
	requirements.True(found)
	assertions.Equal("missing_raw", facts.Outcome)
	facts, found, err = st.EmailLinkageEvidence(ctx, unsupported)
	requirements.NoError(err)
	requirements.True(found)
	assertions.Equal("unsupported_raw", facts.Outcome)
	seed, err := st.EmailWorkSeedForMessage(ctx, missing)
	requirements.NoError(err)
	assertions.Equal(missing, seed.AnchorMessageID)
	oldRevision, err := st.DerivedDataRevision()
	requirements.NoError(err)
	// A pre-checkpoint missing allocation is repaired, even though the max message
	// ID is unchanged; repair is cache-visible and the checkpoint cannot regress.
	_, err = st.DB().Exec(st.Rebind(`DELETE FROM email_work_seeds WHERE live_message_id = ?`), missing)
	requirements.NoError(err)
	repaired, err := st.BackfillEmailLinkage(ctx, source.ID, 10)
	requirements.NoError(err)
	assertions.Equal(1, repaired.Processed)
	assertions.Equal(1, repaired.Changed)
	assertions.Equal(unsupported, repaired.LastMessageID)
	after, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(oldRevision+1, after)
	again, err := st.BackfillEmailLinkage(ctx, source.ID, 10)
	requirements.NoError(err)
	assertions.Zero(again.Processed)
	after, err = st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(oldRevision+1, after)
	requirements.NoError(st.UpsertMessageRaw(missing, []byte("Message-ID: <recovered@example.test>\r\n\r\n[QA] Body")))
	facts, found, err = st.EmailLinkageEvidence(ctx, missing)
	requirements.NoError(err)
	requirements.True(found)
	assertions.Equal("complete", facts.Outcome)
}

// Native raw revision is independent, transactional and idempotent on both
// authoritative backends, including an existing archive without the new key.
func TestMessageRawRevisionLegacyAndAtomicWrites(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	ctx := context.Background()
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gmail", "qa-raw@example.test")
	requirements.NoError(err)
	raw := []byte("Message-ID: <raw@example.test>\r\n\r\n[QA] Original")
	id, err := st.PersistMessage(&store.MessagePersistData{Message: linkageMessage(t, st, source.ID, "raw-revision"), RawMIME: raw})
	requirements.NoError(err)
	revision, err := st.MessageRawRevision(ctx)
	requirements.NoError(err)
	assertions.Equal(int64(1), revision)
	// Legacy archives need no body rewrite or cache rebuild to initialize.
	_, err = st.DB().Exec(`DELETE FROM archive_metadata WHERE key='message_raw_revision'`)
	requirements.NoError(err)
	requirements.NoError(st.InitSchema())
	revision, err = st.MessageRawRevision(ctx)
	requirements.NoError(err)
	assertions.Zero(revision)
	requirements.NoError(st.UpsertMessageRaw(id, raw))
	revision, err = st.MessageRawRevision(ctx)
	requirements.NoError(err)
	assertions.Zero(revision, "identical native bytes do not advance the raw counter")
	derived, err := st.DerivedDataRevision()
	requirements.NoError(err)
	changed := []byte("Message-ID: <raw@example.test>\r\n\r\n[QA] Replacement")
	requirements.NoError(st.UpsertMessageRaw(id, changed))
	revision, err = st.MessageRawRevision(ctx)
	requirements.NoError(err)
	assertions.Equal(int64(1), revision)
	afterDerived, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(derived, afterDerived)
	requirements.NoError(st.UpsertMessageRaw(id, changed))
	revision, err = st.MessageRawRevision(ctx)
	requirements.NoError(err)
	assertions.Equal(int64(1), revision)
	// A failed linkage write rolls bytes and the independent revision back.
	_, err = st.DB().Exec(`DROP TABLE email_linkage_facts`)
	requirements.NoError(err)
	requirements.Error(st.UpsertMessageRaw(id, raw))
	revision, err = st.MessageRawRevision(ctx)
	requirements.NoError(err)
	assertions.Equal(int64(1), revision)
	restored, err := st.GetMessageRaw(id)
	requirements.NoError(err)
	assertions.Equal(changed, restored)
}

// Legacy physical IDs and NULL types are native archive facts, not new IDs to
// allocate or renumber. Preparation must repair them behind its watermark.
func TestEmailLinkageBackfillLegacyTypesAndSignedIDs(t *testing.T) {
	r := require.New(t)
	a := assert.New(t)
	ctx := context.Background()
	st, _ := testutil.NewLegacyNullableEmailStore(t)
	source, err := st.GetOrCreateSource("gmail", "qa-legacy-prepare@example.test")
	r.NoError(err)
	conv, err := st.EnsureConversation(source.ID, "legacy", "[QA]")
	r.NoError(err)
	insert := `INSERT INTO messages(id,source_id,conversation_id,source_message_id,message_type,sent_at)`
	if st.IsPostgreSQL() {
		insert += ` OVERRIDING SYSTEM VALUE`
	}
	insert += ` VALUES (?,?,?,?,?,?)`
	for i, typ := range []any{nil, "", "EMAIL", "email", "chat"} {
		id := int64(i - 2)
		_, err = st.DB().Exec(st.Rebind(insert), id, source.ID, conv, fmt.Sprintf("legacy-%d", id), typ, "2026-01-01T12:00:00Z")
		r.NoError(err)
		if i != 3 {
			_, err = st.DB().Exec(st.Rebind(`INSERT INTO message_raw(message_id,raw_data,raw_format,compression) VALUES (?,?,'mime',NULL)`), id, []byte(fmt.Sprintf("Message-ID: <legacy-%d@example.test>\r\nIn-Reply-To: <legacy-root@example.test>\r\n\r\n[QA]", id)))
			r.NoError(err)
		}
	}
	_, err = st.DB().Exec(st.Rebind(`INSERT INTO email_linkage_checkpoints(source_id,extraction_version,last_message_id) VALUES (?,?,100)`), source.ID, mime.EmailLinkageVersion)
	r.NoError(err)
	before, err := st.DerivedDataRevision()
	r.NoError(err)
	for i := range 4 {
		result, err := st.BackfillEmailLinkage(ctx, source.ID, 1)
		r.NoError(err)
		a.Equal(1, result.Processed)
		a.Equal(1, result.Changed)
		a.Equal(int64(100), result.LastMessageID)
		a.Equal(i == 3, result.Done)
		facts, found, err := st.EmailLinkageEvidence(ctx, int64(i-2))
		r.NoError(err)
		r.True(found)
		if i == 3 {
			a.Equal("missing_raw", facts.Outcome)
		} else {
			a.Equal("complete", facts.Outcome)
		}
		seed, err := st.EmailWorkSeedForMessage(ctx, int64(i-2))
		r.NoError(err)
		a.Equal(int64(i-2), seed.AnchorMessageID)
	}
	_, found, err := st.EmailLinkageEvidence(ctx, 2)
	r.NoError(err)
	a.False(found, "non-email is excluded")
	again, err := st.BackfillEmailLinkage(ctx, source.ID, 1)
	r.NoError(err)
	a.True(again.Done)
	a.Zero(again.Processed)
	a.Zero(again.Changed)
	after, err := st.DerivedDataRevision()
	r.NoError(err)
	a.Equal(before+4, after)
}
