package mcp

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/daemonclient"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/query/querytest"
	"go.kenn.io/msgvault/internal/search"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

// Dispatch real nested API requests in process, as the daemon does. A network
// httptest server would discard the request context this contract preserves.
type principalAPIRoundTripper struct{ handler http.Handler }

func (transport principalAPIRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Host = "msgvault.internal"
	request.RemoteAddr = "127.0.0.1:0"
	response := httptest.NewRecorder()
	transport.handler.ServeHTTP(response, request)
	return response.Result(), nil
}

func TestNativeMCPKeepsNamedKeyRoleAndSources(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	st := testutil.NewTestStore(t)
	first, err := st.GetOrCreateSource("gmail", "first@example.test")
	requirements.NoError(err)
	second, err := st.GetOrCreateSource("gmail", "second@example.test")
	requirements.NoError(err)
	keys := []config.APIKeyConfig{}
	for _, role := range []string{"viewer", "member", "admin"} {
		email := role + "@example.test"
		user, err := st.RecordUserLogin(t.Context(), store.UserLogin{Issuer: "https://idp.example.test", Subject: role, Email: email, Role: role})
		requirements.NoError(err)
		requirements.NoError(st.SetUserSources(t.Context(), user.ID, []int64{first.ID}))
		keys = append(keys, config.APIKeyConfig{Name: role, Key: "synthetic-" + role, Role: role, User: email})
	}
	cfg := &config.Config{Server: config.ServerConfig{APIKey: "synthetic-owner"}, Auth: config.AuthConfig{APIKeys: keys}}
	engine := &querytest.MockEngine{Messages: map[int64]*query.MessageDetail{
		11: {ID: 11, SourceID: first.ID, Subject: "First fixture", MessageType: "email", BodyText: "New\n> Old\n", InboxState: "inbox", ReadState: "unread", CalendarState: &store.CalendarEventState{Status: "confirmed"}},
		22: {ID: 22, SourceID: second.ID, Subject: "Second fixture", MessageType: "email", BodyText: "Private\n> History\n", InboxState: "outside", ReadState: "read"},
	}}
	server := api.NewServerWithOptions(api.ServerOptions{Config: cfg, Engine: engine, Store: st, SavedViewStore: st, UserStore: st, Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() {
		requirements := require.New(t)
		requirements.NoError(server.Shutdown(context.Background()))
	})
	client, err := daemonclient.New(daemonclient.Config{URL: "http://msgvault.internal", APIKey: "synthetic-owner", AllowInsecure: true, HTTPClient: &http.Client{Transport: principalAPIRoundTripper{server.Router()}}})
	requirements.NoError(err)
	t.Cleanup(func() {
		requirements := require.New(t)
		requirements.NoError(client.Close())
	})
	opts := ServeOptions{Engine: daemonclient.NewEngineAdapter(client), SavedViews: client}
	handler, closeHandler := NewHTTPHandler(opts, HTTPOptions{AllowWrites: true})
	requirements.NoError(server.InstallMCPHandlers(handler, handler, closeHandler))
	for _, role := range []string{"viewer", "member", "admin"} {
		t.Run(role, func(t *testing.T) {
			assertions := assert.New(t)
			requirements := require.New(t)
			response, status := rawAuthorizedCall(t, server.Router(), "Bearer synthetic-"+role, "tools/list", map[string]any{})
			requirements.Equal(http.StatusOK, status)
			requirements.Empty(response.Error)
			tools, ok := response.Result["tools"].([]any)
			requirements.True(ok)
			names := []string{}
			for _, tool := range tools {
				name, _ := tool.(map[string]any)["name"].(string)
				names = append(names, name)
			}
			assertions.Contains(names, ToolGetMessage)
			if role == "viewer" {
				assertions.NotContains(names, ToolCreateSavedView)
			} else {
				assertions.Contains(names, ToolCreateSavedView)
			}
			if role == "admin" {
				assertions.Contains(names, ToolStageDeletion)
			} else {
				assertions.NotContains(names, ToolStageDeletion)
			}
			for _, id := range []int{11, 22} {
				response, status = rawAuthorizedCall(t, server.Router(), "Bearer synthetic-"+role, "tools/call", map[string]any{"name": ToolGetMessage, "arguments": map[string]any{"id": id, "include_segments": true}})
				requirements.Equal(http.StatusOK, status)
				requirements.Empty(response.Error)
				isError, text := toolResultText(t, response.Result)
				assertions.Equal(id == 22 && role != "admin", isError, text)
				if !isError {
					var message getMessageResponse
					requirements.NoError(json.Unmarshal([]byte(text), &message))
					assertions.Equal(engine.Messages[int64(id)].SourceID, message.SourceID)
					assertions.Equal(engine.Messages[int64(id)].InboxState, message.InboxState)
					assertions.Equal(engine.Messages[int64(id)].ReadState, message.ReadState)
					assertions.Equal(engine.Messages[int64(id)].CalendarState, message.CalendarState)
					assertions.Equal(engine.Messages[int64(id)].BodyText, message.BodyText)
					assertions.Equal("seg-v1", message.SegmentsVersion)
					requirements.Len(message.Segments, 2)
					assertions.Equal("authored", string(message.Segments[0].Kind))
					assertions.Equal("quoted", string(message.Segments[1].Kind))
				} else {
					assertions.NotContains(text, "History", "denied sources never reach body segmentation")
				}
			}
			if role != "viewer" {
				response, status = rawAuthorizedCall(t, server.Router(), "Bearer synthetic-"+role, "tools/call", map[string]any{"name": ToolCreateSavedView, "arguments": map[string]any{"name": "Native " + role, "schema_version": 2, "canonical_state": map[string]any{"presentation": "table"}}})
				requirements.Equal(http.StatusOK, status)
				requirements.Empty(response.Error)
				isError, text := toolResultText(t, response.Result)
				assertions.False(isError, text)
			}
		})
	}
	views, err := st.ListSavedViews(t.Context())
	requirements.NoError(err)
	assertions.Len(views, 2, "Member and Admin both retain native curation")
}

func TestStageDeletionReportsCompleteBoundedSelection(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	const available = 1300
	targets := make([]query.DeletionTarget, available)
	for i := range targets {
		targets[i] = query.DeletionTarget{MessageID: int64(i + 1), SourceID: 1, SourceType: "gmail", SourceIdentifier: "archive@example.test", SourceMessageID: fmt.Sprintf("message-%d", i)}
	}
	engine := &querytest.MockEngine{GetDeletionTargetsBySearchBoundedFunc: func(_ context.Context, _ *search.Query, _ query.MessageFilter, _ query.DeletionSearchMode, limit int) ([]query.DeletionTarget, int, error) {
		assertions := assert.New(t)
		assertions.Greater(limit, available)
		return targets, available, nil
	}}
	saver := &captureDeletionManifestSaver{}
	handler := &handlers{engine: engine, manifestSaver: saver}
	response := runTool[stageDeletionResponse](t, ToolStageDeletion, handler.stageDeletion, map[string]any{"query": "label:INBOX from:news"})
	assertions.Equal(available, response.MessageCount)
	requirements.NotNil(response.TotalMatching)
	assertions.Equal(available, *response.TotalMatching)
	assertions.False(response.HasMore)
	requirements.NotNil(saver.manifest)
	assertions.Len(saver.manifest.GmailIDs, available)
}
