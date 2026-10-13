package mcp

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/search"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestCalendarCache_EmptyUnavailableIncompleteStaleAndOutside(t *testing.T) {
	check := assert.New(t)
	must := require.New(t)
	q := search.Parse("message_type:calendar_event after:2026-10-01 before:2026-11-01")
	missing := calendarCache(nil, q)
	must.NotNil(missing)
	check.Equal("unavailable", missing.Status)
	check.Contains(missing.Notice, "Empty results do not establish a free schedule")
	c := &store.CalendarCoverage{Start: "2026-01-01T00:00:00Z", End: "2027-01-01T00:00:00Z", Complete: true, Status: "complete", RefreshedAt: time.Now().UTC().Format(time.RFC3339)}
	accounts := []query.AccountInfo{{ID: 4, SourceType: "gcal", CalendarCoverage: c}, {ID: 5, SourceType: "gmail"}}
	check.Equal("covered", calendarCache(accounts, q).Status)
	c.Complete = false
	check.Equal("incomplete", calendarCache(accounts, q).Status)
	c.Complete = true
	c.RefreshedAt = time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	check.Equal("stale", calendarCache(accounts, q).Status)
	c.RefreshedAt = time.Now().UTC().Format(time.RFC3339)
	check.Equal("outside_coverage", calendarCache(accounts, search.Parse("message_type:calendar_event after:2028-01-01 before:2028-02-01")).Status)
	check.Equal("outside_coverage", calendarCache(accounts, search.Parse("message_type:calendar_event")).Status, "an unbounded search cannot claim complete calendar coverage")
	q.AccountIDs = []int64{99}
	check.Equal("unavailable", calendarCache(accounts, q).Status)
	check.Nil(calendarCache([]query.AccountInfo{{SourceType: "gmail"}}, nil), "mail-only stats should not gain unrelated calendar fields")
}

func TestMCPGetMessageExposesRealArchivedCalendarState(t *testing.T) {
	check := assert.New(t)
	must := require.New(t)
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gcal", "qa@example.com/primary")
	must.NoError(err)
	conversation, err := st.EnsureConversationWithType(source.ID, "series", "calendar", "[QA] series")
	must.NoError(err)
	id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conversation, SourceMessageID: "series|2030-03-01T09:00:00Z", MessageType: "calendar_event", Subject: sql.NullString{String: "[QA] retired occurrence", Valid: true}})
	must.NoError(err)
	must.NoError(st.SetMessageMetadata(id, sql.NullString{String: `{"status":"superseded","source_status_before_superseded":"confirmed","recurring_event_id":"series","original_start_time":"2030-03-01T09:00:00Z","start":"2030-03-01T09:00:00Z","end":"2030-03-01T10:00:00Z","superseded_reason":"not_in_expanded_window","all_day":false}`, Valid: true}))
	must.NoError(st.UpsertMessageBody(id, sql.NullString{String: "[QA] historical body", Valid: true}, sql.NullString{}))
	handlers := newTestHandlers(query.NewEngine(st.DB(), st.IsPostgreSQL()))
	response := runTool[getMessageResponse](t, "get_message", handlers.getMessage, map[string]any{"id": float64(id)})
	must.NotNil(response.CalendarState)
	check.Equal("superseded", response.CalendarState.Status)
	check.Equal("confirmed", response.CalendarState.SourceStatusBeforeSuperseded)
	check.Equal("series", response.CalendarState.RecurringEventID)
	check.Equal("[QA] historical body", response.BodyText)
}
