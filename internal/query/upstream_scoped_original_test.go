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
	assertions := assert.New(t)
	requirements := require.New(t)
	f := storetest.New(t)
	other, err := f.Store.GetOrCreateSource("gmail", "excluded@example.test")
	requirements.NoError(err)
	otherConv, err := f.Store.EnsureConversation(other.ID, "excluded-thread", "[QA]")
	requirements.NoError(err)
	own := f.NewMessage().WithSourceMessageID("same-provider-id").Create(t, f.Store)
	hidden := storetest.NewMessage(other.ID, otherConv).WithSourceMessageID("same-provider-id").Create(t, f.Store)
	requirements.NoError(f.Store.UpsertMessageRaw(own, []byte("Subject: [QA]\r\n\r\nPermitted bytes.")))
	requirements.NoError(f.Store.UpsertMessageRaw(hidden, []byte("Subject: [QA]\r\n\r\nExcluded bytes.")))
	inner := originalEngine(f)
	scoped := query.NewScopedEngine(inner, []int64{f.Source.ID})
	reader, ok := scoped.(query.OriginalMessageReader)
	requirements.True(ok)
	original, err := reader.ReadOriginalMessage(t.Context(), query.MessageRef{SourceMessageID: "same-provider-id"}, 1024)
	requirements.NoError(err)
	assertions.Equal(own, original.MessageID)
	assertions.Contains(string(original.MIME), "Permitted bytes.")
	_, err = reader.ReadOriginalMessage(t.Context(), query.MessageRef{ID: hidden}, 1024)
	requirements.ErrorIs(err, store.ErrMessageNotFound)
	_, err = reader.ReadOriginalMessage(t.Context(), query.MessageRef{ID: own}, 4)
	requirements.ErrorIs(err, query.ErrOriginalMessageTooLarge)
	threadQuery := query.ThreadQuery{MessageRef: query.MessageRef{ID: own}, All: true} //nolint:modernize // Go 1.27.2 still requires the embedded field's literal type.
	page, err := reader.ListThread(t.Context(), threadQuery)
	requirements.NoError(err)
	requirements.Len(page.Messages, 1)
	assertions.Equal(own, page.Messages[0].ID)
	empty, ok := query.NewScopedEngine(inner, []int64{}).(query.OriginalMessageReader)
	requirements.True(ok)
	_, err = empty.ReadOriginalMessage(t.Context(), query.MessageRef{ID: own}, 1024)
	requirements.ErrorIs(err, store.ErrMessageNotFound)
}
