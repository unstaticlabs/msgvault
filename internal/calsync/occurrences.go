package calsync

import (
	"context"
	"fmt"
	"time"

	"go.kenn.io/msgvault/internal/gcal"
	"go.kenn.io/msgvault/internal/store"
)

// refreshOccurrences leaves the master feed and its sync token unchanged.
// Fetch the complete bounded traversal before publishing any occurrence, so a
// failed page cannot replace a previously usable occurrence cache with a prefix.
func (s *Syncer) refreshOccurrences(ctx context.Context, src *store.Source, cal gcal.Calendar, result *Result) error {
	now := time.Now().UTC()
	start, end := now.AddDate(-1, 0, 0), now.AddDate(1, 0, 0)
	if s.opts.TimeMin != "" {
		var err error
		start, err = time.Parse(time.RFC3339, s.opts.TimeMin)
		if err != nil {
			return fmt.Errorf("invalid occurrence start: %w", err)
		}
		if s.opts.TimeMax == "" {
			end = start.AddDate(2, 0, 0)
		}
	}
	if s.opts.TimeMax != "" {
		var err error
		end, err = time.Parse(time.RFC3339, s.opts.TimeMax)
		if err != nil {
			return fmt.Errorf("invalid occurrence end: %w", err)
		}
		if s.opts.TimeMin == "" {
			start = end.AddDate(-2, 0, 0)
		}
	}
	if !start.Before(end) {
		return fmt.Errorf("occurrence start must precede end")
	}
	// Preserve the last successful range/time even while a new attempt fails.
	cfg := parseSourceConfig(src.SyncConfig)
	coverage := cfg.Coverage
	if coverage == nil {
		coverage = &store.CalendarCoverage{}
	}
	coverage.AttemptedAt = now.Format(time.RFC3339)
	coverage.Complete = false
	coverage.Status = "refreshing"
	cfg.Coverage = coverage
	publishStatus := func(status string) error {
		coverage.Status = status
		return s.store.UpdateSourceSyncConfig(src.ID, buildSourceConfigJSON(cfg))
	}
	if err := publishStatus("refreshing"); err != nil {
		return err
	}
	fail := func(err error) error {
		if statusErr := publishStatus("incomplete"); statusErr != nil {
			return fmt.Errorf("%w; record incomplete coverage: %w", err, statusErr)
		}
		return err
	}
	var events []gcal.Event
	pageToken := ""
	seenPages := map[string]bool{}
	for {
		page, err := s.client.ListEvents(ctx, cal.ID, gcal.EventsListParams{
			SingleEvents: true, ShowDeleted: true, MaxResults: 2500,
			TimeMin: start.UTC().Format(time.RFC3339), TimeMax: end.UTC().Format(time.RFC3339), PageToken: pageToken,
		})
		if err != nil {
			return fail(fmt.Errorf("expand recurring events: %w", err))
		}
		for _, event := range page.Items {
			// One-off events already belong to the master feed. A master returned as a
			// cancellation belongs there too. Do not ingest either for a second time.
			if event.RecurringEventID != "" {
				events = append(events, event)
			}
		}
		if s.opts.Limit > 0 && len(events) > s.opts.Limit {
			return fail(fmt.Errorf("occurrence refresh exceeded --limit; coverage remains incomplete"))
		}
		if page.NextPageToken == "" {
			break
		}
		if seenPages[page.NextPageToken] {
			return fail(fmt.Errorf("occurrence pagination repeated a page token"))
		}
		seenPages[page.NextPageToken] = true
		pageToken = page.NextPageToken
	}
	for _, event := range events {
		if _, _, err := s.persistOne(src.ID, cal, event, result); err != nil {
			return fail(fmt.Errorf("persist expanded occurrence: %w", err))
		}
	}
	coverage.Start = start.UTC().Format(time.RFC3339)
	coverage.End = end.UTC().Format(time.RFC3339)
	coverage.RefreshedAt = now.Format(time.RFC3339)
	coverage.Complete = true
	return publishStatus("complete")
}

// A failed or partial master traversal also invalidates availability claims.
func (s *Syncer) markCalendarIncomplete(sourceID int64) error {
	src, err := s.store.GetSourceByID(sourceID)
	if err != nil {
		return err
	}
	cfg := parseSourceConfig(src.SyncConfig)
	if cfg.Coverage == nil {
		cfg.Coverage = &store.CalendarCoverage{}
	}
	cfg.Coverage.Complete = false
	cfg.Coverage.Status = "incomplete"
	cfg.Coverage.AttemptedAt = time.Now().UTC().Format(time.RFC3339)
	return s.store.UpdateSourceSyncConfig(sourceID, buildSourceConfigJSON(cfg))
}
