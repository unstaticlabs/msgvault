package daemonclient

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/msgvault/internal/query"
)

func TestGetCLIMessagePreservesCalendarState(t *testing.T) {
	check := assert.New(t)
	must := require.New(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		check.Equal("/api/v1/cli/message", r.URL.Path)
		check.Equal("41", r.URL.Query().Get("id"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":41,"source_message_id":"series|2030-03-01","conversation_id":4,"source_conversation_id":"series","subject":"[QA] old appointment","message_type":"calendar_event","snippet":"","sent_at":"2030-03-01T00:00:00Z","received_at":null,"deleted_at":null,"size_estimate":0,"has_attachments":false,"from":[],"to":[],"cc":[],"bcc":[],"labels":[],"attachments":[],"body_text":"[QA] retained history","body_html":"","calendar_state":{"status":"superseded","source_status_before_superseded":"confirmed","recurring_event_id":"series","original_start_time":"2030-03-01","all_day":true}}`))
	}))
	defer server.Close()
	message, err := newTestStore(server, "key").GetCLIMessage(t.Context(), "41")
	must.NoError(err)
	must.NotNil(message.CalendarState)
	check.Equal("superseded", message.CalendarState.Status)
	check.Equal("confirmed", message.CalendarState.SourceStatusBeforeSuperseded)
	check.Equal("series", message.CalendarState.RecurringEventID)
	check.Equal("2030-03-01", message.CalendarState.OriginalStartTime)
	check.Equal("[QA] retained history", message.BodyText)
}

func TestCLIMessageNilResponseDoesNotPanic(t *testing.T) {
	check := assert.New(t)
	must := require.New(t)
	var detail *query.MessageDetail
	must.NotPanics(func() { detail = cliMessageDetailFromGenerated(nil) })
	must.NotNil(detail)
	check.Nil(detail.CalendarState)
	check.Zero(detail.ID)
}
