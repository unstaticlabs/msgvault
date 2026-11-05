package api

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/authz"
	"go.kenn.io/msgvault/internal/meetingcontent"
	"go.kenn.io/msgvault/internal/vector"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeTagReadsCheckVisibilityBeforeProviderAdapter(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	f := newVisibilityFixture(t)
	provider := &tagsHTTPStore{}
	f.srv.store = provider
	blocked := performSessionRequest(t, f.srv, http.MethodGet, "/api/v1/messages/22/tags", nil, withKey("alice-secret-value"), false)
	requirements.Equal(http.StatusNotFound, blocked.Code, blocked.Body.String())
	assertions.Zero(provider.calls)
	allowed := performSessionRequest(t, f.srv, http.MethodGet, "/api/v1/messages/11/tags", nil, withKey("alice-secret-value"), false)
	requirements.Equal(http.StatusOK, allowed.Code, allowed.Body.String())
	assertions.Equal(1, provider.calls)
	mutation := performSessionRequest(t, f.srv, http.MethodPost, "/api/v1/messages/11/tags", []byte(`{"add":["Next"]}`), withKey("alice-secret-value"), false)
	requirements.Equal(http.StatusForbidden, mutation.Code, mutation.Body.String())
	assertions.Equal(1, provider.calls, "member denial happens before the provider adapter")
}

func TestNativeMeetingMetricsIntersectAuthenticatedSources(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	f := newVisibilityFixture(t)
	backend := &meetingRouteStore{mockStore: &mockStore{}, metricsResult: &meetingcontent.Metrics{}}
	f.srv.store = backend
	response := performSessionRequest(t, f.srv, http.MethodPost, "/api/v1/meetings/metrics", []byte(`{"scope":{"source_ids":[1,2]}}`), withKey("alice-secret-value"), false)
	requirements.Equal(http.StatusOK, response.Code, response.Body.String())
	assertions.Equal([]int64{f.one}, backend.metricsScope.SourceIDs)
	requirements.NoError(f.st.SetUserSources(context.Background(), f.alice.ID, []int64{}))
	f.srv.visibility.reset()
	response = performSessionRequest(t, f.srv, http.MethodPost, "/api/v1/meetings/metrics", []byte(`{}`), withKey("alice-secret-value"), false)
	requirements.Equal(http.StatusOK, response.Code, response.Body.String())
	assertions.Equal([]int64{-1}, backend.metricsScope.SourceIDs, "an empty binding must never become an unrestricted query")
}

func TestNativeInferenceLoginKeepsOIDCAdminOwnersSeparate(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	server := &Server{}
	ownerFor := func(subject string) (string, bool) {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/settings/people-inference/codex/login", nil)
		auth := requestAuthentication{Mode: AuthModeToken, Principal: authz.Principal{Role: authz.RoleAdmin, IdentityIssuer: "https://issuer.example.test", IdentitySubject: subject}}
		request = request.WithContext(context.WithValue(request.Context(), requestSecurityContextKey{}, requestSecurity{auth: auth}))
		return server.peopleCodexLoginOwner(httptest.NewRecorder(), request)
	}
	first, ok := ownerFor("qa-one")
	requirements.True(ok)
	same, ok := ownerFor("qa-one")
	requirements.True(ok)
	other, ok := ownerFor("qa-two")
	requirements.True(ok)
	assertions.Equal(first, same)
	assertions.NotEqual(first, other)
	assertions.NotContains(first, "qa-one")
	_, ok = ownerFor("")
	assertions.False(ok, "an unbound bearer token cannot share the API-key owner's login")
}

func TestEmbeddingCoverageUsesExistingSourceVisibility(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	f := newVisibilityFixture(t)
	requested := []int64{}
	f.srv.SetEmbeddingStatus(func(_ context.Context, sourceID int64) (vector.EmbeddingStatus, error) {
		requested = append(requested, sourceID)
		return vector.EmbeddingStatus{Eligible: 10, Current: 8, Pending: 2, Failed: 99, WindowBatches: 3, WindowSeconds: 1, Diagnostics: &vector.EmbeddingDiagnostics{}}, nil
	}, 3)
	response := performSessionRequest(t, f.srv, http.MethodGet, "/api/v1/embeddings/status", nil, withKey("alice-secret-value"), false)
	requirements.Equal(http.StatusOK, response.Code, response.Body.String())
	var status vector.EmbeddingStatus
	requirements.NoError(decodeJSONBody(response, &status))
	assertions.Equal([]int64{f.one}, requested)
	assertions.Equal(f.one, status.SourceID)
	assertions.Equal(int64(10), status.Eligible)
	assertions.Equal(int64(8), status.Current)
	assertions.Equal(int64(2), status.Pending)
	assertions.Zero(status.Failed)
	assertions.Nil(status.Diagnostics)
	assertions.Nil(status.MessagesPerMinute)
	assertions.Zero(status.WindowBatches)
	response = performSessionRequest(t, f.srv, http.MethodGet, "/api/v1/embeddings/status?source_id="+itoa(f.two), nil, withKey("alice-secret-value"), false)
	assertions.Equal(http.StatusBadRequest, response.Code)
	assertions.Equal([]int64{f.one}, requested, "hidden sources never reach the reader")
	requirements.NoError(f.st.SetUserSources(t.Context(), f.alice.ID, []int64{f.one, f.two}))
	f.srv.visibility.reset()
	response = performSessionRequest(t, f.srv, http.MethodGet, "/api/v1/embeddings/status", nil, withKey("alice-secret-value"), false)
	requirements.Equal(http.StatusOK, response.Code, response.Body.String())
	requirements.NoError(decodeJSONBody(response, &status))
	assertions.Equal(int64(20), status.Eligible, "default coverage sums the caller's available sources")
	assertions.Equal(int64(16), status.Current)
	assertions.Equal(int64(4), status.Pending)
	assertions.Nil(status.Diagnostics)
	response = performSessionRequest(t, f.srv, http.MethodGet, "/api/v1/embeddings/status?source_id="+itoa(f.two), nil, withKey("alice-secret-value"), false)
	requirements.Equal(http.StatusOK, response.Code, response.Body.String())
	assertions.Equal([]int64{f.one, f.one, f.two, f.two}, requested)
	response = performSessionRequest(t, f.srv, http.MethodGet, "/api/v1/embeddings/status", nil, withKey("admin-secret-value"), false)
	requirements.Equal(http.StatusOK, response.Code, response.Body.String())
	requirements.NoError(decodeJSONBody(response, &status))
	assertions.Equal(int64(99), status.Failed, "native Administrator diagnostics remain intact")
	assertions.NotNil(status.Diagnostics)
}

func TestEmbeddingCoverageEmptyScopeAndGenerationSwitch(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	f := newVisibilityFixture(t)
	calls := 0
	f.srv.SetEmbeddingStatus(func(_ context.Context, sourceID int64) (vector.EmbeddingStatus, error) {
		calls++
		return vector.EmbeddingStatus{Generation: vector.EmbeddingGenerationStatus{ID: vector.GenerationID(sourceID), State: "active"}, Eligible: 50}, nil
	}, 3)
	requirements.NoError(f.st.SetUserSources(t.Context(), f.alice.ID, []int64{}))
	f.srv.visibility.reset()
	response := performSessionRequest(t, f.srv, http.MethodGet, "/api/v1/embeddings/status", nil, withKey("alice-secret-value"), false)
	requirements.Equal(http.StatusOK, response.Code, response.Body.String())
	var status vector.EmbeddingStatus
	requirements.NoError(decodeJSONBody(response, &status))
	assertions.Zero(status.Eligible)
	assertions.Zero(calls, "no source binding must never request global coverage")
	requirements.NoError(f.st.SetUserSources(t.Context(), f.alice.ID, []int64{f.one, f.two}))
	f.srv.visibility.reset()
	response = performSessionRequest(t, f.srv, http.MethodGet, "/api/v1/embeddings/status", nil, withKey("alice-secret-value"), false)
	assertions.Equal(http.StatusServiceUnavailable, response.Code, "mixed generations cannot be added into one coverage answer")
	assertions.Contains(response.Body.String(), "generation changed")
}
