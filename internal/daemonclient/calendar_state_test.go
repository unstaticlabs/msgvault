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
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/cli/message", r.URL.Path)
		assert.Equal(t, "41", r.URL.Query().Get("id"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":41,"source_message_id":"series|2030-03-01","conversation_id":4,"source_conversation_id":"series","subject":"[QA] old appointment","message_type":"calendar_event","snippet":"","sent_at":"2030-03-01T00:00:00Z","received_at":null,"deleted_at":null,"size_estimate":0,"has_attachments":false,"from":[],"to":[],"cc":[],"bcc":[],"labels":[],"attachments":[],"body_text":"[QA] retained history","body_html":"","calendar_state":{"status":"superseded","source_status_before_superseded":"confirmed","recurring_event_id":"series","original_start_time":"2030-03-01","all_day":true}}`))
	}))
	defer server.Close()
	message, err := newTestStore(server, "key").GetCLIMessage(t.Context(), "41")
	require.NoError(t, err)
	require.NotNil(t, message.CalendarState)
	assert.Equal(t, "superseded", message.CalendarState.Status)
	assert.Equal(t, "confirmed", message.CalendarState.SourceStatusBeforeSuperseded)
	assert.Equal(t, "series", message.CalendarState.RecurringEventID)
	assert.Equal(t, "2030-03-01", message.CalendarState.OriginalStartTime)
	assert.Equal(t, "[QA] retained history", message.BodyText)
}

func TestCLIMessageNilResponseDoesNotPanic(t *testing.T) {
	var detail *query.MessageDetail
	require.NotPanics(t, func() { detail = cliMessageDetailFromGenerated(nil) })
	require.NotNil(t, detail)
	assert.Nil(t, detail.CalendarState)
	assert.Zero(t, detail.ID)
}
