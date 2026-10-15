package api

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/authz"
	"go.kenn.io/msgvault/internal/meetingcontent"
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
