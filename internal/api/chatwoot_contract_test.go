package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/agentgrant"
	"go.kenn.io/msgvault/internal/apiprotocol"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

// Exercise the public wire contract against real archive rows and native source
// permissions: an alternate call representation must never disclose another inbox.
func TestChatwootRelatedLinksRespectSourceAuthority(t *testing.T) {
	st := testutil.NewTestStore(t)
	allowed, err := st.GetOrCreateSource("chatwoot", "https://support.example.test/accounts/1/inboxes/1")
	require.NoError(t, err)
	hidden, err := st.GetOrCreateSource("chatwoot", "https://support.example.test/accounts/1/inboxes/2")
	require.NoError(t, err)
	seed := func(source int64, key, kind string) int64 {
		t.Helper()
		conv, err := st.EnsureConversationWithType(source, key, "direct_chat", "Synthetic call")
		require.NoError(t, err)
		id, err := st.UpsertMessage(&store.Message{SourceID: source, ConversationID: conv, SourceMessageID: key, MessageType: kind, Subject: sql.NullString{String: "Synthetic call", Valid: true}})
		require.NoError(t, err)
		return id
	}
	call := seed(allowed.ID, "call", "chatwoot")
	meeting := seed(allowed.ID, "meeting", "meeting_transcript")
	otherMeeting := seed(hidden.ID, "hidden-meeting", "meeting_transcript")
	forged := seed(allowed.ID, "cross-source-call", "chatwoot")
	for id, metadata := range map[int64]string{
		call:    fmt.Sprintf(`{"meeting_message_id":%d}`, meeting),
		meeting: fmt.Sprintf(`{"provider":"chatwoot","chat_message_id":%d}`, call),
		forged:  fmt.Sprintf(`{"meeting_message_id":%d}`, otherMeeting),
	} {
		require.NoError(t, st.SetMessageMetadata(id, sql.NullString{String: metadata, Valid: true}))
	}
	cfg := &config.Config{HomeDir: t.TempDir(), Server: config.ServerConfig{APIKey: "synthetic-owner", AgentAccess: true}}
	for _, role := range []string{"member", "viewer"} {
		email := role + "@example.test"
		user, err := st.RecordUserLogin(t.Context(), store.UserLogin{Issuer: "https://idp.example.test", Subject: role, Email: email, DisplayName: "Synthetic reader", Role: role})
		require.NoError(t, err)
		require.NoError(t, st.SetUserSources(t.Context(), user.ID, []int64{allowed.ID}))
		cfg.Auth.APIKeys = append(cfg.Auth.APIKeys, config.APIKeyConfig{Name: role, Key: "synthetic-" + role, Role: role, User: email})
	}
	cfg.Auth.APIKeys = append(cfg.Auth.APIKeys, config.APIKeyConfig{Name: "unbound", Key: "synthetic-unbound", Role: "viewer"})
	srv := NewServerWithOptions(ServerOptions{Config: cfg, Store: st, UserStore: st, Engine: query.NewEngine(st.DB(), st.IsPostgreSQL()), Logger: testLogger()})
	t.Cleanup(func() { require.NoError(t, srv.Shutdown(context.Background())) })
	_, token, _, err := srv.agentGrants.Issue("synthetic-reader", []agentgrant.Permission{agentgrant.PermissionMessageRead}, []agentgrant.SourceRef{{ID: allowed.ID, Type: allowed.SourceType, Identifier: allowed.Identifier}}, time.Time{})
	require.NoError(t, err)
	callers := map[string]http.Header{
		"member": withKey("synthetic-member"),
		"viewer": withKey("synthetic-viewer"),
		"agent":  {apiprotocol.AgentTokenHeader: []string{token}},
	}
	for name, headers := range callers {
		t.Run(name, func(t *testing.T) {
			for _, pair := range [][2]int64{{call, meeting}, {meeting, call}} {
				response := performSessionRequest(t, srv, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", pair[0]), nil, headers, false)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				var body map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
				require.Contains(t, body, "related_message_id")
				var target int64
				require.NoError(t, json.Unmarshal(body["related_message_id"], &target))
				assert.Equal(t, pair[1], target)
			}
			response := performSessionRequest(t, srv, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", forged), nil, headers, false)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var body map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			assert.NotContains(t, body, "related_message_id", "metadata cannot disclose a hidden inbox's message ID")
			response = performSessionRequest(t, srv, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", otherMeeting), nil, headers, false)
			assert.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
		})
	}
	response := performSessionRequest(t, srv, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", call), nil, withKey("synthetic-unbound"), false)
	assert.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
}

func TestChatwootAdditiveMessageSchemasRetainForkContracts(t *testing.T) {
	for name, doc := range map[string]func() *huma.OpenAPI{"server": OpenAPIDocument, "client": openAPIClientDocument} {
		t.Run(name, func(t *testing.T) {
			schemas := doc().Components.Schemas.Map()
			for _, name := range []string{"MessageDetail", "CliMessageResponse"} {
				schema := schemas[name]
				require.NotNil(t, schema, name)
				for _, field := range []string{"calendar_state", "related_message_id"} {
					assert.Contains(t, schema.Properties, field, name)
					assert.NotContains(t, schema.Required, field, "old readers and ordinary messages remain valid")
				}
			}
			for _, field := range []string{"source_id", "inbox_state", "read_state"} {
				assert.Contains(t, schemas["CliMessageResponse"].Properties, field)
			}
		})
	}
}
