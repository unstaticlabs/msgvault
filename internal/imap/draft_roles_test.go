package imap

import (
	"testing"

	imapv2 "github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestListLabelsPreservesAuthoritativeLocalizedDraftRole(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	addr, _ := testutil.StartIMAPMemServerWithSpecialUse(t, map[string]int{"INBOX": 0, "Brouillons": 0, "User Drafts": 0}, map[string][]imapv2.MailboxAttr{"Brouillons": {imapv2.MailboxAttrDrafts}})
	client := newTestClient(t, addr)
	labels, err := client.ListLabels(t.Context())
	requirements.NoError(err)
	roles := map[string]string{}
	for _, label := range labels {
		roles[label.Name] = label.SystemRole
	}
	assertions.Equal("draft", roles["Brouillons"])
	assertions.Empty(roles["User Drafts"], "a display name cannot establish draft evidence")
	assertions.Equal("draft", systemRoleForMailbox([]imapv2.MailboxAttr{imapv2.MailboxAttrDrafts, imapv2.MailboxAttrSent}), "conflicting metadata cannot confidently establish completed Sent")
}
