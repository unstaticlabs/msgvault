package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pgEmailWorkFixture(t *testing.T) (*Store, int64, int64, *Message) {
	t.Helper()
	st := newPGStoreInternal(t, skipUnlessPostgresInternal(t))
	source, err := st.GetOrCreateSource("gmail", "qa-lock@example.test")
	require.NoError(t, err)
	conversation, err := st.EnsureConversation(source.ID, "synthetic-lock-thread", "[QA]")
	require.NoError(t, err)
	message := &Message{SourceID: source.ID, ConversationID: conversation, SourceMessageID: "synthetic-original", MessageType: "email"}
	id, err := st.PersistMessage(&MessagePersistData{Message: message, RawMIME: []byte("Message-ID: <original@example.test>\r\n\r\n[QA] original")})
	require.NoError(t, err)
	return st, source.ID, id, message
}

func pgWorkWait(t *testing.T, st *Store, query string, args ...any) {
	t.Helper()
	require.Eventually(t, func() bool {
		var ready bool
		err := st.DB().QueryRow(st.Rebind(query), args...).Scan(&ready)
		return err == nil && ready
	}, 20*time.Second, 10*time.Millisecond, "native PostgreSQL blocking state did not become observable")
}

func pgWorkAwait(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(30 * time.Second):
		require.FailNow(t, "native PostgreSQL operation did not finish")
		return nil
	}
}

// Real maintenance must wait behind each writer's early lock. Attribution
// writers hold the identity row before touching message tables; the other
// writers hold an early table lock. Every blocker is a real PostgreSQL lock.
func TestPostgreSQLEmailWorkWritersSerializeSourceRemoval(t *testing.T) {
	for _, kind := range []string{"backfill", "raw", "create", "undo"} {
		t.Run(kind, func(t *testing.T) {
			r := require.New(t)
			a := assert.New(t)
			st, source, id, _ := pgEmailWorkFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
			defer cancel()
			seed, err := st.EmailWorkSeedForMessage(ctx, id)
			r.NoError(err)
			input := EmailWorkActionInput{UID: strings.Repeat("a", 32), Kind: "handled", ActorKind: "user", ActorUserID: 1, Targets: []EmailWorkActionTarget{{UID: seed.UID, SourceID: source}}}
			input.Snapshot, err = st.EmailWorkRecoverySnapshot(ctx)
			r.NoError(err)
			if kind == "undo" {
				_, err = st.CreateEmailWorkAction(ctx, input)
				r.NoError(err)
				input.Snapshot, err = st.EmailWorkRecoverySnapshot(ctx)
				r.NoError(err)
			}
			if kind == "backfill" {
				_, err = st.DB().Exec(`DELETE FROM email_linkage_facts`)
				r.NoError(err)
				_, err = st.DB().Exec(`DELETE FROM email_work_seeds`)
				r.NoError(err)
			}
			blocker, err := st.DB().BeginTx(ctx, nil)
			r.NoError(err)
			defer func() { cancel(); _ = blocker.Rollback() }()
			var blockerPID int
			r.NoError(blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID))
			switch kind {
			case "backfill":
				_, err = blocker.ExecContext(ctx, `LOCK TABLE email_linkage_checkpoints IN ACCESS EXCLUSIVE MODE`)
			case "raw":
				_, err = blocker.ExecContext(ctx, `SELECT message_id FROM message_raw WHERE message_id=$1 FOR UPDATE`, id)
			default:
				_, err = st.DB().Exec(`INSERT INTO archive_metadata(key,value) VALUES ('email_work_curation_revision','0') ON CONFLICT DO NOTHING`)
				r.NoError(err)
				_, err = blocker.ExecContext(ctx, `SELECT value FROM archive_metadata WHERE key='email_work_curation_revision' FOR UPDATE`)
			}
			r.NoError(err)
			writer := make(chan error, 1)
			go func() {
				var err error
				switch kind {
				case "backfill":
					_, err = st.BackfillEmailLinkage(ctx, source, 10)
				case "raw":
					err = st.UpsertMessageRaw(id, []byte("Message-ID: <original@example.test>\r\n\r\n[QA] changed"))
				case "create":
					_, err = st.CreateEmailWorkAction(ctx, input)
				case "undo":
					_, err = st.UndoEmailWorkAction(ctx, input.UID, input.Snapshot, 1)
				}
				writer <- err
			}()
			pgWorkWait(t, st, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE ?=ANY(pg_blocking_pids(pid)))`, blockerPID)
			maintenance := make(chan error, 1)
			go func() { _, _, err := st.RemoveSourceSerialized(ctx, source); maintenance <- err }()
			if kind == "raw" {
				// MIME attribution holds the shared identity row first, so
				// maintenance waits there before reaching its table locks.
				// Prove the full maintenance -> writer -> fixture chain.
				pgWorkWait(t, st, `SELECT EXISTS(
					SELECT 1 FROM pg_stat_activity writer
					JOIN pg_stat_activity maintenance ON writer.pid=ANY(pg_blocking_pids(maintenance.pid))
					WHERE ?=ANY(pg_blocking_pids(writer.pid))
				)`, blockerPID)
			} else {
				relation := "messages"
				if kind == "backfill" {
					relation = "sources"
				}
				pgWorkWait(t, st, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='%s'::regclass AND mode='ExclusiveLock' AND NOT granted) AND EXISTS(SELECT 1 FROM pg_locks WHERE relation='%s'::regclass AND mode='RowExclusiveLock' AND granted)`, relation, relation))
			}
			r.NoError(blocker.Rollback())
			r.NoError(pgWorkAwait(t, writer))
			r.NoError(pgWorkAwait(t, maintenance))
			var count int
			for _, table := range []string{"messages", "email_linkage_facts", "email_linkage_checkpoints"} {
				r.NoError(st.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count))
				a.Zero(count, table)
			}
			r.NoError(st.DB().QueryRow(`SELECT COUNT(*) FROM email_work_seeds WHERE anchor_message_id=$1 AND live_message_id IS NULL`, id).Scan(&count))
			a.Equal(1, count)
			if kind == "create" || kind == "undo" {
				action, err := st.EmailWorkAction(ctx, input.UID)
				r.NoError(err)
				a.Equal(kind == "create", action.Active)
				a.Equal([]string{seed.UID}, action.Targets)
			}
		})
	}
}

// The real scoped transaction fences sync_runs before its callback. MIME
// attribution holds identity before that fence, so maintenance waits on identity;
// backfill instead makes maintenance wait on the fenced table. Neither path may
// try to acquire identity after the fence.
func TestPostgreSQLEmailWorkScopedRawMaintenanceLiveness(t *testing.T) {
	for _, kind := range []string{"raw", "backfill"} {
		t.Run(kind, func(t *testing.T) {
			r := require.New(t)
			st, source, id, _ := pgEmailWorkFixture(t)
			if kind == "backfill" {
				_, err := st.DB().Exec(`DELETE FROM email_linkage_facts`)
				r.NoError(err)
				_, err = st.DB().Exec(`DELETE FROM email_work_seeds`)
				r.NoError(err)
			}
			run, err := st.StartSync(source, "full")
			r.NoError(err)
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
			defer cancel()
			blocker, err := st.DB().BeginTx(ctx, nil)
			r.NoError(err)
			defer func() { cancel(); _ = blocker.Rollback() }()
			_, err = blocker.ExecContext(ctx, `SELECT id FROM sync_runs WHERE id=$1 FOR UPDATE`, run)
			r.NoError(err)
			var pid int
			r.NoError(blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
			writer := make(chan error, 1)
			go func() {
				if kind == "raw" {
					writer <- st.ScopedToSync(source, run).UpsertMessageRaw(id, []byte("Message-ID: <original@example.test>\r\n\r\n[QA] scoped"))
				} else {
					_, err := st.ScopedToSync(source, run).BackfillEmailLinkage(ctx, source, 10)
					writer <- err
				}
			}()
			pgWorkWait(t, st, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE ?=ANY(pg_blocking_pids(pid)) AND query LIKE '%UPDATE sync_runs%')`, pid)
			maintenance := make(chan error, 1)
			go func() {
				conn, err := st.DB().Conn(ctx)
				if err != nil {
					maintenance <- err
					return
				}
				defer func() { _ = conn.Close() }()
				err = st.dialect.BeginExclusive(ctx, conn)
				if err == nil {
					_, err = conn.ExecContext(ctx, "COMMIT")
				} else {
					_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
				}
				maintenance <- err
			}()
			if kind == "raw" {
				pgWorkWait(t, st, `SELECT EXISTS(
					SELECT 1 FROM pg_stat_activity writer
					JOIN pg_stat_activity maintenance ON writer.pid=ANY(pg_blocking_pids(maintenance.pid))
					WHERE ?=ANY(pg_blocking_pids(writer.pid))
				)`, pid)
			} else {
				pgWorkWait(t, st, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='sync_runs'::regclass AND mode='ExclusiveLock' AND NOT granted)`)
			}
			r.NoError(blocker.Rollback())
			r.NoError(pgWorkAwait(t, writer))
			r.NoError(pgWorkAwait(t, maintenance))
			raw, err := st.GetMessageRaw(id)
			r.NoError(err)
			if kind == "raw" {
				assert.Contains(t, string(raw), "[QA] scoped")
			} else {
				seed, err := st.EmailWorkSeedForMessage(ctx, id)
				r.NoError(err)
				assert.Equal(t, id, seed.AnchorMessageID)
			}
		})
	}
}

// A legacy message has raw but no seed. The ordinary persister holds message
// before raw; backfill must do the same to prevent a message/raw FK cycle.
func TestPostgreSQLEmailWorkBackfillMissingSeedPersistOrdering(t *testing.T) {
	r := require.New(t)
	st, source, id, message := pgEmailWorkFixture(t)
	_, err := st.DB().Exec(`DELETE FROM email_linkage_facts`)
	r.NoError(err)
	_, err = st.DB().Exec(`DELETE FROM email_work_seeds`)
	r.NoError(err)
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	blocker, err := st.DB().BeginTx(ctx, nil)
	r.NoError(err)
	defer func() { cancel(); _ = blocker.Rollback() }()
	_, err = blocker.ExecContext(ctx, `SELECT message_id FROM message_raw WHERE message_id=$1 FOR UPDATE`, id)
	r.NoError(err)
	var pid int
	r.NoError(blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	backfill := make(chan error, 1)
	go func() { _, err := st.BackfillEmailLinkage(ctx, source, 10); backfill <- err }()
	pgWorkWait(t, st, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE ?=ANY(pg_blocking_pids(pid)) AND query LIKE '%SELECT CASE WHEN length(raw_data)%')`, pid)
	persister := make(chan error, 1)
	go func() {
		_, err := st.PersistMessage(&MessagePersistData{Message: message, RawMIME: []byte("Message-ID: <original@example.test>\r\n\r\n[QA] persisted")})
		persister <- err
	}()
	pgWorkWait(t, st, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity writer JOIN pg_stat_activity backfill ON backfill.pid=ANY(pg_blocking_pids(writer.pid)) WHERE backfill.query LIKE '%SELECT CASE WHEN length(raw_data)%')`)
	r.NoError(blocker.Rollback())
	r.NoError(pgWorkAwait(t, backfill))
	r.NoError(pgWorkAwait(t, persister))
	facts, found, err := st.EmailLinkageEvidence(ctx, id)
	r.NoError(err)
	r.True(found)
	assert.Equal(t, "original@example.test", facts.MessageID)
	seed, err := st.EmailWorkSeedForMessage(ctx, id)
	r.NoError(err)
	assert.Equal(t, id, seed.AnchorMessageID)
	raw, err := st.GetMessageRaw(id)
	r.NoError(err)
	assert.Contains(t, string(raw), "[QA] persisted")
}

func TestPostgreSQLEmailWorkBackfillCancellationRollsBack(t *testing.T) {
	r := require.New(t)
	st, source, id, _ := pgEmailWorkFixture(t)
	_, err := st.DB().Exec(`DELETE FROM email_linkage_facts`)
	r.NoError(err)
	_, err = st.DB().Exec(`DELETE FROM email_work_seeds`)
	r.NoError(err)
	blocker, err := st.DB().BeginTx(t.Context(), nil)
	r.NoError(err)
	defer func() { _ = blocker.Rollback() }()
	_, err = blocker.Exec(`SELECT id FROM messages WHERE id=$1 FOR UPDATE`, id)
	r.NoError(err)
	var pid int
	r.NoError(blocker.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := st.BackfillEmailLinkage(ctx, source, 10); done <- err }()
	pgWorkWait(t, st, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE ?=ANY(pg_blocking_pids(pid)))`, pid)
	cancel()
	err = pgWorkAwait(t, done)
	r.True(errors.Is(err, context.Canceled) || errors.Is(err, sql.ErrTxDone), err)
	r.NoError(blocker.Rollback())
	for _, table := range []string{"email_linkage_checkpoints", "email_linkage_facts", "email_work_seeds"} {
		var count int
		r.NoError(st.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count))
		assert.Zero(t, count, table)
	}
	result, err := st.BackfillEmailLinkage(t.Context(), source, 10)
	r.NoError(err)
	assert.Equal(t, 1, result.Processed)
	assert.True(t, result.Done)
}

// Curation retains fixed targets while an ordinary message-first persister is
// blocked on actual raw bytes. A serialization/stale result requires retry,
// never a partial assertion or a deadlock.
func TestPostgreSQLEmailWorkCurationPersistOrdering(t *testing.T) {
	for _, kind := range []string{"create", "undo"} {
		t.Run(kind, func(t *testing.T) {
			r := require.New(t)
			a := assert.New(t)
			st, source, id, message := pgEmailWorkFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
			defer cancel()
			seed, err := st.EmailWorkSeedForMessage(ctx, id)
			r.NoError(err)
			input := EmailWorkActionInput{UID: strings.Repeat("b", 32), Kind: "handled", ActorKind: "user", ActorUserID: 1, Targets: []EmailWorkActionTarget{{UID: seed.UID, SourceID: source}}}
			input.Snapshot, err = st.EmailWorkRecoverySnapshot(ctx)
			r.NoError(err)
			if kind == "undo" {
				_, err = st.CreateEmailWorkAction(ctx, input)
				r.NoError(err)
				input.Snapshot, err = st.EmailWorkRecoverySnapshot(ctx)
				r.NoError(err)
			}
			blocker, err := st.DB().BeginTx(ctx, nil)
			r.NoError(err)
			defer func() { cancel(); _ = blocker.Rollback() }()
			_, err = blocker.ExecContext(ctx, `SELECT message_id FROM message_raw WHERE message_id=$1 FOR UPDATE`, id)
			r.NoError(err)
			var pid int
			r.NoError(blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
			persister := make(chan error, 1)
			go func() {
				_, err := st.PersistMessage(&MessagePersistData{Message: message, RawMIME: []byte("Message-ID: <original@example.test>\r\n\r\n[QA] persisted")})
				persister <- err
			}()
			pgWorkWait(t, st, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE ?=ANY(pg_blocking_pids(pid)) AND query LIKE '%INSERT INTO message_raw%')`, pid)
			curation := make(chan error, 1)
			go func() {
				var err error
				if kind == "create" {
					_, err = st.CreateEmailWorkAction(ctx, input)
				} else {
					_, err = st.UndoEmailWorkAction(ctx, input.UID, input.Snapshot, 1)
				}
				curation <- err
			}()
			if kind == "create" {
				pgWorkWait(t, st, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity c JOIN pg_stat_activity p ON p.pid=ANY(pg_blocking_pids(c.pid)) WHERE p.query LIKE '%INSERT INTO message_raw%' AND c.query LIKE '%FOR UPDATE OF m%')`)
			}
			r.NoError(blocker.Rollback())
			r.NoError(pgWorkAwait(t, persister))
			err = pgWorkAwait(t, curation)
			r.True(err == nil || errors.Is(err, ErrEmailWorkActionChanged) || isPgError(err, "40001"), "unexpected native curation result: %v", err)
			if err != nil {
				var count int
				r.NoError(st.DB().QueryRow(`SELECT COUNT(*) FROM email_work_actions WHERE uid=$1`, input.UID).Scan(&count))
				if kind == "create" {
					a.Zero(count, "failed creation is atomic")
				} else {
					a.Equal(1, count)
					old, err := st.EmailWorkAction(ctx, input.UID)
					r.NoError(err)
					a.True(old.Active, "failed undo remains active")
				}
				input.Snapshot, err = st.EmailWorkRecoverySnapshot(ctx)
				r.NoError(err)
				if kind == "create" {
					_, err = st.CreateEmailWorkAction(ctx, input)
				} else {
					_, err = st.UndoEmailWorkAction(ctx, input.UID, input.Snapshot, 1)
				}
				r.NoError(err)
			}
			action, err := st.EmailWorkAction(ctx, input.UID)
			r.NoError(err)
			a.Equal(kind == "create", action.Active)
			a.Equal([]string{seed.UID}, action.Targets)
			fresh, err := st.EmailWorkSeedForMessage(ctx, id)
			r.NoError(err)
			a.Equal(seed, fresh)
		})
	}
}

// A seed's immutable opaque identity is read, not updated, by curation. A
// concurrent non-key seed update may coexist with the assertion's FK key-share;
// locking every JOIN row FOR UPDATE unnecessarily blocks this native operation.
func TestPostgreSQLEmailWorkCreateLocksOnlyMessage(t *testing.T) {
	r := require.New(t)
	st, source, id, _ := pgEmailWorkFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	seed, err := st.EmailWorkSeedForMessage(ctx, id)
	r.NoError(err)
	blocker, err := st.DB().BeginTx(ctx, nil)
	r.NoError(err)
	defer func() { _ = blocker.Rollback() }()
	_, err = blocker.ExecContext(ctx, `SELECT uid FROM email_work_seeds WHERE uid=$1 FOR NO KEY UPDATE`, seed.UID)
	r.NoError(err)
	input := EmailWorkActionInput{UID: strings.Repeat("c", 32), Kind: "handled", ActorKind: "user", ActorUserID: 1, Targets: []EmailWorkActionTarget{{UID: seed.UID, SourceID: source}}}
	input.Snapshot, err = st.EmailWorkRecoverySnapshot(ctx)
	r.NoError(err)
	created, err := st.CreateEmailWorkAction(ctx, input)
	r.NoError(err, "native message-only curation must complete while a compatible seed lock remains held")
	assert.True(t, created.Active)
	assert.Equal(t, []string{seed.UID}, created.Targets)
	r.NoError(blocker.Rollback())
}
