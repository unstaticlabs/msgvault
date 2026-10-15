package query_test

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
	"testing"
)

func TestScopedNativeOriginalAndThreadKeepSourceAuthority(t *testing.T) {
	f := storetest.New(t)
	other, err := f.Store.GetOrCreateSource("gmail", "excluded@example.test")
	require.NoError(t, err)
	otherConv, err := f.Store.EnsureConversation(other.ID, "excluded-thread", "[QA]")
	require.NoError(t, err)
	own := f.NewMessage().WithSourceMessageID("same-provider-id").Create(t, f.Store)
	hidden := storetest.NewMessage(other.ID, otherConv).WithSourceMessageID("same-provider-id").Create(t, f.Store)
	require.NoError(t, f.Store.UpsertMessageRaw(own, []byte("Subject: [QA]\r\n\r\nPermitted bytes.")))
	require.NoError(t, f.Store.UpsertMessageRaw(hidden, []byte("Subject: [QA]\r\n\r\nExcluded bytes.")))
	inner := originalEngine(f)
	scoped := query.NewScopedEngine(inner, []int64{f.Source.ID})
	reader, ok := scoped.(query.OriginalMessageReader)
	require.True(t, ok)
	original, err := reader.ReadOriginalMessage(t.Context(), query.MessageRef{SourceMessageID: "same-provider-id"}, 1024)
	require.NoError(t, err)
	assert.Equal(t, own, original.MessageID)
	assert.Contains(t, string(original.MIME), "Permitted bytes.")
	_, err = reader.ReadOriginalMessage(t.Context(), query.MessageRef{ID: hidden}, 1024)
	require.ErrorIs(t, err, store.ErrMessageNotFound)
	_, err = reader.ReadOriginalMessage(t.Context(), query.MessageRef{ID: own}, 4)
	require.ErrorIs(t, err, query.ErrOriginalMessageTooLarge)
	page, err := reader.ListThread(t.Context(), query.ThreadQuery{MessageRef: query.MessageRef{ID: own}, All: true})
	require.NoError(t, err)
	require.Len(t, page.Messages, 1)
	assert.Equal(t, own, page.Messages[0].ID)
	empty := query.NewScopedEngine(inner, []int64{}).(query.OriginalMessageReader)
	_, err = empty.ReadOriginalMessage(t.Context(), query.MessageRef{ID: own}, 1024)
	require.ErrorIs(t, err, store.ErrMessageNotFound)
}
