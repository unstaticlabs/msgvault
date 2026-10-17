package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/daemonclient"
	"go.kenn.io/msgvault/internal/inboxarchive"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

// The provider is the only fake: real MCP selection, daemon grants, HTTP
// transport, archive execution and SQL label convergence run below it.
// Failed writes model a provider refusal, not a new mailbox implementation.
type exactArchiveProvider struct {
	inbox map[string]bool
	fail  string
	calls [][]string
}

func (p *exactArchiveProvider) ArchiveFromInbox(_ context.Context, ids []string) (map[string]error, error) {
	p.calls = append(p.calls, slices.Clone(ids))
	failed := make(map[string]error)
	for _, id := range ids {
		if id == p.fail {
			failed[id] = errors.New("synthetic provider refusal")
			continue
		}
		p.inbox[id] = false
	}
	return failed, nil
}

type exactArchiveRunner struct {
	store    *store.Store
	provider *exactArchiveProvider
}

func (r exactArchiveRunner) RunInboxArchive(ctx context.Context, req api.InboxArchiveRunRequest) (api.InboxArchiveRunResult, error) {
	source, err := r.store.GetSourceByIDContext(ctx, req.SourceID)
	if err != nil {
		return api.InboxArchiveRunResult{}, err
	}
	result, err := inboxarchive.New(r.provider, r.store).Archive(ctx, source, req.SourceMessageIDs)
	return api.InboxArchiveRunResult{Archived: result.Archived, Failed: result.Failed, Remaining: result.Remaining, FailedIDs: result.FailedIDs}, err
}

// The bridge only converts public daemon-client types to the tool interface.
// It does not resolve selection, authorize a grant, or perform archive work.
type exactArchiveBridge struct{ client *daemonclient.Client }

func (b exactArchiveBridge) AuthorizeInboxArchive(ctx context.Context, req InboxArchiveAuthorizeRequest) (string, error) {
	token, err := b.client.AuthorizeInboxArchive(ctx, daemonclient.InboxArchiveAuthorizeRequest{Account: req.Account, SourceID: req.SourceID, Description: req.Description, SourceMessageIDs: req.SourceMessageIDs})
	var apiErr *daemonclient.APIError
	if errors.As(err, &apiErr) && apiErr.APIErrorCode() == "mailbox_writes_disabled" {
		return "", ErrMailboxWritesDisabled
	}
	return token, err
}
func (b exactArchiveBridge) ExecuteInboxArchive(ctx context.Context, req InboxArchiveExecuteRequest) (InboxArchiveResult, error) {
	result, err := b.client.ExecuteInboxArchive(ctx, daemonclient.InboxArchiveExecuteRequest{ConfirmationToken: req.ConfirmationToken, SourceMessageIDs: req.SourceMessageIDs})
	return InboxArchiveResult{BatchID: result.BatchID, Account: result.Account, Archived: result.Archived, Failed: result.Failed, Remaining: result.Remaining, FailedIDs: result.FailedIDs, PartialFailure: result.PartialFailure}, err
}

func TestArchiveExactIDsThroughMCPAndDaemon(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gmail", "qa-inbox@example.com")
	require.NoError(err)
	conversation, err := st.EnsureConversation(source.ID, "qa-thread", "[QA] Handled messages")
	require.NoError(err)
	inbox, err := st.EnsureLabel(source.ID, "INBOX", "INBOX", "system")
	require.NoError(err)
	keep, err := st.EnsureLabel(source.ID, "keep", "Keep", "user")
	require.NoError(err)
	ids := make([]int64, 3)
	for i, name := range []string{"handled-a", "handled-b", "leave-alone"} {
		ids[i], err = st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conversation, SourceMessageID: name, MessageType: "email", SizeEstimate: 100})
		require.NoError(err)
		require.NoError(st.AddMessageLabels(ids[i], []int64{inbox, keep}))
		require.NoError(st.UpsertMessageBody(ids[i], sql.NullString{String: "[QA] Retained message body", Valid: true}, sql.NullString{}))
		require.NoError(st.UpsertAttachment(ids[i], "qa-retained.txt", "text/plain", "qa-retained", "qa-hash", 10))
	}
	provider := &exactArchiveProvider{inbox: map[string]bool{"handled-a": true, "handled-b": true, "leave-alone": true}, fail: "handled-b"}
	engine := query.NewEngine(st.DB(), st.IsPostgreSQL())
	cfg := &config.Config{InboxArchive: config.InboxArchiveConfig{RemoteEnabled: true}}
	daemon := api.NewServerWithOptions(api.ServerOptions{Config: cfg, Store: st, Engine: engine, OperationGate: api.NewSerialOperationGate(), InboxArchive: exactArchiveRunner{st, provider}, Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		assert.NoError(daemon.Shutdown(ctx))
	})
	server := httptest.NewTLSServer(daemon.Router())
	t.Cleanup(server.Close)
	client, err := daemonclient.New(daemonclient.Config{URL: server.URL, HTTPClient: server.Client()})
	require.NoError(err)
	// Remote metadata lookups run through the daemon, with the same visibility
	// boundary used by ordinary MCP message reads.
	remote := daemonclient.NewEngineAdapter(client)
	opts := ServeOptions{Engine: remote, AllowMailboxWrites: true, InboxArchiver: exactArchiveBridge{client}}
	session := task5ConnectClient(t, opts, true)
	call := func(args map[string]any) *sdkmcp.CallToolResult {
		result, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: ToolArchiveFromInbox, Arguments: args})
		require.NoError(err)
		return result
	}
	exact := map[string]any{"message_ids": []int64{ids[0], ids[1]}, "confirm": true}
	result := call(exact)
	require.False(result.IsError, "%v", result.Content)
	encoded, err := json.Marshal(result.StructuredContent)
	require.NoError(err)
	var outcome inboxArchiveExecuteResponse
	require.NoError(json.Unmarshal(encoded, &outcome))
	assert.Equal(1, outcome.Archived)
	assert.Equal(1, outcome.Failed)
	assert.Equal([]string{"handled-b"}, outcome.FailedIDs)
	assert.Contains(outcome.NextStep, "failed")
	assert.False(provider.inbox["handled-a"])
	assert.True(provider.inbox["handled-b"])
	assert.True(provider.inbox["leave-alone"])
	assert.ElementsMatch([]string{"Keep"}, exactArchiveLabels(t, st, ids[0]))
	assert.ElementsMatch([]string{"INBOX", "Keep"}, exactArchiveLabels(t, st, ids[1]))
	assert.ElementsMatch([]string{"INBOX", "Keep"}, exactArchiveLabels(t, st, ids[2]))
	// Exact retries can include a previously successful message: the provider
	// archive operation is idempotent and the unselected message stays untouched.
	provider.fail = ""
	result = call(exact)
	require.False(result.IsError)
	assert.False(provider.inbox["handled-b"])
	assert.True(provider.inbox["leave-alone"])
	assert.ElementsMatch([]string{"Keep"}, exactArchiveLabels(t, st, ids[1]))
	var messages int
	require.NoError(st.DB().QueryRow("SELECT COUNT(*) FROM messages").Scan(&messages))
	assert.Equal(3, messages)
	var attachments int
	require.NoError(st.DB().QueryRow("SELECT COUNT(*) FROM attachments").Scan(&attachments))
	assert.Equal(3, attachments)
	for _, id := range ids {
		body, err := st.GetMessageBodyText(id)
		require.NoError(err)
		assert.Equal("[QA] Retained message body", body)
	}
	require.Len(provider.calls, 2)
	for _, batch := range provider.calls {
		assert.Equal([]string{"handled-a", "handled-b"}, batch)
	}
	// Missing IDs must not silently archive the surviving portion.
	result = call(map[string]any{"message_ids": []int64{ids[2], 999999}, "confirm": true})
	assert.True(result.IsError)
	assert.Len(provider.calls, 2)
	// The daemon's existing mailbox opt-in still applies. No new authorization
	// or per-run human confirmation is introduced for exact-ID calls.
	cfg.InboxArchive.RemoteEnabled = false
	result = call(map[string]any{"message_ids": []int64{ids[2]}, "confirm": true})
	assert.True(result.IsError)
	assert.Len(provider.calls, 2)
	assert.True(provider.inbox["leave-alone"])

	// A real source-scoped engine must not turn an inaccessible exact ID into
	// an action on the accessible remainder of a mixed selection.
	cfg.InboxArchive.RemoteEnabled = true
	hiddenSource, err := st.GetOrCreateSource("gmail", "qa-hidden@example.com")
	require.NoError(err)
	hiddenConversation, err := st.EnsureConversation(hiddenSource.ID, "hidden", "[QA] Another source")
	require.NoError(err)
	hiddenID, err := st.UpsertMessage(&store.Message{SourceID: hiddenSource.ID, ConversationID: hiddenConversation, SourceMessageID: "hidden-message", MessageType: "email"})
	require.NoError(err)
	opts.Engine = query.NewScopedEngine(engine, []int64{source.ID})
	scopedSession := task5ConnectClient(t, opts, true)
	scopedResult, err := scopedSession.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: ToolArchiveFromInbox, Arguments: map[string]any{"message_ids": []int64{ids[2], hiddenID}, "confirm": true}})
	require.NoError(err)
	require.True(scopedResult.IsError)
	assert.Contains(task5SDKResultText(scopedResult), "unavailable")
	assert.Len(provider.calls, 2)
	assert.True(provider.inbox["leave-alone"])
}

func exactArchiveLabels(t *testing.T, st *store.Store, id int64) []string {
	t.Helper()
	rows, err := st.DB().Query(st.Rebind("SELECT l.name FROM labels l JOIN message_labels ml ON ml.label_id=l.id WHERE ml.message_id=?"), id)
	require.NoError(t, err)
	defer func() { assert.NoError(t, rows.Close()) }()
	var labels []string
	for rows.Next() {
		var label string
		require.NoError(t, rows.Scan(&label))
		labels = append(labels, label)
	}
	require.NoError(t, rows.Err())
	return labels
}
