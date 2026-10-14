package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestSavedViewRejectsUnexecutableFiltersBeforeWrite(t *testing.T) {
	ctx := context.Background()
	good := store.SavedViewInput{Name: "[QA] Inbox", SchemaVersion: 1, CanonicalState: json.RawMessage(`{"filters":[{"field":"participant_id","operator":"eq","values":["1"]}]}`)}
	for _, state := range []string{
		`{"filters":[{"field":"unknown","operator":"eq","values":["1"]}]}`,
		`{"filters":[{"field":"participant","operator":"eq","values":["user@example.com"]}]}`,
		`{"filters":[{"field":"participant_id","operator":"eq","values":["0"]}]}`,
		`{"filters":[{"field":"source","operator":"eq","values":["9223372036854775808"]}]}`,
		`{"filters":[{"field":"source","operator":"not","values":["1"]}]}`,
		`{"filters":[{"field":"source","operator":"eq","values":[]}]}`,
		`{"filters":[{"field":"before","operator":"eq","values":["yesterday"]}]}`,
		`{"filters":[{"field":"deletion","operator":"eq","values":["maybe"]}]}`,
	} {
		t.Run(state, func(t *testing.T) {
			st := testutil.NewTestStore(t)
			existing, err := st.CreateSavedView(ctx, good)
			require.NoError(t, err)
			bad := good
			bad.Name = "[QA] Invalid"
			bad.CanonicalState = json.RawMessage(state)
			_, err = st.CreateSavedView(ctx, bad)
			assert.ErrorIs(t, err, store.ErrSavedViewInvalidState)
			_, err = st.UpdateSavedView(ctx, existing.ID, existing.Revision, bad)
			assert.ErrorIs(t, err, store.ErrSavedViewInvalidState)
			got, err := st.GetSavedView(ctx, existing.ID)
			require.NoError(t, err)
			assert.Equal(t, existing, got)
			views, err := st.ListSavedViews(ctx)
			require.NoError(t, err)
			require.Len(t, views, 1)
		})
	}
}
