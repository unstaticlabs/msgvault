package query_test

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
	"testing"
)

func TestProviderMailboxStateIgnoresLocalReadAndPreservesUnknown(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	st := testutil.NewTestStore(t)
	for _, provider := range []string{"gmail", "imap", "slack"} {
		t.Run(provider, func(t *testing.T) {
			source, err := st.GetOrCreateSource(provider, provider+"@example.com")
			requirements.NoError(err)
			conv, err := st.EnsureConversation(source.ID, "state-thread", "[QA] Provider state")
			requirements.NoError(err)
			id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conv, SourceMessageID: "state-id", MessageType: "email"})
			requirements.NoError(err)
			engine := query.NewEngine(st.DB(), st.IsPostgreSQL())
			state := func() []string {
				message, err := engine.GetMessage(context.Background(), id)
				requirements.NoError(err)
				requirements.NotNil(message)
				rows, err := engine.GetMessageSummariesByIDs(context.Background(), []int64{id})
				requirements.NoError(err)
				if message.DeletedAt == nil {
					requirements.Len(rows, 1)
					assertions.Equal(source.ID, rows[0].SourceID)
					assertions.Equal("state-id", rows[0].SourceMessageID)
					assertions.Equal(message.InboxState, rows[0].InboxState)
					assertions.Equal(message.ReadState, rows[0].ReadState)
				} else {
					requirements.Empty(rows, "provider-deleted rows keep the existing summary exclusion")
				}
				apiMessage, err := st.GetMessage(id)
				requirements.NoError(err)
				assertions.Equal(message.InboxState, apiMessage.InboxState)
				assertions.Equal(message.ReadState, apiMessage.ReadState)
				return []string{message.InboxState, message.ReadState}
			}
			assertions.Equal([]string{"unknown", "unknown"}, state())
			if provider == "slack" {
				return
			}
			inboxLabel, err := st.EnsureLabel(source.ID, "INBOX", "INBOX", "system")
			requirements.NoError(err)
			unreadLabel, err := st.EnsureLabel(source.ID, "UNREAD", "UNREAD", "system")
			requirements.NoError(err)
			requirements.NoError(st.AddMessageLabels(id, []int64{inboxLabel, unreadLabel}))
			if provider == "imap" {
				requirements.NoError(st.ApplyIMAPMailboxDeltas(source.ID, []store.IMAPMailboxDelta{{Mailbox: "INBOX", Reset: true, State: store.IMAPFolderState{Mailbox: "INBOX", UIDValidity: 1, UIDNext: 2}, Memberships: []store.IMAPMembershipObservation{{UID: 1, SourceMessageID: "state-id", Flags: []string{}}}}}))
			}
			assertions.Equal([]string{"inbox", "unread"}, state())
			_, err = st.DB().Exec(st.Rebind("UPDATE messages SET is_read = TRUE WHERE id = ?"), id)
			requirements.NoError(err)
			assertions.Equal([]string{"inbox", "unread"}, state(), "local application read is not provider read")
			if provider == "gmail" {
				_, err = st.RemoveLabelBySourceMessageIDs(source.ID, "UNREAD", []string{"state-id"})
				requirements.NoError(err)
			} else {
				delta := store.IMAPMailboxDelta{Mailbox: "INBOX", Reset: true, State: store.IMAPFolderState{Mailbox: "INBOX", UIDValidity: 1, UIDNext: 2}, Memberships: []store.IMAPMembershipObservation{{UID: 1, SourceMessageID: "state-id", Flags: []string{"\\Seen"}}}}
				requirements.NoError(st.ApplyIMAPMailboxDeltas(source.ID, []store.IMAPMailboxDelta{delta}))
			}
			assertions.Equal([]string{"inbox", "read"}, state())
			_, err = st.RemoveLabelBySourceMessageIDs(source.ID, "INBOX", []string{"state-id"})
			requirements.NoError(err)
			assertions.Equal([]string{"outside", "read"}, state())
			if provider == "imap" {
				// Shape-valid JSON can still be incomplete provider metadata.
				flags := `{"seen":true}`
				_, err = st.DB().Exec(st.Rebind("UPDATE imap_message_memberships SET flags = ? WHERE source_id = ?"), flags, source.ID)
				requirements.NoError(err)
				assertions.Equal([]string{"outside", "unknown"}, state())
			}
			_, err = st.DB().Exec(st.Rebind("UPDATE messages SET deleted_from_source_at = CURRENT_TIMESTAMP WHERE id = ?"), id)
			requirements.NoError(err)
			assertions.Equal([]string{"unknown", "unknown"}, state())
		})
	}
}
