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
	assertions := assert.New(t)
	requirements := require.New(t)
	ctx := context.Background()
	good := store.SavedViewInput{Name: "[QA] Inbox", SchemaVersion: 1, CanonicalState: json.RawMessage(`{"filters":[{"field":"participant_id","operator":"eq","values":["1"]}]}`)}
	for _, state := range []string{
		`{"filters":[{"field":"identity","operator":"eq","values":["1","user@example.com","recipient"]}]}`,
		`{"filters":[{"field":"source","operator":"eq","values":["2"]},{"field":"identity","operator":"eq","values":["1","user@example.com","recipient"]}]}`,
		`{"filters":[{"field":"inbox_state","operator":"eq","values":["yes"]}]}`,
		`{"filters":[{"field":"read_state","operator":"eq","values":["seen"]}]}`,
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
			requirements.NoError(err)
			bad := good
			bad.Name = "[QA] Invalid"
			bad.CanonicalState = json.RawMessage(state)
			_, err = st.CreateSavedView(ctx, bad)
			requirements.ErrorIs(err, store.ErrSavedViewInvalidState)
			_, err = st.UpdateSavedView(ctx, existing.ID, existing.Revision, bad)
			requirements.ErrorIs(err, store.ErrSavedViewInvalidState)
			got, err := st.GetSavedView(ctx, existing.ID)
			requirements.NoError(err)
			assertions.Equal(existing, got)
			views, err := st.ListSavedViews(ctx)
			requirements.NoError(err)
			requirements.Len(views, 1)
		})
	}
}

func TestSavedViewsKeepNativeIdentityAndMailingListFiltersExecutable(t *testing.T) {
	st := testutil.NewTestStore(t)
	for _, state := range []string{
		`{"filters":[{"field":"source","operator":"eq","values":["1"]},{"field":"identity","operator":"eq","values":["1","user@example.com","recipient"]},{"field":"inbox_state","operator":"eq","values":["inbox"]}]}`,
		`{"filters":[{"field":"mailing_list","operator":"eq","values":["news.example.com"]},{"field":"mailing_list","operator":"eq","values":["updates.example.com"]}]}`,
	} {
		_, err := st.CreateSavedView(context.Background(), store.SavedViewInput{Name: state, SchemaVersion: 1, CanonicalState: json.RawMessage(state)})
		require.NoError(t, err)
	}
}
