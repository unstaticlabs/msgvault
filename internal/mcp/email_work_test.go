package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/query"
)

type emailWorkReadFixture struct {
	*savedViewServiceFixture

	view, revision int64
	uid, cache     string
}

func (f *emailWorkReadFixture) GetEmailWorkItem(_ context.Context, view, revision int64, uid, cache string, _, _ int) (*query.EmailWorkMembersResponse, error) {
	f.view = view
	f.revision = revision
	f.uid = uid
	f.cache = cache
	return &query.EmailWorkMembersResponse{UID: uid, RequestedUID: uid, CacheRevision: cache, Members: []query.EmailWorkMemberFact{{MessageID: 1, MessageUID: "copy-1", SourceID: 1, SourceIdentifier: "qa@example.test", SourceMessageID: "provider-1", RFCMessageID: "qa@example.test"}}, TotalCount: 1, Summary: query.EmailWorkSummary{UID: uid, State: "unknown", SourceIDs: []int64{1}, Reasons: []string{"identity_unavailable"}}, OutstandingMessageIDs: []int64{}, EvidenceMessageIDs: []int64{}}, nil
}
func TestMCPEmailWorkReaderUsesProducingScopeAndRevisions(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	service := &emailWorkReadFixture{savedViewServiceFixture: &savedViewServiceFixture{}}
	opts := ServeOptions{SavedViews: service}
	tools := toolsByName(t, rawListTools(t, opts, false))
	requirements.Contains(tools, ToolGetEmailWorkItem)
	result := rawCallTool(t, opts, ToolGetEmailWorkItem, map[string]any{"saved_view_id": 7, "saved_view_revision": 2, "work_uid": "work-qa", "cache_revision": "cache-qa"})
	assertions.Equal(int64(7), service.view)
	assertions.Equal(int64(2), service.revision)
	assertions.Equal("work-qa", service.uid)
	assertions.Equal("cache-qa", service.cache)
	assertions.NotContains(result, "isError")
	absent := toolsByName(t, rawListTools(t, ServeOptions{SavedViews: &savedViewServiceFixture{}}, false))
	assertions.NotContains(absent, ToolGetEmailWorkItem, "a service without native family reads must not advertise them")
}
