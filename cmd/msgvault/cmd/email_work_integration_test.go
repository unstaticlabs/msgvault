package cmd

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
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
	"go.kenn.io/msgvault/internal/testutil"
)

// Native deletion and allocation reuse the physical ID, but an old member
// link must never expose the replacement message through either detail path.
func TestEmailWorkMemberBodyRejectsReusedNativeID(t *testing.T) {
	for _, withEngine := range []bool{false, true} {
		t.Run(strconv.FormatBool(withEngine), func(t *testing.T) {
			requirements := require.New(t)
			assertions := assert.New(t)
			st, err := store.Open(filepath.Join(t.TempDir(), "synthetic.db"))
			requirements.NoError(err)
			t.Cleanup(func() { requirements.NoError(st.Close()) })
			requirements.NoError(st.InitSchema())
			source, err := st.GetOrCreateSource("gmail", "qa-copy@example.test")
			requirements.NoError(err)
			conversation, err := st.EnsureConversation(source.ID, "qa-thread", "[QA]")
			requirements.NoError(err)
			message := &store.Message{SourceID: source.ID, ConversationID: conversation, SourceMessageID: "original", MessageType: "email", Subject: sql.NullString{String: "[QA] Original", Valid: true}}
			id, err := st.UpsertMessage(message)
			requirements.NoError(err)
			seed, err := st.EmailWorkSeedForMessage(context.Background(), id)
			requirements.NoError(err)
			var engine query.Engine
			if withEngine {
				engine = query.NewEngine(st.DB(), st.IsPostgreSQL())
			}
			daemon := api.NewServerWithOptions(api.ServerOptions{Config: &config.Config{Server: config.ServerConfig{APIKey: "synthetic-work-key"}}, Store: &storeAPIAdapter{store: st}, Engine: engine, Logger: slog.New(slog.DiscardHandler)})
			t.Cleanup(func() { requirements.NoError(daemon.Shutdown(context.Background())) })
			fetch := func(uid string) *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d?expected_message_uid=%s", id, uid), nil)
				request.Header.Set("X-Api-Key", "synthetic-work-key")
				response := httptest.NewRecorder()
				daemon.Router().ServeHTTP(response, request)
				return response
			}
			first := fetch(seed.UID)
			requirements.Equal(http.StatusOK, first.Code, first.Body.String())
			assertions.Contains(first.Body.String(), "[QA] Original")
			_, err = st.DB().Exec("DELETE FROM messages WHERE id=?", id)
			requirements.NoError(err)
			message.SourceMessageID = "replacement"
			message.Subject.String = "[QA] Unrelated replacement"
			replacement, err := st.UpsertMessage(message)
			requirements.NoError(err)
			requirements.Equal(id, replacement, "real SQLite maximum-ID reuse")
			current, err := st.EmailWorkSeedForMessage(context.Background(), id)
			requirements.NoError(err)
			requirements.NotEqual(seed.UID, current.UID)
			old := fetch(seed.UID)
			assertions.Equal(http.StatusConflict, old.Code, old.Body.String())
			assertions.NotContains(old.Body.String(), message.Subject.String)
			fresh := fetch(current.UID)
			assertions.Equal(http.StatusOK, fresh.Code, fresh.Body.String())
			assertions.Contains(fresh.Body.String(), message.Subject.String)
		})
	}
}

// This adapter uses the actual publisher in-process for the disposable fixture;
// production owns the same publisher in its resource-bounded cache subprocess.
type emailWorkHTTPNativeStore struct {
	*storeAPIAdapter

	dbPath    string
	cache     string
	beforeRaw func(int64)
}

func (s *emailWorkHTTPNativeStore) GetArchivedMessageRaw(ctx context.Context, id int64) ([]byte, error) {
	if s.beforeRaw != nil {
		s.beforeRaw(id)
	}
	return s.storeAPIAdapter.GetArchivedMessageRaw(ctx, id)
}

func (s *emailWorkHTTPNativeStore) RefreshIdentityDatasets(context.Context) (int64, error) {
	if _, err := buildCache(s.dbPath, s.cache, false); err != nil {
		return 0, err
	}
	state, err := query.ReadCacheSyncState(s.cache)
	if err != nil {
		return 0, err
	}
	return state.IdentityRevision, nil
}

func TestEmailWorkRecoveryThroughAuthenticatedNativeHTTP(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	ctx := context.Background()
	root := t.TempDir()
	dbPath, cache := filepath.Join(root, "synthetic.db"), filepath.Join(root, "cache")
	st, err := store.Open(dbPath)
	requirements.NoError(err)
	defer func() { requirements.NoError(st.Close()) }()
	requirements.NoError(st.InitSchema())
	var targets []store.EmailWorkActionTarget
	var sourceIDs []int64
	for i := range 2 {
		source, err := st.GetOrCreateSource("gmail", fmt.Sprintf("qa-%d@example.test", i))
		requirements.NoError(err)
		sourceIDs = append(sourceIDs, source.ID)
		conversation, err := st.EnsureConversation(source.ID, "qa-thread", "[QA]")
		requirements.NoError(err)
		id, err := st.PersistMessage(&store.MessagePersistData{Message: &store.Message{SourceID: source.ID, ConversationID: conversation, SourceMessageID: fmt.Sprintf("original-%d", i), MessageType: "email", SentAt: sql.NullTime{Time: time.Date(2026, 1, 1, 12, i, 0, 0, time.UTC), Valid: true}}, RawMIME: []byte(fmt.Sprintf("Message-ID: <original-%d@example.test>\r\n\r\n[QA]", i))})
		requirements.NoError(err)
		seed, err := st.EmailWorkSeedForMessage(ctx, id)
		requirements.NoError(err)
		targets = append(targets, store.EmailWorkActionTarget{UID: seed.UID, SourceID: source.ID})
	}
	_, err = buildCache(dbPath, cache, true)
	requirements.NoError(err)
	engine, err := query.NewDuckDBEngine(cache, dbPath, nil)
	requirements.NoError(err)
	defer func() { requirements.NoError(engine.Close()) }()
	native := &emailWorkHTTPNativeStore{storeAPIAdapter: &storeAPIAdapter{store: st, analyticsDir: cache}, dbPath: dbPath, cache: cache}
	user, err := st.RecordUserLogin(ctx, store.UserLogin{Issuer: "https://synthetic-idp.example.test", Subject: "synthetic-member", Email: "qa-member@example.test", DisplayName: "[QA] Member", Role: string(authz.RoleMember)})
	requirements.NoError(err)
	requirements.NoError(st.SetUserSources(ctx, user.ID, sourceIDs))
	cfg := &config.Config{Server: config.ServerConfig{APIKey: "synthetic-admin"}, Auth: config.AuthConfig{APIKeys: []config.APIKeyConfig{{Name: "qa-viewer", Key: "synthetic-viewer", Role: string(authz.RoleViewer), User: "qa-member@example.test"}, {Name: "qa-member", Key: "synthetic-member", Role: string(authz.RoleMember), User: "qa-member@example.test"}}}}
	daemon := api.NewServerWithOptions(api.ServerOptions{Config: cfg, Store: native, SavedViewStore: st, UserStore: st, Engine: engine, Logger: slog.New(slog.DiscardHandler)})
	defer func() { requirements.NoError(daemon.Shutdown(ctx)) }()
	post := func(path, key string, body any) *httptest.ResponseRecorder {
		payload, err := json.Marshal(body)
		requirements.NoError(err)
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		request.Header.Set("X-Api-Key", key)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		daemon.Router().ServeHTTP(response, request)
		return response
	}
	work := query.ExploreRequest{Context: query.Context{RowUnit: query.RowUnitEmailWork, WorkAuthorityKey: "synthetic-native-fixture"}}
	rows, err := engine.Explore(ctx, work)
	requirements.NoError(err)
	input := api.EmailWorkActionRequest{Kind: "link", Targets: targets, CacheRevision: rows.CacheRevision}
	for _, role := range []string{"synthetic-viewer", ""} {
		denied := post("/api/v1/explore/email-work/preview", role, input)
		if role == "" {
			assertions.Equal(http.StatusUnauthorized, denied.Code)
		} else {
			assertions.Equal(http.StatusForbidden, denied.Code)
		}
	}
	previewResponse := post("/api/v1/explore/email-work/preview", "synthetic-member", input)
	requirements.Equal(http.StatusOK, previewResponse.Code, previewResponse.Body.String())
	var preview api.EmailWorkActionPreview
	requirements.NoError(json.Unmarshal(previewResponse.Body.Bytes(), &preview))
	requirements.Len(preview.Messages, 2)
	requirements.Len(preview.UID, 32)
	requirements.Len(preview.Snapshot, 64)
	restricted := input
	restricted.Predicate.Filters = []api.ExploreFilter{{Dimension: "source", Values: []string{strconv.FormatInt(sourceIDs[0], 10)}}}
	unavailable := post("/api/v1/explore/email-work/preview", "synthetic-member", restricted)
	assertions.Equal(http.StatusNotFound, unavailable.Code, unavailable.Body.String())
	applied := post("/api/v1/explore/email-work/actions", "synthetic-member", preview.EmailWorkActionRequest)
	requirements.Equal(http.StatusOK, applied.Code, applied.Body.String())
	var mutation api.EmailWorkActionResponse
	requirements.NoError(json.Unmarshal(applied.Body.Bytes(), &mutation))
	requirements.NotNil(mutation.Action)
	assertions.True(mutation.Action.Active)
	assertions.Equal("ready", mutation.CacheState)
	grouped, err := engine.Explore(ctx, work)
	requirements.NoError(err)
	assertions.Equal(int64(1), grouped.TotalCount)
	exportRequest := api.EmailWorkExportRequest{UID: targets[0].UID, CacheRevision: grouped.CacheRevision}
	readExport := func(input api.EmailWorkExportRequest) (api.EmailWorkExportManifest, map[string][]byte) {
		response := post("/api/v1/explore/email-work/export", "synthetic-viewer", input)
		requirements.Equal(http.StatusOK, response.Code, response.Body.String())
		assertions.Equal("application/zip", response.Header().Get("Content-Type"))
		archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
		requirements.NoError(err)
		contents := map[string][]byte{}
		for _, entry := range archive.File {
			stream, err := entry.Open()
			requirements.NoError(err)
			content, err := io.ReadAll(stream)
			requirements.NoError(err)
			requirements.NoError(stream.Close())
			assertions.NotContains(entry.Name, "@", "filenames use opaque identity")
			requirements.NotContains(contents, entry.Name, "each copy is exported once")
			contents[entry.Name] = content
		}
		var manifest api.EmailWorkExportManifest
		requirements.NoError(json.Unmarshal(contents["manifest.json"], &manifest))
		assertions.Equal(manifest.State, response.Header().Get("X-Email-Work-Export-State"))
		return manifest, contents
	}
	exported, files := readExport(exportRequest)
	assertions.Equal("complete", exported.State)
	requirements.Len(exported.Copies, 2)
	assertions.Len(files, 3, "two distinct source copies and one manifest")
	for _, copyEntry := range exported.Copies {
		assertions.Equal("available", copyEntry.State)
		expected, err := st.GetMessageRaw(copyEntry.Member.MessageID)
		requirements.NoError(err)
		assertions.Equal(expected, files[copyEntry.Filename], "raw MIME bytes and provenance remain paired")
	}
	firstCopy := exported.Copies[0]
	originalRaw, err := st.GetMessageRaw(firstCopy.Member.MessageID)
	requirements.NoError(err)
	// Replace an already-read copy with identical linkage headers while the
	// second copy is read. Analytical publication remains unchanged.
	beforeDerived, err := st.DerivedDataRevision()
	requirements.NoError(err)
	rawReads := 0
	native.beforeRaw = func(int64) {
		rawReads++
		if rawReads == 2 {
			requirements.NoError(st.UpsertMessageRaw(firstCopy.Member.MessageID, append(bytes.Clone(originalRaw), []byte("\r\n[QA] Concurrent body replacement")...)))
		}
	}
	changedRaw := post("/api/v1/explore/email-work/export", "synthetic-viewer", exportRequest)
	assertions.Equal(http.StatusConflict, changedRaw.Code, "raw-only mutation must stop ZIP publication")
	assertions.NotEqual("application/zip", changedRaw.Header().Get("Content-Type"))
	native.beforeRaw = nil
	afterDerived, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(beforeDerived, afterDerived, "body replacement does not invalidate analytical linkage")
	requirements.NoError(st.UpsertMessageRaw(firstCopy.Member.MessageID, originalRaw))
	retryManifest, _ := readExport(exportRequest)
	assertions.Equal("complete", retryManifest.State, "unchanged raw retries remain exportable")
	_, err = st.DB().Exec("DELETE FROM message_raw WHERE message_id=?", firstCopy.Member.MessageID)
	requirements.NoError(err)
	partial, partialFiles := readExport(exportRequest)
	assertions.Equal("partial", partial.State)
	requirements.Len(partial.Copies, 2, "missing raw never removes provenance")
	assertions.Len(partialFiles, 2, "missing raw is not fabricated")
	assertions.Equal("raw_unavailable", partial.Copies[0].State)
	requirements.NoError(st.UpsertMessageRaw(firstCopy.Member.MessageID, originalRaw))
	scopedExport := exportRequest
	scopedExport.Predicate.Filters = []api.ExploreFilter{{Dimension: "source", Values: []string{strconv.FormatInt(sourceIDs[0], 10)}}}
	scopedManifest, _ := readExport(scopedExport)
	requirements.Len(scopedManifest.Copies, 1)
	assertions.Equal(sourceIDs[0], scopedManifest.Copies[0].Member.SourceID)
	scopedExport.UID = targets[1].UID
	outside := post("/api/v1/explore/email-work/export", "synthetic-viewer", scopedExport)
	assertions.Equal(http.StatusNotFound, outside.Code, outside.Body.String())
	staleExport := exportRequest
	staleExport.CacheRevision = "old"
	staleDownload := post("/api/v1/explore/email-work/export", "synthetic-viewer", staleExport)
	assertions.Equal(http.StatusConflict, staleDownload.Code, staleDownload.Body.String())
	missingRevision := exportRequest
	missingRevision.CacheRevision = ""
	invalidDownload := post("/api/v1/explore/email-work/export", "synthetic-viewer", missingRevision)
	assertions.Equal(http.StatusBadRequest, invalidDownload.Code, invalidDownload.Body.String())

	retry := post("/api/v1/explore/email-work/actions", "synthetic-member", preview.EmailWorkActionRequest)
	requirements.Equal(http.StatusOK, retry.Code, retry.Body.String())
	var retried api.EmailWorkActionResponse
	requirements.NoError(json.Unmarshal(retry.Body.Bytes(), &retried))
	assertions.Equal(mutation.Action, retried.Action, "lost-response retry must preserve the exact journal record")
	details, err := engine.EmailWorkMembers(ctx, query.EmailWorkMembersRequest{Explore: work, UID: targets[0].UID})
	requirements.NoError(err)
	undo := api.EmailWorkUndoRequest{UID: mutation.Action.UID, ExpectedRevision: mutation.Action.Revision, Snapshot: details.RecoverySnapshot, CacheRevision: details.CacheRevision}
	undoneResponse := post("/api/v1/explore/email-work/undo", "synthetic-member", undo)
	requirements.Equal(http.StatusOK, undoneResponse.Code, undoneResponse.Body.String())
	var undone api.EmailWorkActionResponse
	requirements.NoError(json.Unmarshal(undoneResponse.Body.Bytes(), &undone))
	assertions.False(undone.Action.Active)
	assertions.Equal(int64(2), undone.Action.Revision)
	split, err := engine.Explore(ctx, work)
	requirements.NoError(err)
	assertions.Equal(int64(2), split.TotalCount)
	repeatUndo := post("/api/v1/explore/email-work/undo", "synthetic-member", undo)
	requirements.Equal(http.StatusOK, repeatUndo.Code, repeatUndo.Body.String())
	repeatApply := post("/api/v1/explore/email-work/actions", "synthetic-member", preview.EmailWorkActionRequest)
	requirements.Equal(http.StatusOK, repeatApply.Code, repeatApply.Body.String())
	var stillUndone api.EmailWorkActionResponse
	requirements.NoError(json.Unmarshal(repeatApply.Body.Bytes(), &stillUndone))
	assertions.Equal(undone.Action, stillUndone.Action, "an old creation retry cannot resurrect undo")
	// Public MCP -> authenticated TLS daemon -> durable Saved View -> native
	// curation/publisher/Explore. No service or query result is substituted.
	call := nativeEmailWorkMCPCaller(t, daemon)
	const memberKey = "synthetic-mcp-member"
	created := call(mcp.ToolCreateSavedView, memberKey, map[string]any{"name": "[QA] Related email", "schema_version": 1, "canonical_state": map[string]any{"filters": []any{map[string]any{"field": "source", "operator": "in", "values": []string{strconv.FormatInt(sourceIDs[0], 10), strconv.FormatInt(sourceIDs[1], 10)}}, map[string]any{"field": "row_unit", "operator": "eq", "values": []string{"email_work_item"}}}, "presentation": "table"}})
	requirements.NotEqual(true, created["isError"], created)
	definition, ok := created["structuredContent"].(map[string]any)
	requirements.True(ok, created)
	viewID, viewRevision := definition["id"], definition["revision"]
	run := call(mcp.ToolRunSavedView, memberKey, map[string]any{"id": viewID})
	requirements.NotEqual(true, run["isError"], run)
	page, ok := run["structuredContent"].(map[string]any)
	requirements.True(ok, run)
	assertions.InDelta(2, page["total_count"], 0)
	previewArgs := map[string]any{"saved_view_id": viewID, "saved_view_revision": viewRevision, "cache_revision": page["cache_revision"], "kind": "link", "message_uids": []string{targets[0].UID, targets[1].UID}}
	deniedTool := call(mcp.ToolPreviewEmailWorkAction, "synthetic-mcp-viewer", previewArgs)
	assertions.True(deniedTool["rpc_error"] != nil || deniedTool["isError"] == true, "viewer cannot invoke local recovery", deniedTool)
	reviewed := call(mcp.ToolPreviewEmailWorkAction, memberKey, previewArgs)
	requirements.NotEqual(true, reviewed["isError"], reviewed)
	plan, ok := reviewed["structuredContent"].(map[string]any)
	requirements.True(ok, reviewed)
	appliedTool := call(mcp.ToolApplyEmailWorkAction, memberKey, map[string]any{"plan": plan})
	requirements.NotEqual(true, appliedTool["isError"], appliedTool)
	linkedPage := call(mcp.ToolRunSavedView, memberKey, map[string]any{"id": viewID})
	requirements.NotEqual(true, linkedPage["isError"], linkedPage)
	linkedResult, ok := linkedPage["structuredContent"].(map[string]any)
	requirements.True(ok, linkedPage)
	assertions.InDelta(1, linkedResult["total_count"], 0)
	detailArgs := map[string]any{"saved_view_id": viewID, "saved_view_revision": viewRevision, "work_uid": targets[0].UID, "cache_revision": linkedResult["cache_revision"]}
	read := call(mcp.ToolGetEmailWorkItem, memberKey, detailArgs)
	requirements.NotEqual(true, read["isError"], read)
	family, ok := read["structuredContent"].(map[string]any)
	requirements.True(ok, read)
	assertions.InDelta(2, family["total_count"], 0)
	assertions.Len(family["members"], 2)
	previewArgs["kind"] = "handled"
	previewArgs["cache_revision"] = linkedResult["cache_revision"]
	handledPreview := call(mcp.ToolPreviewEmailWorkAction, memberKey, previewArgs)
	requirements.NotEqual(true, handledPreview["isError"], handledPreview)
	handledPlan, ok := handledPreview["structuredContent"].(map[string]any)
	requirements.True(ok, handledPreview)
	confirmed := call(mcp.ToolApplyEmailWorkAction, memberKey, map[string]any{"plan": handledPlan})
	requirements.NotEqual(true, confirmed["isError"], confirmed)
	completedPage := call(mcp.ToolRunSavedView, memberKey, map[string]any{"id": viewID})
	requirements.NotEqual(true, completedPage["isError"], completedPage)
	completed, ok := completedPage["structuredContent"].(map[string]any)
	requirements.True(ok, completedPage)
	completedRows, ok := completed["rows"].([]any)
	requirements.True(ok, completed)
	requirements.Len(completedRows, 1)
	completedRow, ok := completedRows[0].(map[string]any)
	requirements.True(ok, completedRows)
	summary, ok := completedRow["email_work"].(map[string]any)
	requirements.True(ok, completedRow)
	assertions.Equal("handled", summary["state"])
	assertions.Contains(summary["reasons"], "local_confirmation")
	assertions.Contains(summary["reasons"], "identity_unavailable", "local confirmation must keep automatic uncertainty explicit")
	detailArgs["cache_revision"] = completed["cache_revision"]
	currentRead := call(mcp.ToolGetEmailWorkItem, memberKey, detailArgs)
	requirements.NotEqual(true, currentRead["isError"], currentRead)
	currentFamily, ok := currentRead["structuredContent"].(map[string]any)
	requirements.True(ok, currentRead)
	assertions.Empty(currentFamily["evidence_message_ids"], "local confirmation must not become an outbound reply")
	confirmedResult, ok := confirmed["structuredContent"].(map[string]any)
	requirements.True(ok, confirmed)
	confirmedAction, ok := confirmedResult["action"].(map[string]any)
	requirements.True(ok, confirmedResult)
	undoneTool := call(mcp.ToolUndoEmailWorkAction, memberKey, map[string]any{"saved_view_id": viewID, "saved_view_revision": viewRevision, "cache_revision": completed["cache_revision"], "snapshot": currentFamily["recovery_snapshot"], "action_uid": confirmedAction["uid"], "action_revision": confirmedAction["revision"]})
	requirements.NotEqual(true, undoneTool["isError"], undoneTool)
	reopened := call(mcp.ToolRunSavedView, memberKey, map[string]any{"id": viewID})
	requirements.NotEqual(true, reopened["isError"], reopened)
	reopenedPage, ok := reopened["structuredContent"].(map[string]any)
	requirements.True(ok, reopened)
	reopenedRows, ok := reopenedPage["rows"].([]any)
	requirements.True(ok, reopenedPage)
	requirements.Len(reopenedRows, 1)
	reopenedRow, ok := reopenedRows[0].(map[string]any)
	requirements.True(ok, reopenedRows)
	reopenedSummary, ok := reopenedRow["email_work"].(map[string]any)
	requirements.True(ok, reopenedRow)
	assertions.Equal("unknown", reopenedSummary["state"], "undo preserves the underlying uncertainty instead of inventing a reply")
	var lastID int64
	requirements.NoError(st.DB().QueryRow("SELECT MAX(id) FROM messages").Scan(&lastID))
	oldSeed, err := st.EmailWorkSeedForMessage(ctx, lastID)
	requirements.NoError(err)
	var replacementConversation int64
	requirements.NoError(st.DB().QueryRow("SELECT conversation_id FROM messages WHERE id=?", lastID).Scan(&replacementConversation))
	native.beforeRaw = func(id int64) {
		if id != lastID {
			return
		}
		native.beforeRaw = nil
		_, err := st.DB().Exec("DELETE FROM messages WHERE id=?", id)
		requirements.NoError(err)
		replacement, err := st.PersistMessage(&store.MessagePersistData{Message: &store.Message{SourceID: sourceIDs[1], SourceMessageID: "replacement-export", ConversationID: replacementConversation, MessageType: "email"}, RawMIME: []byte("Message-ID: <unrelated-export@example.test>\r\n\r\n[QA] Replacement must not leak")})
		requirements.NoError(err)
		requirements.Equal(id, replacement, "real maximum physical ID reuse")
		fresh, err := st.EmailWorkSeedForMessage(ctx, replacement)
		requirements.NoError(err)
		requirements.NotEqual(oldSeed.UID, fresh.UID)
	}
	currentExportRevision, ok := reopenedPage["cache_revision"].(string)
	requirements.True(ok)
	replacedExport := post("/api/v1/explore/email-work/export", "synthetic-viewer", api.EmailWorkExportRequest{UID: targets[0].UID, CacheRevision: currentExportRevision})
	assertions.Equal(http.StatusConflict, replacedExport.Code, replacedExport.Body.String())
	assertions.NotContains(replacedExport.Body.String(), "Replacement must not leak")
	assertions.NotEqual("application/zip", replacedExport.Header().Get("Content-Type"), "no bytes published before identity verification")
}

func nativeEmailWorkMCPCaller(t *testing.T, daemon *api.Server) func(string, string, map[string]any) map[string]any {
	t.Helper()
	requirements := require.New(t)
	endpoint := httptest.NewTLSServer(daemon.Router())
	t.Cleanup(endpoint.Close)
	client, err := daemonclient.New(daemonclient.Config{URL: endpoint.URL, APIKey: "synthetic-member", HTTPClient: endpoint.Client()})
	requirements.NoError(err)
	ctx, cancel := context.WithCancel(t.Context())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	requirements.NoError(err)
	addr := listener.Addr().String()
	requirements.NoError(listener.Close())
	done := make(chan error, 1)
	go func() {
		done <- mcp.ServeHTTPWithOptions(ctx, mcp.ServeOptions{Engine: daemonclient.NewEngineAdapter(client), SavedViews: client}, mcp.HTTPOptions{Addr: addr, AllowWrites: true, APIKey: "synthetic-mcp-admin", Keys: []mcp.NamedKey{{Name: "member", Key: "synthetic-mcp-member", Role: authz.RoleMember}, {Name: "viewer", Key: "synthetic-mcp-viewer", Role: authz.RoleViewer}}})
	}()
	t.Cleanup(func() { cancel(); requirements.ErrorIs(<-done, context.Canceled) })
	requirements.Eventually(func() bool {
		connection, err := net.Dial("tcp", addr)
		if err != nil {
			return false
		}
		_ = connection.Close()
		return true
	}, 10*time.Second, 10*time.Millisecond, "MCP listener must reach its observable ready state")
	return func(name, key string, args map[string]any) map[string]any {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args, "_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientInfo": map[string]any{"name": "synthetic-work-recovery", "version": "1"}, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}}})
		requirements.NoError(err)
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/mcp", bytes.NewReader(payload))
		requirements.NoError(err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		request.Header.Set("Mcp-Method", "tools/call")
		request.Header.Set("Mcp-Name", name)
		request.Header.Set("Authorization", "Bearer "+key)
		response, err := http.DefaultClient.Do(request)
		requirements.NoError(err)
		defer func() { requirements.NoError(response.Body.Close()) }()
		var wire struct {
			Result map[string]any `json:"result"`
			Error  any            `json:"error"`
		}
		requirements.NoError(json.NewDecoder(response.Body).Decode(&wire))
		if wire.Error != nil {
			return map[string]any{"rpc_error": wire.Error}
		}
		requirements.Equal(http.StatusOK, response.StatusCode)
		return wire.Result
	}
}

// Actual native preparation/publication/member/ZIP paths preserve signed
// historical physical IDs, their opaque aliases and every archived MIME copy.
func TestEmailWorkLegacySignedIDsThroughNativeExport(t *testing.T) {
	for _, mode := range []string{"native", "csv"} {
		t.Run(mode, func(t *testing.T) {
			r := require.New(t)
			a := assert.New(t)
			ctx := context.Background()
			if mode == "csv" {
				t.Setenv("MSGVAULT_FORCE_CSV_SNAPSHOT", "1")
			}
			root := t.TempDir()
			cache := filepath.Join(root, "cache")
			st, dbPath := testutil.NewLegacyNullableSQLiteEmailStore(t)
			source, err := st.GetOrCreateSource("gmail", "qa-legacy-export@example.test")
			r.NoError(err)
			conv, err := st.EnsureConversation(source.ID, "legacy-thread", "[QA] Legacy copies")
			r.NoError(err)
			for _, id := range []int64{-7, 0, 10} {
				_, err = st.DB().Exec(`INSERT INTO messages(id,source_id,conversation_id,source_message_id,message_type,sent_at) VALUES (?,?,?,?,NULL,?)`, id, source.ID, conv, fmt.Sprintf("legacy-%d", id), "2026-01-01T12:00:00Z")
				r.NoError(err)
				_, err = st.DB().Exec(`INSERT INTO message_raw(message_id,raw_data,raw_format,compression) VALUES (?,?,'mime',NULL)`, id, []byte(fmt.Sprintf("Message-ID: <legacy-%d@example.test>\r\nIn-Reply-To: <legacy-root@example.test>\r\n\r\n[QA] copy %d", id, id)))
				r.NoError(err)
			}
			prepared, err := st.BackfillEmailLinkage(ctx, source.ID, 10)
			r.NoError(err)
			r.Equal(3, prepared.Processed)
			r.True(prepared.Done)
			seed, err := st.EmailWorkSeedForMessage(ctx, 0)
			r.NoError(err)
			_, err = buildCache(dbPath, cache, true)
			r.NoError(err)
			engine, err := query.NewDuckDBEngine(cache, dbPath, st.DB())
			r.NoError(err)
			defer func() { r.NoError(engine.Close()) }()
			work := query.ExploreRequest{Context: query.Context{RowUnit: query.RowUnitEmailWork, WorkAuthorityKey: "synthetic-legacy"}}
			rows, err := engine.Explore(ctx, work)
			r.NoError(err)
			r.Equal(int64(1), rows.TotalCount)
			r.Len(rows.Rows, 1)
			a.Equal(int64(3), rows.Rows[0].MessageCount)
			members, err := engine.EmailWorkMembers(ctx, query.EmailWorkMembersRequest{Explore: work, UID: seed.UID})
			r.NoError(err)
			r.Len(members.Members, 3)
			ids := []int64{}
			for _, member := range members.Members {
				ids = append(ids, member.MessageID)
			}
			a.ElementsMatch([]int64{-7, 0, 10}, ids)
			native := &emailWorkHTTPNativeStore{storeAPIAdapter: &storeAPIAdapter{store: st, analyticsDir: cache}, dbPath: dbPath, cache: cache}
			daemon := api.NewServerWithOptions(api.ServerOptions{Config: &config.Config{Server: config.ServerConfig{APIKey: "synthetic-legacy"}}, Store: native, Engine: engine, Logger: slog.New(slog.DiscardHandler)})
			defer func() { r.NoError(daemon.Shutdown(ctx)) }()
			payload, err := json.Marshal(api.EmailWorkExportRequest{UID: seed.UID, CacheRevision: members.CacheRevision})
			r.NoError(err)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/explore/email-work/export", bytes.NewReader(payload))
			request.Header.Set("X-Api-Key", "synthetic-legacy")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			daemon.Router().ServeHTTP(response, request)
			r.Equal(http.StatusOK, response.Code, response.Body.String())
			archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
			r.NoError(err)
			files := map[string][]byte{}
			for _, entry := range archive.File {
				stream, err := entry.Open()
				r.NoError(err)
				content, err := io.ReadAll(stream)
				r.NoError(err)
				r.NoError(stream.Close())
				r.NotContains(files, entry.Name)
				files[entry.Name] = content
			}
			var manifest api.EmailWorkExportManifest
			r.NoError(json.Unmarshal(files["manifest.json"], &manifest))
			a.Equal("complete", manifest.State)
			r.Len(manifest.Copies, 3)
			a.Len(files, 4)
			exported := []int64{}
			for _, copy := range manifest.Copies {
				exported = append(exported, copy.Member.MessageID)
				raw, err := st.GetMessageRaw(copy.Member.MessageID)
				r.NoError(err)
				a.Equal(raw, files[copy.Filename])
			}
			a.ElementsMatch([]int64{-7, 0, 10}, exported)
			_, err = st.DB().Exec(`DELETE FROM messages WHERE id=0`)
			r.NoError(err)
			_, err = engine.EmailWorkMembers(ctx, query.EmailWorkMembersRequest{Explore: work, UID: seed.UID})
			r.ErrorIs(err, store.ErrEmailWorkAliasGone)
			_, err = st.DB().Exec(`INSERT INTO messages(id,source_id,conversation_id,source_message_id,message_type,sent_at) VALUES (0,?,?,?,'email',?)`, source.ID, conv, "replacement-zero", "2026-01-01T12:00:00Z")
			r.NoError(err)
			r.NoError(st.UpsertMessageRaw(0, []byte("Message-ID: <replacement@example.test>\r\n\r\n[QA] Replacement")))
			fresh, err := st.EmailWorkSeedForMessage(ctx, 0)
			r.NoError(err)
			a.NotEqual(seed.UID, fresh.UID)
			_, err = engine.EmailWorkMembers(ctx, query.EmailWorkMembersRequest{Explore: work, UID: seed.UID})
			r.ErrorIs(err, store.ErrEmailWorkAliasGone)
		})
	}
}
