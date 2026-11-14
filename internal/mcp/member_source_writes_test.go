package mcp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/authz"
	"go.kenn.io/msgvault/internal/query/querytest"
)

// TestMemberSourceWritesOffersMailboxToolsToMembers checks that the opt-in
// offers exactly the source-confined write tools to members, and nothing to
// viewers. The daemon confines each call to the member's own sources.
func TestMemberSourceWritesOffersMailboxToolsToMembers(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	for _, enabled := range []bool{false, true} {
		opts := ServeOptions{
			Engine: &querytest.MockEngine{}, AttachmentsDir: t.TempDir(),
			AllowMailboxWrites: true, InboxArchiver: &fakeInboxArchiver{},
			MemberSourceWrites: enabled,
		}
		handler := newMCPHTTPServer(opts, HTTPOptions{
			APIKey:      "admin-secret-value",
			AllowWrites: true,
			Keys: []NamedKey{
				{Name: "reader", Key: "reader-secret-value", Role: authz.RoleViewer, User: "alice@example.com"},
				{Name: "assistant", Key: "assistant-secret-value", Role: authz.RoleMember, User: "alice@example.com"},
			},
		}).Handler

		member, status := rawAuthorizedToolNames(t, handler, "Bearer assistant-secret-value")
		require.Equal(http.StatusOK, status)
		viewer, status := rawAuthorizedToolNames(t, handler, "Bearer reader-secret-value")
		require.Equal(http.StatusOK, status)
		admin, status := rawAuthorizedToolNames(t, handler, "Bearer admin-secret-value")
		require.Equal(http.StatusOK, status)

		for _, tool := range []string{ToolStageDeletion, ToolArchiveFromInbox} {
			if enabled {
				assert.Contains(member, tool, "the opt-in offers %s to members", tool)
			} else {
				assert.NotContains(member, tool, "%s stays administrator-only by default", tool)
			}
			assert.NotContains(viewer, tool, "viewers never get %s", tool)
			assert.Contains(admin, tool)
		}
		assert.NotContains(member, ToolExportAttachment, "server filesystem writes stay administrator-only")
	}
}
