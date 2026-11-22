package cmd

import (
	"strings"
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
		"<html><body><div><b>same</b> draft <i>text</i></div></body></html>\r\n" +
		"--synthetic-alternative--\r\n")
}

// A client can replace a plain draft with an equivalent alternative without
// changing its visible text. This reproduces the adoption/retry sequence using
// the real lifecycle and MIME builder, with a disposable provider fixture.
func TestGmailDraftExternalAlternativeAdoptionAndRichEdit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	fixture := newGmailDraftTestFixture(t)
	draft := fixture.seedDraft(t)
	fixture.client.getDraft = &gmail.Draft{ID: "gmail-draft-managed", Message: gmail.RawMessage{
		ID: "gmail-message-client-replacement", ThreadID: "gmail-thread-1", Raw: syntheticExternalAlternativeDraft(),
	}}
	conflict, err := fixture.lifecycle(t, api.CLIRunDraftEditCommand, draft, "new draft text")
	require.ErrorContains(err, "changed_externally")
	require.Len(conflict, 1)
	assert.Contains(conflict[0].Data, `"revision":2`)
	assert.Contains(conflict[0].Data, `"status":"changed_externally"`)
	assert.Zero(fixture.client.updateCalls)
	adopted, err := fixture.store.GetGmailDraftContext(t.Context(), draft.DraftID)
	require.NoError(err)
	require.Equal(int64(2), adopted.Revision)
	require.Nil(adopted.Pending)
	assert.Equal("gmail-message-client-replacement", adopted.CurrentReceipt.GmailMessageID)

	_, previousRaw, err := fixture.store.GetMessageBodyAndRawContext(t.Context(), adopted.CurrentMessageID)
	require.NoError(err)
	assert.Equal(syntheticExternalAlternativeDraft(), previousRaw)
	fixture.client.updateDraft = &gmail.Draft{ID: "gmail-draft-managed", Message: gmail.RawMessage{ID: "gmail-message-resumed", ThreadID: "gmail-thread-1"}}
	events, err := fixture.lifecycle(t, api.CLIRunDraftEditCommand, adopted, "new draft text")
	require.NoError(err)
	require.Len(events, 1)
	assert.Contains(events[0].Data, `"status":"edited"`)
	assert.Equal(1, fixture.client.updateCalls)
	assert.Zero(fixture.client.deleteCalls + fixture.client.createCalls)
	after, err := fixture.store.GetGmailDraftContext(t.Context(), draft.DraftID)
	require.NoError(err)
	assert.Equal(int64(3), after.Revision)
	assert.Equal("gmail-message-resumed", after.CurrentReceipt.GmailMessageID)
	assert.Nil(after.Pending)
	_, raw, err := fixture.store.GetMessageBodyAndRawContext(t.Context(), after.CurrentMessageID)
	require.NoError(err)
	parsed, err := msgmime.Parse(raw)
	require.NoError(err)
	assert.Equal("new draft text", strings.TrimSpace(parsed.BodyText))
	assert.Contains(parsed.BodyHTML, "<b>new</b> draft <i>text</i>")
	require.Len(parsed.Bcc, 1)
	assert.Equal("hidden@example.test", parsed.Bcc[0].Email)
	require.Len(parsed.ReplyTo, 1)
	assert.Equal("replies@example.test", parsed.ReplyTo[0].Email)
}

func TestGmailDraftExplicitRichAlternativeThroughCLI(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	fixture := newGmailDraftTestFixture(t)
	draft := fixture.seedDraft(t)
	external := strings.Replace(string(syntheticExternalAlternativeDraft()), "<b>same</b> draft <i>text</i>", "une autre version", 1)
	fixture.client.getDraft = &gmail.Draft{ID: "gmail-draft-managed", Message: gmail.RawMessage{ID: "external-message", ThreadID: "gmail-thread-1", Raw: []byte(external)}}
	_, err := fixture.lifecycle(t, api.CLIRunDraftEditCommand, draft, "new text")
	require.ErrorContains(err, "changed_externally")
	fixture.client.updateDraft = &gmail.Draft{ID: "gmail-draft-managed", Message: gmail.RawMessage{ID: "saved-edited", ThreadID: "gmail-thread-1"}}
	_, err = runReviewLifecycle(t, fixture.adapter, api.CLIRunDraftEditCommand, draft.DraftID, "--revision", "2", "--body", "new text", "--body-html", "<strong>nouveau texte</strong>", "--json")
	require.NoError(err)
	result, err := fixture.store.GetGmailDraftContext(t.Context(), draft.DraftID)
	require.NoError(err)
	_, raw, err := fixture.store.GetMessageBodyAndRawContext(t.Context(), result.CurrentMessageID)
	require.NoError(err)
	message, err := msgmime.Parse(raw)
	require.NoError(err)
	assert.Equal("new text", strings.TrimSpace(message.BodyText))
	assert.Equal("<strong>nouveau texte</strong>", strings.TrimSpace(message.BodyHTML))
	assert.Equal(1, fixture.client.updateCalls)
}
