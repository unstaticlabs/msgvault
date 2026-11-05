package deletion

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeleteOneRejectsUnrecognisedMethod pins the fail-closed branch in
// deleteOne. Before it existed, every method that was not exactly MethodTrash
// fell through to DeleteMessage, so a manifest carrying an unfamiliar method --
// hand-edited, or written by a binary that knows an operation this one does not
// -- would permanently destroy mail instead of being refused.
func TestDeleteOneRejectsUnrecognisedMethod(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	tc := NewTestContext(t)

	result, err := tc.Exec.deleteOne(context.Background(), 1, "msg-1", Method("archive"))

	require.Error(err)
	assert.Equal(resultFatal, result, "an unknown method must halt execution")
	assert.Contains(err.Error(), "archive")
	assert.Empty(tc.MockAPI.DeleteCalls, "no message may be permanently deleted")
	assert.Empty(tc.MockAPI.TrashCalls, "no message may be trashed")
}

// TestDeleteOneAcceptsKnownMethods keeps the guard honest: the two real methods
// must still reach their respective provider calls.
func TestDeleteOneAcceptsKnownMethods(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	tc := NewTestContext(t)
	ctx := context.Background()

	result, err := tc.Exec.deleteOne(ctx, 1, "msg-trash", MethodTrash)
	require.NoError(err)
	assert.Equal(resultSuccess, result)

	result, err = tc.Exec.deleteOne(ctx, 1, "msg-delete", MethodDelete)
	require.NoError(err)
	assert.Equal(resultSuccess, result)

	assert.Equal([]string{"msg-trash"}, tc.MockAPI.TrashCalls)
	assert.Equal([]string{"msg-delete"}, tc.MockAPI.DeleteCalls)
}

func TestExecuteRejectsUnknownMethodBeforeClaim(t *testing.T) {
	for _, method := range []Method{"", "purge-v9", "TRASH", " trash "} {
		t.Run(fmt.Sprintf("%q", method), func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)

			tc := NewTestContext(t)
			tc.SeedMessages([]string{"msg-1"})
			manifest := tc.CreateManifest("unknown-method", []string{"msg-1"})
			before, status, err := tc.Mgr.GetManifestWithStatus(manifest.ID)
			require.NoError(err)
			require.Equal(StatusPending, status)
			require.Nil(before.Execution)

			err = tc.Exec.Execute(t.Context(), manifest.ID, &ExecuteOptions{Method: method, BatchSize: 100})

			require.ErrorContains(err, fmt.Sprintf("unsupported deletion method %q", method))
			assert.Empty(tc.MockAPI.DeleteCalls)
			assert.Empty(tc.MockAPI.TrashCalls)
			assert.Empty(tc.MockAPI.BatchDeleteCalls)
			assert.Zero(tc.CountDeleted())
			after, status, err := tc.Mgr.GetManifestWithStatus(manifest.ID)
			require.NoError(err)
			assert.Equal(StatusPending, status)
			assert.Nil(after.Execution)
			assert.Equal(before, after, "rejection must leave the pending manifest unchanged")
		})
	}
}
