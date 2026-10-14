package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/authz"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/daemonclient"
	"go.kenn.io/msgvault/internal/mcp"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

// This exercises the public MCP listener, daemon client, authenticated API,
// persisted definition and actual built Parquet cache together. No tool or
// query implementation is substituted.
func TestInboxSavedViewMCPThroughNativeDaemon(t *testing.T) {
	dir := t.TempDir()
	dbPath, cache := filepath.Join(dir, "mail.db"), filepath.Join(dir, "cache")
	st, err := store.Open(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()
	require.NoError(t, st.InitSchema())
	var sources []string
	var wantIDs []int64
	for account := range 3 {
		source, err := st.GetOrCreateSource("gmail", fmt.Sprintf("account-%d@example.com", account))
		require.NoError(t, err)
		sources = append(sources, strconv.FormatInt(source.ID, 10))
		conv, err := st.EnsureConversation(source.ID, "same-thread", "[QA] Independent copies")
		require.NoError(t, err)
		inbox, err := st.EnsureLabel(source.ID, "INBOX", "INBOX", "system")
		require.NoError(t, err)
		unread, err := st.EnsureLabel(source.ID, "UNREAD", "UNREAD", "system")
		require.NoError(t, err)
		for item := range 2 {
			id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conv, SourceMessageID: fmt.Sprintf("same-id-%d", item), MessageType: "email", SentAt: sql.NullTime{Time: time.Date(2026, 1, 1, 12, item, 0, 0, time.UTC), Valid: true}})
			require.NoError(t, err)
			labels := []int64{inbox}
			if item == 0 {
				labels = append(labels, unread)
				wantIDs = append(wantIDs, id)
			}
			require.NoError(t, st.AddMessageLabels(id, labels))
		}
	}
	_, err = buildCache(dbPath, cache, false)
	require.NoError(t, err)
	engine, err := query.NewDuckDBEngine(cache, dbPath, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, engine.Close()) }()
	daemon := api.NewServerWithOptions(api.ServerOptions{Config: &config.Config{Server: config.ServerConfig{APIKey: "synthetic-inbox-key"}}, Store: st, SavedViewStore: st, Engine: engine, Logger: slog.New(slog.DiscardHandler)})
	defer func() { require.NoError(t, daemon.Shutdown(context.Background())) }()
	endpoint := httptest.NewTLSServer(daemon.Router())
	defer endpoint.Close()
	client, err := daemonclient.New(daemonclient.Config{URL: endpoint.URL, APIKey: "synthetic-inbox-key", HTTPClient: endpoint.Client()})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())
	done := make(chan error, 1)
	go func() {
		done <- mcp.ServeHTTPWithOptions(ctx, mcp.ServeOptions{Engine: daemonclient.NewEngineAdapter(client), SavedViews: client}, mcp.HTTPOptions{Addr: addr, AllowWrites: true, APIKey: "synthetic-mcp-admin", Keys: []mcp.NamedKey{{Name: "viewer", Key: "synthetic-mcp-viewer", Role: authz.RoleViewer}}})
	}()
	defer func() { cancel(); require.ErrorIs(t, <-done, context.Canceled) }()
	require.Eventually(t, func() bool {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 10*time.Second, 10*time.Millisecond, "MCP listener never became available")
	credential := "synthetic-mcp-admin"
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args, "_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientInfo": map[string]any{"name": "inbox-integration", "version": "1"}, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}}})
		require.NoError(t, err)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/mcp", bytes.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", "tools/call")
		req.Header.Set("Mcp-Name", name)
		req.Header.Set("Authorization", "Bearer "+credential)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { require.NoError(t, resp.Body.Close()) }()
		var wire struct {
			Result map[string]any `json:"result"`
			Error  any            `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&wire))
		if wire.Error != nil {
			return map[string]any{"rpc_error": wire.Error}
		}
		require.Equal(t, http.StatusOK, resp.StatusCode)
		return wire.Result
	}
	filters := []any{map[string]any{"field": "source", "operator": "in", "values": sources}, map[string]any{"field": "inbox_state", "operator": "eq", "values": []string{"inbox"}}, map[string]any{"field": "read_state", "operator": "eq", "values": []string{"unread"}}}
	created := call(mcp.ToolCreateSavedView, map[string]any{"name": "[QA] Inbox Unread", "schema_version": 1, "canonical_state": map[string]any{"filters": filters, "presentation": "table"}})
	require.NotEqual(t, true, created["isError"], "%#v", created)
	definition, ok := created["structuredContent"].(map[string]any)
	require.True(t, ok)
	id, ok := definition["id"].(float64)
	require.True(t, ok)
	got := call(mcp.ToolGetSavedView, map[string]any{"id": id})
	require.NotEqual(t, true, got["isError"])
	run := func() {
		t.Helper()
		result := call(mcp.ToolRunSavedView, map[string]any{"id": id})
		require.NotEqual(t, true, result["isError"], "%#v", result)
		page, ok := result["structuredContent"].(map[string]any)
		require.True(t, ok)
		rows, ok := page["rows"].([]any)
		require.True(t, ok)
		require.Len(t, rows, 3)
		var ids []int64
		for _, value := range rows {
			row, ok := value.(map[string]any)
			require.True(t, ok)
			anchor, ok := row["anchor_message_id"].(float64)
			require.True(t, ok)
			ids = append(ids, int64(anchor))
			assert.Equal(t, "inbox", row["inbox_state"])
			assert.Equal(t, "unread", row["read_state"])
			assert.NotEmpty(t, row["source_identifier"])
			assert.NotZero(t, row["source_id"])
		}
		assert.ElementsMatch(t, wantIDs, ids)
	}
	run()
	credential = "synthetic-mcp-viewer"
	denied := call(mcp.ToolCreateSavedView, map[string]any{"name": "[QA] Denied", "schema_version": 1, "canonical_state": map[string]any{}})
	require.NotNil(t, denied["rpc_error"], "viewer unexpectedly created a view: %#v", denied)
	views, err := client.ListSavedViews(t.Context())
	require.NoError(t, err)
	require.Len(t, views, 1)
	credential = "synthetic-mcp-admin"
	invalid := call(mcp.ToolUpdateSavedView, map[string]any{"id": id, "revision": 1, "canonical_state": map[string]any{"filters": []any{map[string]any{"field": "participant", "operator": "eq", "values": []string{"participant@example.com"}}}}})
	require.Equal(t, true, invalid["isError"])
	stored, err := client.GetSavedView(t.Context(), int64(id))
	require.NoError(t, err)
	assert.Equal(t, int64(1), stored.Revision)
	run()
}
