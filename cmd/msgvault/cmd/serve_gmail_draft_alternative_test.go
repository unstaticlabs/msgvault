package cmd

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/gmail"
	msgmime "go.kenn.io/msgvault/internal/mime"
)

func syntheticExternalAlternativeDraft() []byte {
	return []byte("From: owner@example.test\r\n" +
		"To: recipient@example.test\r\n" +
		"Cc: copy@example.test\r\n" +
		"Bcc: hidden@example.test\r\n" +
		"Reply-To: replies@example.test\r\n" +
		"Subject: Synthetic draft\r\n" +
		"Message-ID: <external-client@example.test>\r\n" +
		"In-Reply-To: <parent@example.test>\r\n" +
		"References: <root@example.test> <parent@example.test>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=synthetic-alternative\r\n\r\n" +
		"--synthetic-alternative\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		"same draft text\r\n" +
		"--synthetic-alternative\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><body><div>same draft text</div></body></html>\r\n" +
		"--synthetic-alternative--\r\n")
}

// A client can replace a plain draft with an equivalent alternative without
// changing its visible text. This reproduces the adoption/retry sequence using
// the real lifecycle and MIME builder, with a disposable provider fixture.
func TestGmailDraftExternalAlternativeAdoptionReproducesFormatRefusal(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	fixture := newGmailDraftTestFixture(t)
	draft := fixture.seedDraft(t)
	fixture.client.getDraft = &gmail.Draft{ID: "gmail-draft-managed", Message: gmail.RawMessage{
		ID: "gmail-message-client-replacement", ThreadID: "gmail-thread-1", Raw: syntheticExternalAlternativeDraft(),
	}}
	_, err := fixture.lifecycle(t, api.CLIRunDraftEditCommand, draft, "new draft text")
	require.ErrorContains(err, "changed_externally")
	assert.Zero(fixture.client.updateCalls)
	adopted, err := fixture.store.GetGmailDraftContext(t.Context(), draft.DraftID)
	require.NoError(err)
	require.Equal(int64(2), adopted.Revision)
	require.Nil(adopted.Pending)
	assert.Equal("gmail-message-client-replacement", adopted.CurrentReceipt.GmailMessageID)

	_, err = fixture.lifecycle(t, api.CLIRunDraftEditCommand, adopted, "new draft text")
	require.ErrorContains(err, "invalid_draft")
	coded, ok := errors.AsType[*api.CLIRunCodedError](err)
	require.True(ok)
	assert.ErrorContains(coded.Err, "plain-text message")
	assert.Zero(fixture.client.updateCalls)
	after, err := fixture.store.GetGmailDraftContext(t.Context(), draft.DraftID)
	require.NoError(err)
	assert.Equal(adopted.Revision, after.Revision)
	assert.Nil(after.Pending)
	_, raw, err := fixture.store.GetMessageBodyAndRawContext(t.Context(), after.CurrentMessageID)
	require.NoError(err)
	parsed, err := msgmime.Parse(raw)
	require.NoError(err)
	assert.Contains(parsed.BodyText, "same draft text")
	assert.Contains(parsed.BodyHTML, "same draft text")
	assert.NotContains(parsed.BodyText, "new draft text")
	assert.NotContains(parsed.BodyHTML, "new draft text")
}
