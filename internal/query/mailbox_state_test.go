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
	st := testutil.NewTestStore(t)
	for _, provider := range []string{"gmail", "imap", "slack"} {
		t.Run(provider, func(t *testing.T) {
			source, err := st.GetOrCreateSource(provider, provider+"@example.com")
			require.NoError(t, err)
			conv, err := st.EnsureConversation(source.ID, "state-thread", "[QA] Provider state")
			require.NoError(t, err)
			id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conv, SourceMessageID: "state-id", MessageType: "email"})
			require.NoError(t, err)
			engine := query.NewEngine(st.DB(), st.IsPostgreSQL())
			state := func() []string {
				message, err := engine.GetMessage(context.Background(), id)
				require.NoError(t, err)
				require.NotNil(t, message)
				rows, err := engine.GetMessageSummariesByIDs(context.Background(), []int64{id})
				require.NoError(t, err)
				if message.DeletedAt == nil {
					require.Len(t, rows, 1)
					assert.Equal(t, source.ID, rows[0].SourceID)
					assert.Equal(t, "state-id", rows[0].SourceMessageID)
					assert.Equal(t, message.InboxState, rows[0].InboxState)
					assert.Equal(t, message.ReadState, rows[0].ReadState)
				} else {
					require.Empty(t, rows, "provider-deleted rows keep the existing summary exclusion")
				}
				apiMessage, err := st.GetMessage(id)
				require.NoError(t, err)
				assert.Equal(t, message.InboxState, apiMessage.InboxState)
				assert.Equal(t, message.ReadState, apiMessage.ReadState)
				return []string{message.InboxState, message.ReadState}
			}
			assert.Equal(t, []string{"unknown", "unknown"}, state())
			if provider == "slack" {
				return
			}
			inboxLabel, err := st.EnsureLabel(source.ID, "INBOX", "INBOX", "system")
			require.NoError(t, err)
			unreadLabel, err := st.EnsureLabel(source.ID, "UNREAD", "UNREAD", "system")
			require.NoError(t, err)
			require.NoError(t, st.AddMessageLabels(id, []int64{inboxLabel, unreadLabel}))
			if provider == "imap" {
				require.NoError(t, st.ApplyIMAPMailboxDeltas(source.ID, []store.IMAPMailboxDelta{{Mailbox: "INBOX", Reset: true, State: store.IMAPFolderState{Mailbox: "INBOX", UIDValidity: 1, UIDNext: 2}, Memberships: []store.IMAPMembershipObservation{{UID: 1, SourceMessageID: "state-id", Flags: []string{}}}}}))
			}
			assert.Equal(t, []string{"inbox", "unread"}, state())
			_, err = st.DB().Exec(st.Rebind("UPDATE messages SET is_read = TRUE WHERE id = ?"), id)
			require.NoError(t, err)
			assert.Equal(t, []string{"inbox", "unread"}, state(), "local application read is not provider read")
			if provider == "gmail" {
				_, err = st.RemoveLabelBySourceMessageIDs(source.ID, "UNREAD", []string{"state-id"})
				require.NoError(t, err)
			} else {
				delta := store.IMAPMailboxDelta{Mailbox: "INBOX", Reset: true, State: store.IMAPFolderState{Mailbox: "INBOX", UIDValidity: 1, UIDNext: 2}, Memberships: []store.IMAPMembershipObservation{{UID: 1, SourceMessageID: "state-id", Flags: []string{"\\Seen"}}}}
				require.NoError(t, st.ApplyIMAPMailboxDeltas(source.ID, []store.IMAPMailboxDelta{delta}))
			}
			assert.Equal(t, []string{"inbox", "read"}, state())
			_, err = st.RemoveLabelBySourceMessageIDs(source.ID, "INBOX", []string{"state-id"})
			require.NoError(t, err)
			assert.Equal(t, []string{"outside", "read"}, state())
			if provider == "imap" {
				// Shape-valid JSON can still be incomplete provider metadata.
				flags := `{"seen":true}`
				_, err = st.DB().Exec(st.Rebind("UPDATE imap_message_memberships SET flags = ? WHERE source_id = ?"), flags, source.ID)
				require.NoError(t, err)
				assert.Equal(t, []string{"outside", "unknown"}, state())
			}
			_, err = st.DB().Exec(st.Rebind("UPDATE messages SET deleted_from_source_at = CURRENT_TIMESTAMP WHERE id = ?"), id)
			require.NoError(t, err)
			assert.Equal(t, []string{"unknown", "unknown"}, state())
		})
	}
}
