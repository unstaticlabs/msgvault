package calsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"go.kenn.io/msgvault/internal/gcal"
	"go.kenn.io/msgvault/internal/store"
)

// refreshOccurrences leaves the master feed and its sync token unchanged.
// Fetch the complete bounded traversal before publishing any occurrence, so a
// failed page cannot replace a previously usable occurrence cache with a prefix.
func (s *Syncer) refreshOccurrences(ctx context.Context, src *store.Source, cal gcal.Calendar, result *Result, deferred []gcal.Event) error {
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
	observed := map[string]bool{}
	expanded := map[string]bool{}
	for _, event := range events {
		observed[deriveSourceMessageID(event)] = true
		expanded[deriveSourceMessageID(event)] = true
	}
	// The master feed also contains exception instances. Apply them only after
	// expansion has succeeded; matching expanded records are the current oracle.
	for _, event := range deferred {
		if expanded[deriveSourceMessageID(event)] {
			continue
		}
		if _, _, err := s.persistOne(src.ID, cal, event, result); err != nil {
			return fail(fmt.Errorf("persist deferred exception: %w", err))
		}
	}
	for _, event := range events {
		if _, _, err := s.persistOne(src.ID, cal, event, result); err != nil {
			return fail(fmt.Errorf("persist expanded occurrence: %w", err))
		}
	}
	if err := s.supersedeMissingOccurrences(src.ID, start, end, observed, now); err != nil {
		return fail(err)
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

// supersedeMissingOccurrences records disappearance as archive history, not a
// provider cancellation. Only previously active occurrences overlapping this
// fully traversed window are candidates; one-off events and masters are kept.
func (s *Syncer) supersedeMissingOccurrences(sourceID int64, start, end time.Time, observed map[string]bool, now time.Time) error {
	// SQLite retains timestamp offsets as text. Compare parsed event intervals,
	// not lexical sent_at values, or an occurrence near a UTC boundary can be
	// missed. The source/type predicate confines this read to one calendar.
	rows, err := s.store.DB().Query(s.store.Rebind(`SELECT id,source_message_id,metadata FROM messages WHERE source_id=? AND message_type=?`), sourceID, gcal.MessageTypeCalendarEvent)
	if err != nil {
		return fmt.Errorf("read occurrence window: %w", err)
	}
	type pending struct {
		id       int64
		metadata sql.NullString
	}
	var updates []pending
	for rows.Next() {
		var id int64
		var identity string
		var raw sql.NullString
		if err := rows.Scan(&id, &identity, &raw); err != nil {
			rows.Close()
			return err
		}
		if observed[identity] || !raw.Valid {
			continue
		}
		var metadata map[string]any
		if err := json.Unmarshal([]byte(raw.String), &metadata); err != nil {
			rows.Close()
			return fmt.Errorf("decode archived occurrence: %w", err)
		}
		series, _ := metadata["recurring_event_id"].(string)
		status, _ := metadata["status"].(string)
		if series == "" || (status != gcal.StatusConfirmed && status != gcal.StatusTentative) {
			continue
		}
		currentStart, _ := metadata["start"].(string)
		currentEnd, _ := metadata["end"].(string)
		from, ok := metadataInstant(currentStart)
		if !ok {
			continue
		}
		to, ok := metadataInstant(currentEnd)
		if !ok {
			to = from
		}
		if !from.Before(end) || !to.After(start) {
			continue
		}
		metadata["source_status_before_superseded"] = status
		metadata["status"] = "superseded"
		metadata["superseded_reason"] = "not_in_expanded_window"
		metadata["superseded_at"] = now.Format(time.RFC3339)
		encoded, err := json.Marshal(metadata)
		if err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, pending{id: id, metadata: sql.NullString{String: string(encoded), Valid: true}})
	}
	scanErr := rows.Err()
	closeErr := rows.Close()
	if scanErr != nil {
		return scanErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, update := range updates {
		if err := s.store.SetMessageMetadata(update.id, update.metadata); err != nil {
			return fmt.Errorf("supersede removed occurrence: %w", err)
		}
	}
	return nil
}

func metadataInstant(value string) (time.Time, bool) {
	if instant, err := time.Parse(time.RFC3339, value); err == nil {
		return instant, true
	}
	if day, err := time.Parse("2006-01-02", value); err == nil {
		return day.UTC(), true
	}
	return time.Time{}, false
}
