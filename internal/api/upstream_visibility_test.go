package api

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/meetingcontent"
	"net/http"
	"testing"
)

func TestNativeTagReadsCheckVisibilityBeforeProviderAdapter(t *testing.T) {
	f := newVisibilityFixture(t)
	provider := &tagsHTTPStore{}
	f.srv.store = provider
	blocked := performSessionRequest(t, f.srv, http.MethodGet, "/api/v1/messages/22/tags", nil, withKey("alice-secret-value"), false)
	require.Equal(t, http.StatusNotFound, blocked.Code, blocked.Body.String())
	assert.Zero(t, provider.calls)
	allowed := performSessionRequest(t, f.srv, http.MethodGet, "/api/v1/messages/11/tags", nil, withKey("alice-secret-value"), false)
	require.Equal(t, http.StatusOK, allowed.Code, allowed.Body.String())
	assert.Equal(t, 1, provider.calls)
	mutation := performSessionRequest(t, f.srv, http.MethodPost, "/api/v1/messages/11/tags", []byte(`{"add":["Next"]}`), withKey("alice-secret-value"), false)
	require.Equal(t, http.StatusForbidden, mutation.Code, mutation.Body.String())
	assert.Equal(t, 1, provider.calls, "member denial happens before the provider adapter")
}

func TestNativeMeetingMetricsIntersectAuthenticatedSources(t *testing.T) {
	f := newVisibilityFixture(t)
	backend := &meetingRouteStore{mockStore: &mockStore{}, metricsResult: &meetingcontent.Metrics{}}
	f.srv.store = backend
	response := performSessionRequest(t, f.srv, http.MethodPost, "/api/v1/meetings/metrics", []byte(`{"scope":{"source_ids":[1,2]}}`), withKey("alice-secret-value"), false)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, []int64{f.one}, backend.metricsScope.SourceIDs)
	require.NoError(t, f.st.SetUserSources(context.Background(), f.alice.ID, []int64{}))
	f.srv.visibility.reset()
	response = performSessionRequest(t, f.srv, http.MethodPost, "/api/v1/meetings/metrics", []byte(`{}`), withKey("alice-secret-value"), false)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, []int64{-1}, backend.metricsScope.SourceIDs, "an empty binding must never become an unrestricted query")
}
