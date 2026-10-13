package calsync

import (
	"go.kenn.io/msgvault/internal/gcal"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	cs.occurrenceErrorPage = "p2"
	_, err = s.Incremental(t.Context())
	require.ErrorContains(t, err, "expand recurring events")
	src = primarySource(t, st)
	assert.Equal(t, "masters-T1", src.SyncCursor.String, "failed expansion cannot advance the master cursor")
	occurrence, ok = loadEvent(t, st, src.ID, "series|2030-03-31T07:00:00Z")
	require.True(t, ok)
	assert.Equal(t, "[QA] after DST", occurrence.subject, "first page was buffered, not published before failed second page")
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
