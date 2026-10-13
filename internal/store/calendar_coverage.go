package store

import (
	"database/sql"
	"encoding/json"
)

// CalendarCoverage describes a successfully expanded window, plus the latest
// refresh attempt. It covers registered sources, never every visible calendar.
type CalendarCoverage struct {
	Start       string `json:"start,omitempty"`
	End         string `json:"end,omitempty"`
	RefreshedAt string `json:"refreshed_at,omitempty"`
	AttemptedAt string `json:"attempted_at,omitempty"`
	Complete    bool   `json:"complete"`
	Status      string `json:"status"`
}

// ParseCalendarCoverage returns nil for legacy or non-calendar configurations.
func ParseCalendarCoverage(config sql.NullString) *CalendarCoverage {
	if !config.Valid {
		return nil
	}
	var c struct {
		Coverage *CalendarCoverage `json:"occurrence_coverage"`
	}
	if json.Unmarshal([]byte(config.String), &c) != nil {
		return nil
	}
	return c.Coverage
}
