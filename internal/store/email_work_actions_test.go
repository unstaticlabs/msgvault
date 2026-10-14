package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func localWorkActionFixture(t *testing.T) (*store.Store, store.EmailWorkActionInput, []int64) {
	t.Helper()
	requirements := require.New(t)
	st := testutil.NewTestStore(t)
	input := store.EmailWorkActionInput{UID: strings.Repeat("a", 32), Kind: "link", ActorKind: "user", ActorUserID: 7}
	var ids []int64
	for _, key := range []string{"original", "inline-forward"} {
		source, err := st.GetOrCreateSource("gmail", key+"@example.test")
		requirements.NoError(err)
		id, err := st.UpsertMessage(linkageMessage(t, st, source.ID, key))
		requirements.NoError(err)
		seed, err := st.EmailWorkSeedForMessage(context.Background(), id)
		requirements.NoError(err)
		input.Targets = append(input.Targets, store.EmailWorkActionTarget{UID: seed.UID, SourceID: source.ID})
		ids = append(ids, id)
	}
	var err error
	input.Snapshot, err = st.EmailWorkRecoverySnapshot(context.Background())
	requirements.NoError(err)
	return st, input, ids
}

func TestEmailWorkLocalActionNativeCreateRetryUndo(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	st, input, _ := localWorkActionFixture(t)
	ctx := context.Background()
	before, err := st.DerivedDataRevision()
	requirements.NoError(err)
	created, err := st.CreateEmailWorkAction(ctx, input)
	requirements.NoError(err)
	assertions.True(created.Active)
	assertions.Equal(int64(1), created.Revision)
	assertions.Len(created.Targets, 2)
	assertions.Equal("user", created.ActorKind)
	assertions.Equal(int64(7), created.ActorUserID)
	after, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(before+1, after)
	retry, err := st.CreateEmailWorkAction(ctx, input)
	requirements.NoError(err)
	assertions.Equal(created, retry)
	revision, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(after, revision, "identical retry must not invalidate publication")
	snapshot, err := st.EmailWorkRecoverySnapshot(ctx)
	requirements.NoError(err)
	undone, err := st.UndoEmailWorkAction(ctx, created.UID, snapshot, created.Revision)
	requirements.NoError(err)
	assertions.False(undone.Active)
	assertions.Equal(int64(2), undone.Revision)
	revision, err = st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(after+1, revision)
	repeated, err := st.UndoEmailWorkAction(ctx, created.UID, snapshot, created.Revision)
	requirements.NoError(err)
	assertions.Equal(undone, repeated)
	repeated, err = st.CreateEmailWorkAction(ctx, input)
	requirements.NoError(err)
	assertions.Equal(undone, repeated, "creation retries cannot resurrect undone assertions")
	revision, err = st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(after+1, revision)
}

func TestEmailWorkLocalActionStalePreviewAndInvalidTargets(t *testing.T) {
	for _, mutation := range []string{"new-mail", "changed-evidence", "removed-anchor", "wrong-source", "duplicate", "changed-actor", "changed-targets"} {
		t.Run(mutation, func(t *testing.T) {
			requirements := require.New(t)
			assertions := assert.New(t)
			st, input, ids := localWorkActionFixture(t)
			ctx := context.Background()
			switch mutation {
			case "new-mail":
				_, err := st.UpsertMessage(linkageMessage(t, st, input.Targets[0].SourceID, "later"))
				requirements.NoError(err)
			case "changed-evidence":
				requirements.NoError(st.UpsertMessageRaw(ids[0], []byte("Message-ID: <changed@example.test>\r\n\r\n[QA]")))
			case "removed-anchor":
				_, err := st.DB().Exec(st.Rebind(`DELETE FROM messages WHERE id=?`), ids[0])
				requirements.NoError(err)
				// Even a freshly captured snapshot cannot make a tombstoned target valid.
				input.Snapshot, err = st.EmailWorkRecoverySnapshot(ctx)
				requirements.NoError(err)
			case "wrong-source":
				input.Targets[0].SourceID = input.Targets[1].SourceID
			case "duplicate":
				input.Targets[1] = input.Targets[0]
			case "changed-actor", "changed-targets":
				_, err := st.CreateEmailWorkAction(ctx, input)
				requirements.NoError(err)
				if mutation == "changed-actor" {
					input.ActorUserID++
				} else {
					input.Kind = "handled"
					input.Targets = input.Targets[:1]
				}
			}
			_, err := st.CreateEmailWorkAction(ctx, input)
			requirements.Error(err)
			assertions.True(errors.Is(err, store.ErrEmailWorkActionChanged) || errors.Is(err, store.ErrEmailWorkActionInvalid), err)
			var count int
			requirements.NoError(st.DB().QueryRow(`SELECT COUNT(*) FROM email_work_actions`).Scan(&count))
			if mutation == "changed-actor" || mutation == "changed-targets" {
				assertions.Equal(1, count)
			} else {
				assertions.Zero(count)
			}
		})
	}
}

func TestEmailWorkLocalUndoPreservesOtherAssertions(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	st, input, _ := localWorkActionFixture(t)
	ctx := context.Background()
	first, err := st.CreateEmailWorkAction(ctx, input)
	requirements.NoError(err)
	staleSnapshot, err := st.EmailWorkRecoverySnapshot(ctx)
	requirements.NoError(err)
	input.UID = strings.Repeat("b", 32)
	input.Kind = "handled"
	input.Targets = input.Targets[:1]
	input.Snapshot = staleSnapshot
	second, err := st.CreateEmailWorkAction(ctx, input)
	requirements.NoError(err)
	_, err = st.UndoEmailWorkAction(ctx, first.UID, staleSnapshot, first.Revision)
	requirements.ErrorIs(err, store.ErrEmailWorkActionChanged)
	snapshot, err := st.EmailWorkRecoverySnapshot(ctx)
	requirements.NoError(err)
	_, err = st.UndoEmailWorkAction(ctx, first.UID, snapshot, first.Revision)
	requirements.NoError(err)
	current, err := st.EmailWorkAction(ctx, second.UID)
	requirements.NoError(err)
	assertions.Equal(second, current)
	assertions.True(current.Active)
	// Only opaque seeds and local provenance are retained after a hard delete.
	_, err = st.DB().Exec(`DELETE FROM messages`)
	requirements.NoError(err)
	current, err = st.EmailWorkAction(ctx, second.UID)
	requirements.NoError(err)
	assertions.Equal(second, current)
	var live sql.NullInt64
	requirements.NoError(st.DB().QueryRow(st.Rebind(`SELECT live_message_id FROM email_work_seeds WHERE uid=?`), second.Targets[0]).Scan(&live))
	assertions.False(live.Valid)
}

func TestEmailWorkLocalAssertionsRespectScopeAndNewInbound(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	members := []store.EmailWorkMember{{MessageID: 1, SourceID: 1}, {MessageID: 2, SourceID: 2}, {MessageID: 3, SourceID: 3}}
	components, err := store.PartitionEmailWorkMembersWithLocalLinks(members, [][]int64{{1, 2}, {2, 3}})
	requirements.NoError(err)
	assertions.Equal([][]int64{{1, 2, 3}}, components)
	scoped, err := store.PartitionEmailWorkMembersWithLocalLinks([]store.EmailWorkMember{members[0], members[2]}, [][]int64{{1, 2}, {2, 3}})
	requirements.NoError(err)
	assertions.Equal([][]int64{{1}, {3}}, scoped, "excluded B cannot be a local bridge between A/C")
	// Local confirmation can recover an unknown original without pretending a
	// missing header is automatic proof. A later unknown received event reopens it.
	activities := []store.EmailWorkActivity{{EmailWorkMember: members[0], LocalHandled: true}}
	handled := store.EvaluateEmailWorkHandling(activities)
	assertions.Equal("handled", handled.State)
	assertions.Empty(handled.OutboundEvidenceIDs, "local assertion must never masquerade as a sent reply")
	assertions.Contains(handled.Reasons, "local_confirmation")
	activities = append(activities, store.EmailWorkActivity{EmailWorkMember: members[2], Incoming: true})
	pending := store.EvaluateEmailWorkHandling(activities)
	assertions.Equal("actionable", pending.State)
	assertions.Equal([]int64{3}, pending.OutstandingEventIDs)
	activities[0].LocalHandled = false
	reopened := store.EvaluateEmailWorkHandling(activities[:1])
	assertions.Equal("unknown", reopened.State, "undo restores the unresolved underlying evidence")
}

func TestEmailWorkLocalActionStorageFailureRollsBackJournalAndRevision(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	st, input, _ := localWorkActionFixture(t)
	ctx := context.Background()
	before, err := st.DerivedDataRevision()
	requirements.NoError(err)
	trigger := `CREATE TRIGGER reject_local_target BEFORE INSERT ON email_work_action_targets BEGIN SELECT RAISE(ABORT,'synthetic storage failure'); END`
	if st.IsPostgreSQL() {
		trigger = `CREATE FUNCTION reject_local_target() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic storage failure'; END; $$; CREATE TRIGGER reject_local_target BEFORE INSERT ON email_work_action_targets FOR EACH ROW EXECUTE FUNCTION reject_local_target()`
	}
	_, err = st.DB().Exec(trigger)
	requirements.NoError(err)
	_, err = st.CreateEmailWorkAction(ctx, input)
	requirements.ErrorContains(err, "synthetic storage failure")
	var count int
	requirements.NoError(st.DB().QueryRow(`SELECT COUNT(*) FROM email_work_actions`).Scan(&count))
	assertions.Zero(count, "action insertion must roll back with failed target insertion")
	requirements.NoError(st.DB().QueryRow(`SELECT COUNT(*) FROM email_work_action_targets`).Scan(&count))
	assertions.Zero(count)
	after, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(before, after)
	snapshot, err := st.EmailWorkRecoverySnapshot(ctx)
	requirements.NoError(err)
	assertions.Equal(input.Snapshot, snapshot, "a failed transaction must not consume the optimistic preview")
}

func TestEmailWorkLocalRecoveryRejectsChangedSavedDefinitionInsideTransaction(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	st, input, _ := localWorkActionFixture(t)
	ctx := context.Background()
	definition := store.SavedViewInput{Name: "[QA] Related email", SchemaVersion: 1, CanonicalState: json.RawMessage(`{"filters":[{"field":"row_unit","operator":"eq","values":["email_work_item"]}]}`)}
	view, err := st.CreateSavedView(ctx, definition)
	requirements.NoError(err)
	input.ExpectedViewID = view.ID
	input.ExpectedViewRevision = view.Revision
	definition.Name = "[QA] Reviewed definition changed"
	updated, err := st.UpdateSavedView(ctx, view.ID, view.Revision, definition)
	requirements.NoError(err)
	snapshot, err := st.EmailWorkRecoverySnapshot(ctx)
	requirements.NoError(err)
	assertions.Equal(input.Snapshot, snapshot, "a saved-definition edit is a separate optimistic contract")
	_, err = st.CreateEmailWorkAction(ctx, input)
	requirements.ErrorIs(err, store.ErrSavedViewRevisionConflict)
	input.ExpectedViewRevision = updated.Revision
	action, err := st.CreateEmailWorkAction(ctx, input)
	requirements.NoError(err)
	snapshot, err = st.EmailWorkRecoverySnapshot(ctx)
	requirements.NoError(err)
	_, err = st.UndoEmailWorkAction(ctx, action.UID, snapshot, action.Revision, view.ID, view.Revision)
	requirements.ErrorIs(err, store.ErrSavedViewRevisionConflict)
	_, err = st.UndoEmailWorkAction(ctx, action.UID, snapshot, action.Revision, updated.ID, updated.Revision)
	requirements.NoError(err)
}
