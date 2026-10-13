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
	require.NoError(t, err)
	src := primarySource(t, st)
	assert.Equal(t, 4, countMessages(t, st, src.ID), "one master, one standalone and two occurrences, without duplicating the one-off")
	occurrence, ok := loadEvent(t, st, src.ID, "series|2030-03-31T07:00:00Z")
	require.True(t, ok, "ordinary recurring occurrence must be archived")
	assert.Equal(t, "2030-03-31T07:00:00Z", occurrence.sentAt.Time.UTC().Format("2006-01-02T15:04:05Z"))
	assert.Equal(t, "Europe/Paris", occurrence.meta["time_zone"])
	allday, ok := loadEvent(t, st, src.ID, "allday-series|2030-04-01")
	require.True(t, ok)
	assert.Equal(t, true, allday.meta["all_day"])
	coverage := store.ParseCalendarCoverage(src.SyncConfig)
	require.NotNil(t, coverage)
	assert.True(t, coverage.Complete)
	assert.Equal(t, "2030-03-01T00:00:00Z", coverage.Start)
	// Bounded full traversals must not establish a master incremental baseline.
	assert.Empty(t, src.SyncCursor.String)
	_, err = s.Full(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 4, countMessages(t, st, src.ID), "repeated expansion upserts original-start identities")
	src = primarySource(t, st)
	coverage = store.ParseCalendarCoverage(src.SyncConfig)
	require.NoError(t, st.UpdateSourceSyncCursor(src.ID, "masters-T1"))
	cs.occurrencePages[""] = `{"items":[{"id":"i1","status":"confirmed","recurringEventId":"series","summary":"[QA] moved appointment","originalStartTime":{"dateTime":"2030-03-31T09:00:00+02:00"},"start":{"dateTime":"2030-03-31T11:00:00+02:00"},"end":{"dateTime":"2030-03-31T12:00:00+02:00"}}],"nextPageToken":"p2"}`
	cs.incPages["masters-T1"] = `{"items":[{"id":"moved-exception","status":"confirmed","recurringEventId":"series","summary":"[QA] moved in master delta","originalStartTime":{"dateTime":"2030-03-31T09:00:00+02:00"},"start":{"dateTime":"2030-03-31T11:00:00+02:00"},"end":{"dateTime":"2030-03-31T12:00:00+02:00"}}, {"id":"cancelled-exception","status":"cancelled","recurringEventId":"allday-series","originalStartTime":{"date":"2030-04-01"}}],"nextSyncToken":"masters-T2"}`
	cs.occurrenceErrorPage = "p2"
	_, err = s.Incremental(t.Context())
	require.ErrorContains(t, err, "expand recurring events")
	src = primarySource(t, st)
	assert.Equal(t, "masters-T1", src.SyncCursor.String, "failed expansion cannot advance the master cursor")
	occurrence, ok = loadEvent(t, st, src.ID, "series|2030-03-31T07:00:00Z")
	require.True(t, ok)
	assert.Equal(t, "[QA] after DST", occurrence.subject, "neither master exception nor expanded page was published before failed second page")
	assert.Equal(t, "2030-03-31T07:00:00Z", occurrence.sentAt.Time.UTC().Format(time.RFC3339))
	retainedAllDay, retained := loadEvent(t, st, src.ID, "allday-series|2030-04-01")
	require.True(t, retained)
	assert.Equal(t, "confirmed", retainedAllDay.meta["status"], "deferred master cancellation must not mutate the previous successful cache")
	failedCoverage := store.ParseCalendarCoverage(src.SyncConfig)
	require.NotNil(t, failedCoverage)
	assert.False(t, failedCoverage.Complete)
	assert.Equal(t, coverage.RefreshedAt, failedCoverage.RefreshedAt)
	assert.Equal(t, coverage.Start, failedCoverage.Start, "last successful range remains available")
	cs.occurrenceErrorPage = ""
	cs.occurrencePages["p2"] = `{"items":[{"id":"i2","status":"cancelled","recurringEventId":"allday-series","originalStartTime":{"date":"2030-04-01"}}]}`
	_, err = s.Incremental(t.Context())
	require.NoError(t, err)
	src = primarySource(t, st)
	assert.Equal(t, "masters-T2", src.SyncCursor.String)
	occurrence, ok = loadEvent(t, st, src.ID, "series|2030-03-31T07:00:00Z")
	require.True(t, ok)
	assert.Equal(t, "[QA] moved appointment", occurrence.subject)
	assert.Equal(t, "2030-03-31T09:00:00Z", occurrence.sentAt.Time.UTC().Format("2006-01-02T15:04:05Z"))
	allday, ok = loadEvent(t, st, src.ID, "allday-series|2030-04-01")
	require.True(t, ok)
	assert.Equal(t, "cancelled", allday.meta["status"])
	assert.Equal(t, "[QA] all-day occurrence", allday.subject, "cancellation must preserve archived content")
	assert.Equal(t, 4, countMessages(t, st, src.ID), "moved occurrence retains its original identity")
	assert.True(t, store.ParseCalendarCoverage(src.SyncConfig).Complete)
}

func TestOccurrenceDefaultWindowAndMasterCursorAreIndependent(t *testing.T) {
	m := gcal.NewMockAPI()
	m.Calendars = []gcal.Calendar{{ID: "primary", AccessRole: "owner"}}
	m.FullSyncToken["primary"] = "master-baseline"
	syncer, st := newSyncer(t, m, Options{})
	_, err := syncer.Full(t.Context())
	require.NoError(t, err)
	src := primarySource(t, st)
	assert.Equal(t, "master-baseline", src.SyncCursor.String)
	coverage := store.ParseCalendarCoverage(src.SyncConfig)
	require.NotNil(t, coverage)
	start, err := time.Parse(time.RFC3339, coverage.Start)
	require.NoError(t, err)
	end, err := time.Parse(time.RFC3339, coverage.End)
	require.NoError(t, err)
	now := time.Now().UTC()
	assert.Equal(t, now.Year()-1, start.Year())
	assert.Equal(t, now.Year()+1, end.Year())
	assert.True(t, coverage.Complete)
}

func TestOccurrenceRefresh_RealClientSupersedesChangedRecurrence(t *testing.T) {
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
	require.NoError(t, err)
	src := primarySource(t, st)
	old, ok := loadEvent(t, st, src.ID, "series|2030-03-01T09:00:00Z")
	require.True(t, ok)
	previousBody := old.body
	cs.fullPages[""] = `{"items":[{"id":"series","status":"confirmed","summary":"[QA] monthly at new time","start":{"dateTime":"2030-04-01T11:00:00Z"},"end":{"dateTime":"2030-04-01T12:00:00Z"},"recurrence":["RRULE:FREQ=MONTHLY"]},{"id":"oneoff","status":"confirmed","summary":"[QA] standalone","start":{"dateTime":"2030-04-01T09:00:00Z"},"end":{"dateTime":"2030-04-01T10:00:00Z"}}],"nextSyncToken":"T2"}`
	cs.occurrencePages[""] = `{"items":[{"id":"monthly","status":"confirmed","summary":"[QA] current monthly","recurringEventId":"series","originalStartTime":{"dateTime":"2030-04-01T11:00:00Z"},"start":{"dateTime":"2030-04-01T11:00:00Z"},"end":{"dateTime":"2030-04-01T12:00:00Z"}}]}`
	syncer.opts.TimeMax = "2030-05-01T00:00:00Z"
	_, err = syncer.Full(t.Context())
	require.NoError(t, err)
	for _, identity := range []string{"series|2030-03-01T09:00:00Z", "series|2030-03-08T09:00:00Z", "series|2030-04-30T23:00:00Z"} {
		historical, ok := loadEvent(t, st, src.ID, identity)
		require.True(t, ok)
		assert.Equal(t, "superseded", historical.meta["status"], "removed weekly/time identities cannot remain confirmed")
		assert.Equal(t, "confirmed", historical.meta["source_status_before_superseded"])
		assert.Equal(t, "not_in_expanded_window", historical.meta["superseded_reason"])
	}
	historical, ok := loadEvent(t, st, src.ID, "series|2030-03-01T09:00:00Z")
	require.True(t, ok)
	assert.Equal(t, previousBody, historical.body, "history content must be retained")
	assert.Equal(t, old.id, historical.id, "retire history rather than delete/reinsert")
	detail, err := st.GetMessage(historical.id)
	require.NoError(t, err)
	require.NotNil(t, detail.CalendarState)
	assert.Equal(t, "superseded", detail.CalendarState.Status, "API details must expose the historical state")
	queryDetail, err := query.NewSQLiteEngine(st.DB()).GetMessage(t.Context(), historical.id)
	require.NoError(t, err)
	require.NotNil(t, queryDetail.CalendarState)
	assert.Equal(t, "superseded", queryDetail.CalendarState.Status, "query details must expose the historical state")
	later, ok := loadEvent(t, st, src.ID, "series|2030-07-01T09:00:00Z")
	require.True(t, ok)
	assert.Equal(t, "confirmed", later.meta["status"], "a narrower refresh cannot retire evidence outside its window")
	oneoff, ok := loadEvent(t, st, src.ID, "oneoff")
	require.True(t, ok)
	assert.Equal(t, "confirmed", oneoff.meta["status"])
	current, ok := loadEvent(t, st, src.ID, "series|2030-04-01T11:00:00Z")
	require.True(t, ok)
	assert.Equal(t, "confirmed", current.meta["status"])
	src = primarySource(t, st)
	assert.True(t, store.ParseCalendarCoverage(src.SyncConfig).Complete)
	_, err = syncer.Full(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 7, countMessages(t, st, src.ID), "repeating the snapshot retains history without duplication")
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
			require.NoError(t, err)
			src := primarySource(t, st)
			before, ok := loadEvent(t, st, src.ID, "series|2030-03-01")
			require.True(t, ok)
			cs.occurrencePages[""] = `{"items":[]}`
			syncer.opts.TimeMin = test.start
			syncer.opts.TimeMax = test.end
			_, err = syncer.Full(t.Context())
			require.NoError(t, err)
			after, ok := loadEvent(t, st, src.ID, "series|2030-03-01")
			require.True(t, ok)
			assert.Equal(t, test.status, after.meta["status"])
			assert.Equal(t, "2030-03-01", after.meta["start"], "timezone comparison must not change date-only storage")
			assert.Equal(t, "2030-03-01", after.meta["original_start_time"])
			assert.Equal(t, before.id, after.id)
			assert.Equal(t, before.body, after.body)
		})
	}
}
