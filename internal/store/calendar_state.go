package store

import (
	"database/sql"
	"encoding/json"
)

// CalendarEventState is the provider/archival state needed to distinguish an
// appointment from a series master, cancellation or superseded occurrence.
type CalendarEventState struct {
	Status                       string   `json:"status,omitempty"`
	Start                        string   `json:"start,omitempty"`
	End                          string   `json:"end,omitempty"`
	TimeZone                     string   `json:"time_zone,omitempty"`
	AllDay                       bool     `json:"all_day"`
	Recurrence                   []string `json:"recurrence,omitempty"`
	RecurringEventID             string   `json:"recurring_event_id,omitempty"`
	OriginalStartTime            string   `json:"original_start_time,omitempty"`
	SourceStatusBeforeSuperseded string   `json:"source_status_before_superseded,omitempty"`
	SupersededReason             string   `json:"superseded_reason,omitempty"`
	SupersededAt                 string   `json:"superseded_at,omitempty"`
}

func ParseCalendarEventState(messageType string, raw sql.NullString) *CalendarEventState {
	if messageType != "calendar_event" || !raw.Valid {
		return nil
	}
	var state CalendarEventState
	if json.Unmarshal([]byte(raw.String), &state) != nil {
		return nil
	}
	return &state
}
