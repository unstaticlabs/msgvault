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
	q := search.Parse("message_type:calendar_event after:2026-10-01 before:2026-11-01")
	missing := calendarCache(nil, q)
	require.NotNil(t, missing)
	assert.Equal(t, "unavailable", missing.Status)
	assert.Contains(t, missing.Notice, "Empty results do not establish a free schedule")
	c := &store.CalendarCoverage{Start: "2026-01-01T00:00:00Z", End: "2027-01-01T00:00:00Z", Complete: true, Status: "complete", RefreshedAt: time.Now().UTC().Format(time.RFC3339)}
	accounts := []query.AccountInfo{{ID: 4, SourceType: "gcal", CalendarCoverage: c}, {ID: 5, SourceType: "gmail"}}
	assert.Equal(t, "covered", calendarCache(accounts, q).Status)
	c.Complete = false
	assert.Equal(t, "incomplete", calendarCache(accounts, q).Status)
	c.Complete = true
	c.RefreshedAt = time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	assert.Equal(t, "stale", calendarCache(accounts, q).Status)
	c.RefreshedAt = time.Now().UTC().Format(time.RFC3339)
	assert.Equal(t, "outside_coverage", calendarCache(accounts, search.Parse("message_type:calendar_event after:2028-01-01 before:2028-02-01")).Status)
	assert.Equal(t, "outside_coverage", calendarCache(accounts, search.Parse("message_type:calendar_event")).Status, "an unbounded search cannot claim complete calendar coverage")
	q.AccountIDs = []int64{99}
	assert.Equal(t, "unavailable", calendarCache(accounts, q).Status)
	assert.Nil(t, calendarCache([]query.AccountInfo{{SourceType: "gmail"}}, nil), "mail-only stats should not gain unrelated calendar fields")
}

func TestMCPGetMessageExposesRealArchivedCalendarState(t *testing.T) {
	st := testutil.NewTestStore(t)
	source, err := st.GetOrCreateSource("gcal", "qa@example.com/primary")
	require.NoError(t, err)
	conversation, err := st.EnsureConversationWithType(source.ID, "series", "calendar", "[QA] series")
	require.NoError(t, err)
	id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conversation, SourceMessageID: "series|2030-03-01T09:00:00Z", MessageType: "calendar_event", Subject: sql.NullString{String: "[QA] retired occurrence", Valid: true}})
	require.NoError(t, err)
	require.NoError(t, st.SetMessageMetadata(id, sql.NullString{String: `{"status":"superseded","source_status_before_superseded":"confirmed","recurring_event_id":"series","original_start_time":"2030-03-01T09:00:00Z","start":"2030-03-01T09:00:00Z","end":"2030-03-01T10:00:00Z","superseded_reason":"not_in_expanded_window","all_day":false}`, Valid: true}))
	require.NoError(t, st.UpsertMessageBody(id, sql.NullString{String: "[QA] historical body", Valid: true}, sql.NullString{}))
	handlers := newTestHandlers(query.NewEngine(st.DB(), st.IsPostgreSQL()))
	response := runTool[getMessageResponse](t, "get_message", handlers.getMessage, map[string]any{"id": float64(id)})
	require.NotNil(t, response.CalendarState)
	assert.Equal(t, "superseded", response.CalendarState.Status)
	assert.Equal(t, "confirmed", response.CalendarState.SourceStatusBeforeSuperseded)
	assert.Equal(t, "series", response.CalendarState.RecurringEventID)
	assert.Equal(t, "[QA] historical body", response.BodyText)
}
