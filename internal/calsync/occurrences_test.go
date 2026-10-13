package calsync

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/msgvault/internal/gcal"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestOccurrenceRefresh_RealClientRecurrenceAndRecovery(t *testing.T) {
	check := assert.New(t)
	must := require.New(t)
	cs := &calFakeServer{
		calendarListJSON: `{"items":[{"id":"primary","accessRole":"owner","timeZone":"Europe/Paris"}]}`,
		fullPages: map[string]string{"": `{"items":[
   {"id":"series","status":"confirmed","summary":"[QA] weekly appointment","start":{"dateTime":"2030-03-24T09:00:00+01:00","timeZone":"Europe/Paris"},"end":{"dateTime":"2030-03-24T10:00:00+01:00"},"recurrence":["RRULE:FREQ=WEEKLY"]},
   {"id":"oneoff","status":"confirmed","summary":"[QA] one-off","start":{"date":"2030-04-01"},"end":{"date":"2030-04-02"}}
  ],"nextSyncToken":"masters-T1"}`},
		incPages: map[string]string{"masters-T1": `{"items":[],"nextSyncToken":"masters-T2"}`},
		occurrencePages: map[string]string{
			"":   `{"items":[{"id":"i1","status":"confirmed","recurringEventId":"series","summary":"[QA] after DST","originalStartTime":{"dateTime":"2030-03-31T09:00:00+02:00"},"start":{"dateTime":"2030-03-31T09:00:00+02:00","timeZone":"Europe/Paris"},"end":{"dateTime":"2030-03-31T10:00:00+02:00"}}],"nextPageToken":"p2"}`,
			"p2": `{"items":[{"id":"i2","status":"confirmed","recurringEventId":"allday-series","summary":"[QA] all-day occurrence","originalStartTime":{"date":"2030-04-01"},"start":{"date":"2030-04-01"},"end":{"date":"2030-04-02"}}, {"id":"oneoff","status":"confirmed","summary":"[QA] one-off"}]}`,
		},
	}
	server := cs.start(t)
	defer server.Close()
	st := testutil.NewTestStore(t)
	s := New(realCalClient(t, server), st, Options{AccountEmail: testAccount, TimeMin: "2030-03-01T00:00:00Z", TimeMax: "2030-05-01T00:00:00Z"}).WithLogger(quietLogger())
	_, err := s.Full(t.Context())
	must.NoError(err)
	src := primarySource(t, st)
	check.Equal(4, countMessages(t, st, src.ID), "one master, one standalone and two occurrences, without duplicating the one-off")
	occurrence, ok := loadEvent(t, st, src.ID, "series|2030-03-31T07:00:00Z")
	must.True(ok, "ordinary recurring occurrence must be archived")
	check.Equal("2030-03-31T07:00:00Z", occurrence.sentAt.Time.UTC().Format("2006-01-02T15:04:05Z"))
	check.Equal("Europe/Paris", occurrence.meta["time_zone"])
	allday, ok := loadEvent(t, st, src.ID, "allday-series|2030-04-01")
	must.True(ok)
	check.Equal(true, allday.meta["all_day"])
	coverage := store.ParseCalendarCoverage(src.SyncConfig)
	must.NotNil(coverage)
	check.True(coverage.Complete)
	check.Equal("2030-03-01T00:00:00Z", coverage.Start)
	// Bounded full traversals must not establish a master incremental baseline.
	check.Empty(src.SyncCursor.String)
	_, err = s.Full(t.Context())
	must.NoError(err)
	check.Equal(4, countMessages(t, st, src.ID), "repeated expansion upserts original-start identities")
	src = primarySource(t, st)
	coverage = store.ParseCalendarCoverage(src.SyncConfig)
	must.NoError(st.UpdateSourceSyncCursor(src.ID, "masters-T1"))
	cs.occurrencePages[""] = `{"items":[{"id":"i1","status":"confirmed","recurringEventId":"series","summary":"[QA] moved appointment","originalStartTime":{"dateTime":"2030-03-31T09:00:00+02:00"},"start":{"dateTime":"2030-03-31T11:00:00+02:00"},"end":{"dateTime":"2030-03-31T12:00:00+02:00"}}],"nextPageToken":"p2"}`
	cs.incPages["masters-T1"] = `{"items":[{"id":"moved-exception","status":"confirmed","recurringEventId":"series","summary":"[QA] moved in master delta","originalStartTime":{"dateTime":"2030-03-31T09:00:00+02:00"},"start":{"dateTime":"2030-03-31T11:00:00+02:00"},"end":{"dateTime":"2030-03-31T12:00:00+02:00"}}, {"id":"cancelled-exception","status":"cancelled","recurringEventId":"allday-series","originalStartTime":{"date":"2030-04-01"}}],"nextSyncToken":"masters-T2"}`
	cs.occurrenceErrorPage = "p2"
	_, err = s.Incremental(t.Context())
	must.ErrorContains(err, "expand recurring events")
	src = primarySource(t, st)
	check.Equal("masters-T1", src.SyncCursor.String, "failed expansion cannot advance the master cursor")
	occurrence, ok = loadEvent(t, st, src.ID, "series|2030-03-31T07:00:00Z")
	must.True(ok)
	check.Equal("[QA] after DST", occurrence.subject, "neither master exception nor expanded page was published before failed second page")
	check.Equal("2030-03-31T07:00:00Z", occurrence.sentAt.Time.UTC().Format(time.RFC3339))
	retainedAllDay, retained := loadEvent(t, st, src.ID, "allday-series|2030-04-01")
	must.True(retained)
	check.Equal("confirmed", retainedAllDay.meta["status"], "deferred master cancellation must not mutate the previous successful cache")
	failedCoverage := store.ParseCalendarCoverage(src.SyncConfig)
	must.NotNil(failedCoverage)
	check.False(failedCoverage.Complete)
	check.Equal(coverage.RefreshedAt, failedCoverage.RefreshedAt)
	check.Equal(coverage.Start, failedCoverage.Start, "last successful range remains available")
	cs.occurrenceErrorPage = ""
	cs.occurrencePages["p2"] = `{"items":[{"id":"i2","status":"cancelled","recurringEventId":"allday-series","originalStartTime":{"date":"2030-04-01"}}]}`
	_, err = s.Incremental(t.Context())
	must.NoError(err)
	src = primarySource(t, st)
	check.Equal("masters-T2", src.SyncCursor.String)
	occurrence, ok = loadEvent(t, st, src.ID, "series|2030-03-31T07:00:00Z")
	must.True(ok)
	check.Equal("[QA] moved appointment", occurrence.subject)
	check.Equal("2030-03-31T09:00:00Z", occurrence.sentAt.Time.UTC().Format("2006-01-02T15:04:05Z"))
	allday, ok = loadEvent(t, st, src.ID, "allday-series|2030-04-01")
	must.True(ok)
	check.Equal("cancelled", allday.meta["status"])
	check.Equal("[QA] all-day occurrence", allday.subject, "cancellation must preserve archived content")
	check.Equal(4, countMessages(t, st, src.ID), "moved occurrence retains its original identity")
	check.True(store.ParseCalendarCoverage(src.SyncConfig).Complete)
}

func TestOccurrenceDefaultWindowAndMasterCursorAreIndependent(t *testing.T) {
	check := assert.New(t)
	must := require.New(t)
	m := gcal.NewMockAPI()
	m.Calendars = []gcal.Calendar{{ID: "primary", AccessRole: "owner"}}
	m.FullSyncToken["primary"] = "master-baseline"
	syncer, st := newSyncer(t, m, Options{})
	_, err := syncer.Full(t.Context())
	must.NoError(err)
	src := primarySource(t, st)
	check.Equal("master-baseline", src.SyncCursor.String)
	coverage := store.ParseCalendarCoverage(src.SyncConfig)
	must.NotNil(coverage)
	start, err := time.Parse(time.RFC3339, coverage.Start)
	must.NoError(err)
	end, err := time.Parse(time.RFC3339, coverage.End)
	must.NoError(err)
	now := time.Now().UTC()
	check.Equal(now.Year()-1, start.Year())
	check.Equal(now.Year()+1, end.Year())
	check.True(coverage.Complete)
}

func TestOccurrenceRefresh_RealClientSupersedesChangedRecurrence(t *testing.T) {
	check := assert.New(t)
	must := require.New(t)
	cs := &calFakeServer{
		calendarListJSON: `{"items":[{"id":"primary","accessRole":"owner"}]}`,
		fullPages:        map[string]string{"": `{"items":[{"id":"series","status":"confirmed","summary":"[QA] weekly series","start":{"dateTime":"2030-03-01T09:00:00Z"},"end":{"dateTime":"2030-03-01T10:00:00Z"},"recurrence":["RRULE:FREQ=WEEKLY"]},{"id":"oneoff","status":"confirmed","summary":"[QA] standalone","start":{"dateTime":"2030-04-01T09:00:00Z"},"end":{"dateTime":"2030-04-01T10:00:00Z"}}],"nextSyncToken":"T1"}`},
		incPages:         map[string]string{},
		occurrencePages: map[string]string{"": `{"items":[
   {"id":"weekly1","status":"confirmed","summary":"[QA] weekly first","recurringEventId":"series","originalStartTime":{"dateTime":"2030-03-01T09:00:00Z"},"start":{"dateTime":"2030-03-01T09:00:00Z"},"end":{"dateTime":"2030-03-01T10:00:00Z"}},
   {"id":"weekly2","status":"confirmed","summary":"[QA] weekly second","recurringEventId":"series","originalStartTime":{"dateTime":"2030-03-08T09:00:00Z"},"start":{"dateTime":"2030-03-08T09:00:00Z"},"end":{"dateTime":"2030-03-08T10:00:00Z"}},
   {"id":"future","status":"confirmed","summary":"[QA] later archive","recurringEventId":"series","originalStartTime":{"dateTime":"2030-07-01T09:00:00Z"},"start":{"dateTime":"2030-07-01T09:00:00Z"},"end":{"dateTime":"2030-07-01T10:00:00Z"}},
   {"id":"boundary","status":"confirmed","summary":"[QA] UTC boundary","recurringEventId":"series","originalStartTime":{"dateTime":"2030-05-01T01:00:00+02:00"},"start":{"dateTime":"2030-05-01T01:00:00+02:00"},"end":{"dateTime":"2030-05-01T02:00:00+02:00"}}
  ]}`},
	}
	server := cs.start(t)
	defer server.Close()
	st := testutil.NewTestStore(t)
	syncer := New(realCalClient(t, server), st, Options{AccountEmail: testAccount, TimeMin: "2030-03-01T00:00:00Z", TimeMax: "2030-09-01T00:00:00Z"}).WithLogger(quietLogger())
	_, err := syncer.Full(t.Context())
	must.NoError(err)
	src := primarySource(t, st)
	old, ok := loadEvent(t, st, src.ID, "series|2030-03-01T09:00:00Z")
	must.True(ok)
	previousBody := old.body
	cs.fullPages[""] = `{"items":[{"id":"series","status":"confirmed","summary":"[QA] monthly at new time","start":{"dateTime":"2030-04-01T11:00:00Z"},"end":{"dateTime":"2030-04-01T12:00:00Z"},"recurrence":["RRULE:FREQ=MONTHLY"]},{"id":"oneoff","status":"confirmed","summary":"[QA] standalone","start":{"dateTime":"2030-04-01T09:00:00Z"},"end":{"dateTime":"2030-04-01T10:00:00Z"}}],"nextSyncToken":"T2"}`
	cs.occurrencePages[""] = `{"items":[{"id":"monthly","status":"confirmed","summary":"[QA] current monthly","recurringEventId":"series","originalStartTime":{"dateTime":"2030-04-01T11:00:00Z"},"start":{"dateTime":"2030-04-01T11:00:00Z"},"end":{"dateTime":"2030-04-01T12:00:00Z"}}]}`
	syncer.opts.TimeMax = "2030-05-01T00:00:00Z"
	_, err = syncer.Full(t.Context())
	must.NoError(err)
	for _, identity := range []string{"series|2030-03-01T09:00:00Z", "series|2030-03-08T09:00:00Z", "series|2030-04-30T23:00:00Z"} {
		historical, ok := loadEvent(t, st, src.ID, identity)
		must.True(ok)
		check.Equal("superseded", historical.meta["status"], "removed weekly/time identities cannot remain confirmed")
		check.Equal("confirmed", historical.meta["source_status_before_superseded"])
		check.Equal("not_in_expanded_window", historical.meta["superseded_reason"])
	}
	historical, ok := loadEvent(t, st, src.ID, "series|2030-03-01T09:00:00Z")
	must.True(ok)
	check.Equal(previousBody, historical.body, "history content must be retained")
	check.Equal(old.id, historical.id, "retire history rather than delete/reinsert")
	detail, err := st.GetMessage(historical.id)
	must.NoError(err)
	must.NotNil(detail.CalendarState)
	check.Equal("superseded", detail.CalendarState.Status, "API details must expose the historical state")
	queryDetail, err := query.NewEngine(st.DB(), st.IsPostgreSQL()).GetMessage(t.Context(), historical.id)
	must.NoError(err)
	must.NotNil(queryDetail.CalendarState)
	check.Equal("superseded", queryDetail.CalendarState.Status, "query details must expose the historical state")
	later, ok := loadEvent(t, st, src.ID, "series|2030-07-01T09:00:00Z")
	must.True(ok)
	check.Equal("confirmed", later.meta["status"], "a narrower refresh cannot retire evidence outside its window")
	oneoff, ok := loadEvent(t, st, src.ID, "oneoff")
	must.True(ok)
	check.Equal("confirmed", oneoff.meta["status"])
	current, ok := loadEvent(t, st, src.ID, "series|2030-04-01T11:00:00Z")
	must.True(ok)
	check.Equal("confirmed", current.meta["status"])
	src = primarySource(t, st)
	check.True(store.ParseCalendarCoverage(src.SyncConfig).Complete)
	_, err = syncer.Full(t.Context())
	must.NoError(err)
	check.Equal(7, countMessages(t, st, src.ID), "repeating the snapshot retains history without duplication")
}

func TestOccurrenceRefresh_RealClientAllDayTimezones(t *testing.T) {
	cases := []struct{ name, calendarZone, eventZone, start, end, status string }{
		{"Honolulu outside UTC morning", "Pacific/Honolulu", "", "2030-03-01T00:00:00Z", "2030-03-01T06:00:00Z", "confirmed"},
		{"Tokyo event override outside UTC evening", "UTC", "Asia/Tokyo", "2030-03-01T18:00:00Z", "2030-03-02T03:00:00Z", "confirmed"},
		{"Honolulu overlaps next UTC morning", "Pacific/Honolulu", "", "2030-03-02T04:00:00Z", "2030-03-02T06:00:00Z", "superseded"},
		{"Tokyo overlaps previous UTC evening", "Asia/Tokyo", "", "2030-02-28T17:00:00Z", "2030-02-28T20:00:00Z", "superseded"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			check := assert.New(t)
			must := require.New(t)
			zoneField := ""
			if test.eventZone != "" {
				zoneField = `,"timeZone":"` + test.eventZone + `"`
			}
			cs := &calFakeServer{
				calendarListJSON: fmt.Sprintf(`{"items":[{"id":"primary","accessRole":"owner","timeZone":%q}]}`, test.calendarZone),
				fullPages:        map[string]string{"": `{"items":[]}`}, incPages: map[string]string{},
				occurrencePages: map[string]string{"": `{"items":[{"id":"all-day","status":"confirmed","summary":"[QA] all-day","recurringEventId":"series","originalStartTime":{"date":"2030-03-01"},"start":{"date":"2030-03-01"` + zoneField + `},"end":{"date":"2030-03-02"` + zoneField + `}}]}`},
			}
			server := cs.start(t)
			defer server.Close()
			st := testutil.NewTestStore(t)
			syncer := New(realCalClient(t, server), st, Options{AccountEmail: testAccount, TimeMin: "2030-02-27T00:00:00Z", TimeMax: "2030-03-04T00:00:00Z"}).WithLogger(quietLogger())
			_, err := syncer.Full(t.Context())
			must.NoError(err)
			src := primarySource(t, st)
			before, ok := loadEvent(t, st, src.ID, "series|2030-03-01")
			must.True(ok)
			cs.occurrencePages[""] = `{"items":[]}`
			syncer.opts.TimeMin = test.start
			syncer.opts.TimeMax = test.end
			_, err = syncer.Full(t.Context())
			must.NoError(err)
			after, ok := loadEvent(t, st, src.ID, "series|2030-03-01")
			must.True(ok)
			check.Equal(test.status, after.meta["status"])
			check.Equal("2030-03-01", after.meta["start"], "timezone comparison must not change date-only storage")
			check.Equal("2030-03-01", after.meta["original_start_time"])
			check.Equal(before.id, after.id)
			check.Equal(before.body, after.body)
		})
	}
}
