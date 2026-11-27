package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/gmail"
	msgmime "go.kenn.io/msgvault/internal/mime"
)

func syntheticExternalAlternativeDraft(plain string, file bool) []byte {
	headers := "From: owner@example.test\r\n" +
		"To: recipient@example.test\r\n" +
		"Cc: copy@example.test\r\n" +
		"Bcc: hidden@example.test\r\n" +
		"Reply-To: replies@example.test\r\n" +
		"Subject: Synthetic draft\r\n" +
		"Message-ID: <external-client@example.test>\r\n" +
		"In-Reply-To: <parent@example.test>\r\n" +
		"References: <root@example.test> <parent@example.test>\r\n" +
		"MIME-Version: 1.0\r\n"
	alternative := "Content-Type: multipart/alternative; boundary=synthetic-alternative\r\n\r\n" +
		"--synthetic-alternative\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		plain + "\r\n" +
		"--synthetic-alternative\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><body><div><b>same</b> draft <i>text</i></div></body></html>\r\n" +
		"--synthetic-alternative--\r\n"
	if !file {
		return []byte(headers + alternative)
	}
	return []byte(headers + "Content-Type: multipart/mixed; boundary=outer\r\n\r\n--outer\r\n" +
		alternative + "--outer\r\nContent-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=fixture.bin\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		"AAECA/8=\r\n--outer--\r\n")
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
		ID: "gmail-message-client-replacement", ThreadID: "gmail-thread-1", Raw: syntheticExternalAlternativeDraft("same draft text", false),
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
	assert.Equal(syntheticExternalAlternativeDraft("same draft text", false), previousRaw)
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
	external := strings.Replace(string(syntheticExternalAlternativeDraft("same draft text", false)), "<b>same</b> draft <i>text</i>", "une autre version", 1)
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

// A mail client can leave an empty or whitespace-only plain-text part alongside
// a formatted draft. Exercise the archive read and provider update separately.
func TestGmailDraftAppleMailEmptyPlainAlternative(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plain string
		file  bool
	}{
		{name: "multipart alternative"},
		{name: "mixed with a file", file: true},
		{name: "blank plain part", plain: " \t\r\n"},
		{name: "blank plain part with a file", plain: " \t\r\n", file: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, check := require.New(t), assert.New(t)
			fixture := newGmailDraftTestFixture(t)
			draft := fixture.seedDraft(t)
			fixture.client.updateDraft = &gmail.Draft{ID: "gmail-draft-managed", Message: gmail.RawMessage{ID: "local-save", ThreadID: "gmail-thread-1"}}
			_, err := fixture.lifecycle(t, api.CLIRunDraftEditCommand, draft, "Local version")
			req.NoError(err)
			draft, err = fixture.store.GetGmailDraftContext(t.Context(), draft.DraftID)
			req.NoError(err)
			req.Equal(int64(2), draft.Revision)

			raw := syntheticExternalAlternativeDraft(tc.plain, tc.file)
			fixture.client.getDraft = &gmail.Draft{ID: "gmail-draft-managed", Message: gmail.RawMessage{
				ID: "apple-mail-save", ThreadID: "gmail-thread-1", Raw: raw,
			}}
			before := fixture.client.updateCalls
			_, err = fixture.lifecycle(t, api.CLIRunDraftEditCommand, draft, "new draft text")
			req.ErrorContains(err, "changed_externally")
			check.Equal(before, fixture.client.updateCalls)
			read, err := runReviewLifecycle(t, fixture.adapter, api.CLIRunDraftGetCommand, draft.DraftID, "--json")
			req.NoError(err)
			req.Len(read, 1)
			var adopted gmailDraftLifecycleOutput
			req.NoError(json.Unmarshal([]byte(read[0].Data), &adopted))
			req.Equal(int64(3), adopted.Revision)
			check.Equal(draft.DraftID, adopted.DraftID)
			for _, word := range []string{"same", "draft", "text"} {
				check.Contains(adopted.Content, word)
			}
			check.Equal(string(raw), adopted.RawMIME)
			_, external, err := fixture.store.GetMessageBodyAndRawContext(t.Context(), adopted.MessageID)
			req.NoError(err)
			req.Equal(raw, external)

			fixture.client.updateDraft = &gmail.Draft{ID: "gmail-draft-managed", Message: gmail.RawMessage{ID: "saved-after-retry", ThreadID: "gmail-thread-1"}}
			_, err = runReviewLifecycle(t, fixture.adapter, api.CLIRunDraftEditCommand, draft.DraftID, "--revision", "3", "--body", "new draft text", "--json")
			req.NoError(err)
			read, err = runReviewLifecycle(t, fixture.adapter, api.CLIRunDraftGetCommand, draft.DraftID, "--json")
			req.NoError(err)
			req.Len(read, 1)
			var final gmailDraftLifecycleOutput
			req.NoError(json.Unmarshal([]byte(read[0].Data), &final))
			req.Equal(int64(4), final.Revision)
			check.Equal(draft.DraftID, final.DraftID)
			check.Equal("new draft text", strings.TrimSpace(final.Content))
			check.Equal(before+1, fixture.client.updateCalls)
			check.Zero(fixture.client.createCalls)
			check.Zero(fixture.client.deleteCalls)
			_, saved, err := fixture.store.GetMessageBodyAndRawContext(t.Context(), final.MessageID)
			req.NoError(err)
			parsed, err := msgmime.Parse(saved)
			req.NoError(err)
			check.Equal("new draft text", strings.TrimSpace(parsed.BodyText))
			check.Contains(parsed.BodyHTML, "<b>new</b> draft <i>text</i>")
			check.Equal("gmail-draft-managed", fixture.client.updatedDraftID)
			check.Equal("gmail-thread-1", fixture.client.updatedThreadID)
			check.Equal(fixture.client.updatedRaw, saved)
			for _, recipient := range []struct {
				addresses []msgmime.Address
				want      string
			}{
				{parsed.To, "recipient@example.test"}, {parsed.Cc, "copy@example.test"},
				{parsed.Bcc, "hidden@example.test"}, {parsed.ReplyTo, "replies@example.test"},
			} {
				req.Len(recipient.addresses, 1)
				check.Equal(recipient.want, recipient.addresses[0].Email)
			}
			original, err := msgmime.Parse(raw)
			req.NoError(err)
			check.Equal(original.InReplyTo, parsed.InReplyTo)
			check.Equal(original.References, parsed.References)
			if tc.file {
				req.Len(parsed.Attachments, 1)
				check.Equal("fixture.bin", parsed.Attachments[0].Filename)
				check.Equal([]byte{0, 1, 2, 3, 255}, parsed.Attachments[0].Content)
			}
		})
	}
}
