package mcp

import (
	"context"
	"slices"
	"time"

	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/search"
	"go.kenn.io/msgvault/internal/store"
)

// CalendarCache explicitly separates archived results from authoritative live
// availability. Unregistered calendars cannot be inferred from local sources.
type CalendarCache struct {
	Scope   string                `json:"scope"`
	Status  string                `json:"status"`
	Notice  string                `json:"notice"`
	Sources []CalendarCacheSource `json:"sources"`
}

type CalendarCacheSource struct {
	SourceID int64                   `json:"source_id"`
	Coverage *store.CalendarCoverage `json:"coverage,omitempty"`
	Status   string                  `json:"status"`
}

func calendarCache(accounts []query.AccountInfo, q *search.Query) *CalendarCache {
	response := &CalendarCache{
		Scope: "registered_calendars", Status: "unavailable",
		Notice:  "This is an archive, not live availability. Empty results do not establish a free schedule. Check calendar registration, coverage and freshness. Series masters, cancelled and superseded events remain archived; inspect get_message.calendar_state before counting appointments.",
		Sources: []CalendarCacheSource{},
	}
	for _, account := range accounts {
		if account.SourceType != "gcal" || (q != nil && len(q.AccountIDs) > 0 && !slices.Contains(q.AccountIDs, account.ID)) {
			continue
		}
		status := "incomplete"
		coverage := account.CalendarCoverage
		if coverage != nil && coverage.Complete {
			status = "covered"
			refreshed, err := time.Parse(time.RFC3339, coverage.RefreshedAt)
			if err != nil || time.Since(refreshed) > 24*time.Hour {
				status = "stale"
			}
			start, e1 := time.Parse(time.RFC3339, coverage.Start)
			end, e2 := time.Parse(time.RFC3339, coverage.End)
			if e1 != nil || e2 != nil {
				status = "incomplete"
			} else if q != nil {
				if q.AfterDate == nil || q.BeforeDate == nil || q.AfterDate.Before(start) || q.BeforeDate.After(end) {
					status = "outside_coverage"
				}
			}
		}
		response.Sources = append(response.Sources, CalendarCacheSource{SourceID: account.ID, Coverage: coverage, Status: status})
		if len(response.Sources) == 1 {
			response.Status = status
		} else if response.Status != status {
			response.Status = "mixed"
		}
	}
	if q == nil && len(response.Sources) == 0 {
		return nil
	}
	return response
}

func (h *handlers) calendarQueryCache(ctx context.Context, q *search.Query) *CalendarCache {
	if !slices.Contains(q.MessageTypes, "calendar_event") {
		return nil
	}
	accounts, err := h.engine.ListAccounts(ctx)
	if err != nil {
		return &CalendarCache{Scope: "registered_calendars", Status: "unavailable", Notice: "Calendar coverage could not be loaded. Empty archived results do not establish a free schedule.", Sources: []CalendarCacheSource{}}
	}
	return calendarCache(accounts, q)
}
