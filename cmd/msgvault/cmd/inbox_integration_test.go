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
	assertions := assert.New(t)
	requirements := require.New(t)
	dir := t.TempDir()
	dbPath, cache := filepath.Join(dir, "mail.db"), filepath.Join(dir, "cache")
	st, err := store.Open(dbPath)
	requirements.NoError(err)
	defer func() { requirements.NoError(st.Close()) }()
	requirements.NoError(st.InitSchema())
	var sources []string
	var wantIDs []int64
	for account := range 3 {
		source, err := st.GetOrCreateSource("gmail", fmt.Sprintf("account-%d@example.com", account))
		requirements.NoError(err)
		sources = append(sources, strconv.FormatInt(source.ID, 10))
		conv, err := st.EnsureConversation(source.ID, "same-thread", "[QA] Independent copies")
		requirements.NoError(err)
		inbox, err := st.EnsureLabel(source.ID, "INBOX", "INBOX", "system")
		requirements.NoError(err)
		unread, err := st.EnsureLabel(source.ID, "UNREAD", "UNREAD", "system")
		requirements.NoError(err)
		for item := range 2 {
			id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conv, SourceMessageID: fmt.Sprintf("same-id-%d", item), MessageType: "email", SentAt: sql.NullTime{Time: time.Date(2026, 1, 1, 12, item, 0, 0, time.UTC), Valid: true}})
			requirements.NoError(err)
			labels := []int64{inbox}
			if item == 0 {
				labels = append(labels, unread)
				wantIDs = append(wantIDs, id)
			}
			requirements.NoError(st.AddMessageLabels(id, labels))
		}
	}
	_, err = buildCache(dbPath, cache, false)
	requirements.NoError(err)
	engine, err := query.NewDuckDBEngine(cache, dbPath, nil)
	requirements.NoError(err)
	defer func() { requirements.NoError(engine.Close()) }()
	daemon := api.NewServerWithOptions(api.ServerOptions{Config: &config.Config{Server: config.ServerConfig{APIKey: "synthetic-inbox-key"}}, Store: st, SavedViewStore: st, Engine: engine, Logger: slog.New(slog.DiscardHandler)})
	defer func() { requirements.NoError(daemon.Shutdown(context.Background())) }()
	endpoint := httptest.NewTLSServer(daemon.Router())
	defer endpoint.Close()
	client, err := daemonclient.New(daemonclient.Config{URL: endpoint.URL, APIKey: "synthetic-inbox-key", HTTPClient: endpoint.Client()})
	requirements.NoError(err)
	ctx, cancel := context.WithCancel(t.Context())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	requirements.NoError(err)
	addr := listener.Addr().String()
	requirements.NoError(listener.Close())
	done := make(chan error, 1)
	go func() {
		done <- mcp.ServeHTTPWithOptions(ctx, mcp.ServeOptions{Engine: daemonclient.NewEngineAdapter(client), SavedViews: client}, mcp.HTTPOptions{Addr: addr, AllowWrites: true, APIKey: "synthetic-mcp-admin", Keys: []mcp.NamedKey{{Name: "viewer", Key: "synthetic-mcp-viewer", Role: authz.RoleViewer}}})
	}()
	defer func() { cancel(); requirements.ErrorIs(<-done, context.Canceled) }()
	requirements.Eventually(func() bool {
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
		requirements.NoError(err)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/mcp", bytes.NewReader(payload))
		requirements.NoError(err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", "tools/call")
		req.Header.Set("Mcp-Name", name)
		req.Header.Set("Authorization", "Bearer "+credential)
		resp, err := http.DefaultClient.Do(req)
		requirements.NoError(err)
		defer func() { requirements.NoError(resp.Body.Close()) }()
		var wire struct {
			Result map[string]any `json:"result"`
			Error  any            `json:"error"`
		}
		requirements.NoError(json.NewDecoder(resp.Body).Decode(&wire))
		if wire.Error != nil {
			return map[string]any{"rpc_error": wire.Error}
		}
		requirements.Equal(http.StatusOK, resp.StatusCode)
		return wire.Result
	}
	filters := []any{map[string]any{"field": "source", "operator": "in", "values": sources}, map[string]any{"field": "inbox_state", "operator": "eq", "values": []string{"inbox"}}, map[string]any{"field": "read_state", "operator": "eq", "values": []string{"unread"}}}
	created := call(mcp.ToolCreateSavedView, map[string]any{"name": "[QA] Inbox Unread", "schema_version": 1, "canonical_state": map[string]any{"filters": filters, "presentation": "table"}})
	requirements.NotEqual(true, created["isError"], "%#v", created)
	definition, ok := created["structuredContent"].(map[string]any)
	requirements.True(ok)
	id, ok := definition["id"].(float64)
	requirements.True(ok)
	got := call(mcp.ToolGetSavedView, map[string]any{"id": id})
	requirements.NotEqual(true, got["isError"])
	run := func() {
		t.Helper()
		result := call(mcp.ToolRunSavedView, map[string]any{"id": id})
		requirements.NotEqual(true, result["isError"], "%#v", result)
		page, ok := result["structuredContent"].(map[string]any)
		requirements.True(ok)
		rows, ok := page["rows"].([]any)
		requirements.True(ok)
		requirements.Len(rows, 3)
		var ids []int64
		for _, value := range rows {
			row, ok := value.(map[string]any)
			requirements.True(ok)
			anchor, ok := row["anchor_message_id"].(float64)
			requirements.True(ok)
			ids = append(ids, int64(anchor))
			assertions.Equal("inbox", row["inbox_state"])
			assertions.Equal("unread", row["read_state"])
			assertions.NotEmpty(row["source_identifier"])
			assertions.NotZero(row["source_id"])
		}
		assertions.ElementsMatch(wantIDs, ids)
	}
	run()
	credential = "synthetic-mcp-viewer"
	denied := call(mcp.ToolCreateSavedView, map[string]any{"name": "[QA] Denied", "schema_version": 1, "canonical_state": map[string]any{}})
	requirements.NotNil(denied["rpc_error"], "viewer unexpectedly created a view: %#v", denied)
	views, err := client.ListSavedViews(t.Context())
	requirements.NoError(err)
	requirements.Len(views, 1)
	credential = "synthetic-mcp-admin"
	invalid := call(mcp.ToolUpdateSavedView, map[string]any{"id": id, "revision": 1, "canonical_state": map[string]any{"filters": []any{map[string]any{"field": "participant", "operator": "eq", "values": []string{"participant@example.com"}}}}})
	requirements.Equal(true, invalid["isError"])
	stored, err := client.GetSavedView(t.Context(), int64(id))
	requirements.NoError(err)
	assertions.Equal(int64(1), stored.Revision)
	run()
}
