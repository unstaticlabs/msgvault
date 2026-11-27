package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/msgvault/internal/sourcescope"
	"go.kenn.io/msgvault/internal/store"
)

type CalendarEventStore interface {
	ListCalendarOccurrencesContext(ctx context.Context, from, to time.Time) (store.CalendarOccurrencePage, error)
}

func (s *Server) registerCalendarEventsRoute(api huma.API) {
	op := rawAPIV1Operation("listCalendarEvents", http.MethodGet, "/calendar/events", "List archived calendar occurrences")
	op.Tags = []string{"Calendar"}
	op.Parameters = []*huma.Param{
		{Name: "from", In: "query", Required: true, Schema: &huma.Schema{Type: "string", Format: "date-time"}},
		{Name: "to", In: "query", Required: true, Schema: &huma.Schema{Type: "string", Format: "date-time"}},
		queryIntegerArrayParam("source_ids", "Only include these archive calendar source IDs."),
		queryIntegerParam("limit", "Return at most this many occurrences and unexpandable records, from 1 to 100."),
		queryIntegerParam("offset", "Number of results to skip when a limit is supplied."),
	}
	op.Responses = jsonResponsesFor[store.CalendarOccurrencePage](api)
	op.Errors = []int{400, 401, 403, 500, 503}
	addErrorResponses(api, op.Responses, op.Errors...)
	registerRawHumaRoute(api, op, s.handleCalendarEvents)
}
func (s *Server) handleCalendarEvents(w http.ResponseWriter, r *http.Request) {
	from, err := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	if err != nil {
		writeError(w, 400, "invalid_argument", "from must be RFC3339")
		return
	}
	to, err := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	if err != nil || to.Before(from) || to.Equal(from) && to.Format(time.DateOnly) <= from.Format(time.DateOnly) {
		writeError(w, 400, "invalid_argument", "to must be RFC3339 and after from")
		return
	}
	backend, ok := s.store.(CalendarEventStore)
	if !ok {
		writeError(w, 503, "calendar_unavailable", "Archived calendar events are unavailable")
		return
	}
	ctx := scopedStoreReadContext(r.Context())
	ids, explicit, err := queryInt64s(r, "source_ids")
	// The delegation middleware uses -1 for a grant with no remaining source.
	// Preserve its empty result while rejecting nonpositive IDs from callers.
	emptyGrant := len(ids) == 1 && ids[0] == -1 && slices.Equal(ids, agentReadSourceIDs(r))
	if err != nil || (!emptyGrant && slices.ContainsFunc(ids, func(id int64) bool { return id <= 0 })) {
		writeError(w, 400, "invalid_argument", "source_ids must contain positive calendar source IDs")
		return
	}
	if explicit {
		ids = normalizeSourceIDs(ids)
		visible := visibleSources(ctx, ids)
		if !slices.Equal(ids, visible) {
			writeError(w, 403, "permission_denied", "One or more selected calendars are unavailable")
			return
		}
		ctx = sourcescope.WithSourceIDs(ctx, visible)
	}
	limit, paged, limitErr := queryInt(r, "limit")
	offset, hasOffset, offsetErr := queryInt(r, "offset")
	if limitErr != nil || offsetErr != nil || (paged && (limit < 1 || limit > 100)) || (hasOffset && (!paged || offset < 0)) {
		writeError(w, 400, "invalid_argument", "Use a limit from 1 to 100 and a nonnegative offset")
		return
	}
	page, err := backend.ListCalendarOccurrencesContext(ctx, from, to)
	if err != nil {
		if errors.Is(err, store.ErrQueryTooBroad) {
			writeError(w, 400, "query_too_broad", err.Error())
			return
		}
		if s.writeIfContextError(w, err) {
			return
		}
		writeError(w, 500, "calendar_failed", err.Error())
		return
	}
	if paged {
		// Both lists count toward the same page. A caller can reach every
		// unexpandable record even when all regular occurrences fill the page.
		count := len(page.Occurrences)
		total := count + len(page.Unexpandable)
		start := min(offset, total)
		end := start + min(limit, total-start)
		page.Occurrences = page.Occurrences[min(start, count):min(end, count)]
		page.Unexpandable = page.Unexpandable[max(start-count, 0):max(end-count, 0)]
		page.Total = &total
		if end < total {
			page.NextOffset = &end
		}
	}
	writeJSON(w, 200, page)
}
