package cmd

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
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
	mcpserver "go.kenn.io/msgvault/internal/mcp"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func openCalendarArchiveSession(t *testing.T, opts mcpserver.ServeOptions) *sdkmcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	clientWire, serverWire := sdkmcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- mcpserver.ServeTransport(ctx, opts, serverWire) }()
	session, err := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "schedule fixture", Version: "1"}, nil).Connect(ctx, clientWire, nil)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, session.Close()); cancel(); <-done })
	return session
}

func TestMCPArchivedOccurrencesUseSelectedAndGrantedCalendars(t *testing.T) {
	assertions, requirements := assert.New(t), require.New(t)
	st := testutil.NewTestStore(t)
	ctx := t.Context()
	public, err := st.GetOrCreateSource("gcal", "studio@example.test")
	requirements.NoError(err)
	other, err := st.GetOrCreateSource("gcal", "second@example.test")
	requirements.NoError(err)
	coverage := fmt.Sprintf(`{"calendar_id":"studio@example.test","time_zone":"UTC","occurrence_coverage":{"start":"2027-03-01T00:00:00Z","end":"2027-04-01T00:00:00Z","complete":true,"refreshed_at":%q,"status":"complete"}}`, time.Now().UTC().Format(time.RFC3339))
	requirements.NoError(st.UpdateSourceSyncConfig(public.ID, coverage))
	add := func(src *store.Source, key, subject, raw string) int64 {
		t.Helper()
		id, err := st.PersistMessage(&store.MessagePersistData{Message: &store.Message{SourceID: src.ID, SourceMessageID: key, MessageType: "calendar_event", Subject: sql.NullString{String: subject, Valid: true}}, Conversation: &store.ConversationPersistData{SourceConversationID: key, ConversationType: "calendar", Title: subject}, RawFormat: "gcal_json", RawMIME: []byte(raw)})
		requirements.NoError(err)
		return id
	}
	master := add(public, "weekly", "Studio rehearsal", `{"id":"weekly","start":{"dateTime":"2027-03-02T10:00:00Z"},"end":{"dateTime":"2027-03-02T11:00:00Z"},"recurrence":["RRULE:FREQ=WEEKLY;COUNT=3"]}`)
	moved := add(public, "moved", "Rehearsal moved", `{"id":"moved","recurringEventId":"weekly","originalStartTime":{"dateTime":"2027-03-09T10:00:00Z"},"start":{"dateTime":"2027-03-10T11:00:00Z"},"end":{"dateTime":"2027-03-10T12:00:00Z"}}`)
	cancelled := add(public, "cancel", "Rehearsal cancelled", `{"id":"cancel","recurringEventId":"weekly","originalStartTime":{"dateTime":"2027-03-16T10:00:00Z"},"status":"cancelled"}`)
	add(other, "elsewhere", "Other rehearsal", `{"id":"elsewhere","start":{"dateTime":"2027-03-04T10:00:00Z"},"end":{"dateTime":"2027-03-04T11:00:00Z"}}`)
	cfg := &config.Config{Server: config.ServerConfig{APIKey: "synthetic-schedule-fixture", AgentAccess: true}}
	cfg.Auth.APIKeys = []config.APIKeyConfig{{Name: "studio member", Key: "synthetic-studio-member", Role: "member", User: "member@example.test"}}
	user, err := st.RecordUserLogin(ctx, store.UserLogin{Issuer: "https://id.example.test", Subject: "studio-member", Email: "member@example.test", Role: "member"})
	requirements.NoError(err)
	requirements.NoError(st.SetUserSources(ctx, user.ID, []int64{public.ID}))
	cfg.Data.DataDir = t.TempDir()
	log := slog.New(slog.DiscardHandler)
	daemon := httptest.NewServer(api.NewServerWithOptions(api.ServerOptions{Config: cfg, UserStore: st, Store: &storeAPIAdapter{store: st, config: cfg, logger: log}, Engine: query.NewEngine(st.DB(), st.IsPostgreSQL()), Logger: log}).Router())
	t.Cleanup(daemon.Close)
	owner, err := daemonclient.New(daemonclient.Config{URL: daemon.URL, APIKey: cfg.Server.APIKey, AllowInsecure: true, HTTPClient: daemon.Client()})
	requirements.NoError(err)
	t.Cleanup(func() { assertions.NoError(owner.Close()) })
	sourceGrant := func(permission string) mcpserver.ServeOptions {
		t.Helper()
		issued, err := owner.IssueAgentToken(ctx, "studio reader", []string{permission}, []int64{public.ID}, nil, time.Time{})
		requirements.NoError(err)
		client, err := daemonclient.New(daemonclient.Config{URL: daemon.URL, AgentToken: issued.Secret, AllowInsecure: true, HTTPClient: daemon.Client()})
		requirements.NoError(err)
		t.Cleanup(func() { assertions.NoError(client.Close()) })
		opts, err := delegatedMCPServeOptions(ctx, client)
		requirements.NoError(err)
		return opts
	}
	ownerSession := openCalendarArchiveSession(t, daemonMCPServeOptions(ctx, owner, nil))
	memberClient, err := daemonclient.New(daemonclient.Config{URL: daemon.URL, APIKey: "synthetic-studio-member", HTTPClient: daemon.Client(), AllowInsecure: true})
	requirements.NoError(err)
	t.Cleanup(func() { assertions.NoError(memberClient.Close()) })
	memberSession := openCalendarArchiveSession(t, daemonMCPServeOptions(ctx, memberClient, nil))
	member := openCalendarArchiveSession(t, sourceGrant("message.read"))
	searchOnly := openCalendarArchiveSession(t, sourceGrant("search.read"))
	for _, tc := range []struct {
		session *sdkmcp.ClientSession
		visible bool
	}{{ownerSession, true}, {memberSession, true}, {member, true}, {searchOnly, false}} {
		catalog, err := tc.session.ListTools(ctx, nil)
		requirements.NoError(err)
		assertions.Equal(tc.visible, slices.ContainsFunc(catalog.Tools, func(tool *sdkmcp.Tool) bool { return tool.Name == mcpserver.ToolListCalendarOccurrences }))
	}
	call := func(session *sdkmcp.ClientSession, ids []int64, offset int) *sdkmcp.CallToolResult {
		t.Helper()
		res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: mcpserver.ToolListCalendarOccurrences, Arguments: map[string]any{"source_ids": ids, "from": "2027-03-01T00:00:00Z", "to": "2027-03-31T00:00:00Z", "limit": 2, "offset": offset}})
		requirements.NoError(err)
		return res
	}
	for _, session := range []*sdkmcp.ClientSession{ownerSession, memberSession, member} {
		for _, tc := range []struct {
			offset int
			want   []int64
		}{{0, []int64{master, moved}}, {2, []int64{cancelled}}, {4, []int64{}}} {
			res := call(session, []int64{public.ID}, tc.offset)
			requirements.False(res.IsError, "%+v", res.Content)
			data, err := json.Marshal(res.StructuredContent)
			requirements.NoError(err)
			var page struct {
				Total  int                        `json:"total"`
				Next   *int                       `json:"next_offset"`
				Events []store.CalendarOccurrence `json:"occurrences"`
				Cache  mcpserver.CalendarCache    `json:"calendar_cache"`
			}
			requirements.NoError(json.Unmarshal(data, &page))
			requirements.Equal(3, page.Total)
			requirements.Len(page.Events, len(tc.want))
			requirements.Len(page.Cache.Sources, 1)
			assertions.Equal(public.ID, page.Cache.Sources[0].SourceID)
			assertions.Equal("covered", page.Cache.Status)
			for i, event := range page.Events {
				assertions.Equal(tc.want[i], event.MessageID)
				assertions.Contains(string(data), fmt.Sprintf("/messages/%d\"", event.MessageID))
				assertions.Equal(public.ID, event.SourceID)
				if event.MessageID == moved {
					assertions.Equal(master, event.SeriesMessageID)
					assertions.Equal("2027-03-10T11:00:00Z", event.Start.Format(time.RFC3339))
				}
				if event.MessageID == cancelled {
					assertions.Equal("cancelled", event.Status)
				}
			}
			if tc.offset == 0 {
				requirements.NotNil(page.Next)
				assertions.Equal(2, *page.Next)
			} else {
				assertions.Nil(page.Next)
			}
		}
	}
	res := call(member, []int64{public.ID, other.ID}, 0)
	assertions.True(res.IsError)
	wire, err := json.Marshal(res)
	requirements.NoError(err)
	assertions.NotContains(string(wire), "Other rehearsal")
	res = call(memberSession, []int64{other.ID}, 0)
	assertions.True(res.IsError)
	_, err = owner.SetUserSources(ctx, user.ID, []int64{})
	requirements.NoError(err)
	res = call(memberSession, []int64{public.ID}, 0)
	assertions.True(res.IsError)

	// The final page must also include records whose recurrence cannot be
	// expanded, even when the scheduled occurrences consumed earlier pages.
	unexpandable := add(other, "hourly", "Unsupported repeat", `{"id":"hourly","start":{"date":"2027-03-01"},"end":{"date":"2027-03-02"},"recurrence":["RRULE:FREQ=HOURLY"]}`)
	res = call(ownerSession, []int64{public.ID, other.ID}, 4)
	requirements.False(res.IsError)
	wire, err = json.Marshal(res.StructuredContent)
	requirements.NoError(err)
	var last struct {
		Total   int                          `json:"total"`
		Events  []store.CalendarOccurrence   `json:"occurrences"`
		Skipped []store.CalendarUnexpandable `json:"unexpandable"`
		Cache   mcpserver.CalendarCache      `json:"calendar_cache"`
	}
	requirements.NoError(json.Unmarshal(wire, &last))
	assertions.Equal(5, last.Total)
	assertions.Empty(last.Events)
	requirements.Len(last.Skipped, 1)
	assertions.Equal(unexpandable, last.Skipped[0].MessageID)
	assertions.NotEmpty(last.Skipped[0].Reason)
	assertions.Equal("mixed", last.Cache.Status)
}
