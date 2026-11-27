package cmd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/calsync"
	"go.kenn.io/msgvault/internal/gcal"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

type legacyCalendarTime struct {
	id, stored, want string
	event            gcal.Event
}

func legacyCalendarTimes(t *testing.T) []legacyCalendarTime {
	t.Helper()
	timed := func(id, start, zone, want string) legacyCalendarTime {
		instant, err := time.Parse(time.RFC3339Nano, start)
		require.NoError(t, err)
		return legacyCalendarTime{id: id, stored: start, want: want, event: gcal.Event{
			ID: id, Status: gcal.StatusConfirmed,
			Start: gcal.EventDateTime{DateTime: instant, TimeZone: zone},
			End:   gcal.EventDateTime{DateTime: instant.Add(time.Hour), TimeZone: zone},
		}}
	}
	fixtures := []legacyCalendarTime{
		timed("summer", "2026-06-02T09:00:00+02:00", "Europe/Paris", "2026-06-02T07:00:00Z"),
		timed("winter", "2026-01-02T09:00:00+01:00", "Europe/Paris", "2026-01-02T08:00:00Z"),
		timed("fold-first", "2026-10-25T02:30:00+02:00", "Europe/Paris", "2026-10-25T00:30:00Z"),
		timed("fold-second", "2026-10-25T02:30:00+01:00", "Europe/Paris", "2026-10-25T01:30:00Z"),
		timed("previous-year", "2026-01-01T00:30:00+05:30", "Asia/Kolkata", "2025-12-31T19:00:00Z"),
		timed("next-year", "2025-12-31T23:30:00-05:00", "America/New_York", "2026-01-01T04:30:00Z"),
		timed("fraction", "2026-06-02T09:00:00.123456+02:00", "Europe/Paris", "2026-06-02T07:00:00.123456Z"),
		timed("weekly", "2026-03-22T09:00:00+01:00", "Europe/Paris", "2026-03-22T08:00:00Z"),
	}
	naive := timed("offset-free", "2026-06-02T07:00:00Z", "UTC", "2026-06-02T07:00:00Z")
	naive.stored = "2026-06-02 07:00:00"
	fixtures[len(fixtures)-1].event.Recurrence = []string{"RRULE:FREQ=WEEKLY;COUNT=3"}
	fixtures = append(fixtures, naive, legacyCalendarTime{
		id: "all-day", stored: "2026-03-29T00:00:00Z", want: "2026-03-29T00:00:00Z",
		event: gcal.Event{ID: "all-day", Status: gcal.StatusConfirmed,
			Start: gcal.EventDateTime{Date: "2026-03-29"}, End: gcal.EventDateTime{Date: "2026-03-30"}},
	})
	return fixtures
}

func legacyCalendarArchive(t *testing.T, dbPath string) ([]legacyCalendarTime, map[int64]string) {
	t.Helper()
	st := testutil.NewSQLiteTestStoreAt(t, dbPath)
	fixtures := legacyCalendarTimes(t)
	mock := gcal.NewMockAPI()
	mock.Calendars = []gcal.Calendar{{ID: "primary", AccessRole: "owner", TimeZone: "Europe/Paris"}}
	var events []gcal.Event
	for _, fixture := range fixtures {
		event := fixture.event
		event.Organizer = gcal.Person{Email: "organizer@example.com"}
		event.Attendees = []gcal.Attendee{{Email: "guest@example.com"}}
		events = append(events, event)
	}
	mock.FullEvents["primary"] = [][]gcal.Event{events}
	mock.FullSyncToken["primary"] = "synthetic-sync-token"
	_, err := calsync.New(mock, st, calsync.Options{AccountEmail: "organizer@example.com"}).Full(t.Context())
	require.NoError(t, err)
	ids := make(map[int64]string)
	for _, fixture := range fixtures {
		var id int64
		require.NoError(t, st.DB().QueryRow(`SELECT id FROM messages WHERE source_message_id = ?`, fixture.id).Scan(&id))
		// Reproduce archives written before UTC persistence was introduced.
		_, err := st.DB().Exec(`UPDATE messages SET sent_at = ? WHERE id = ?`, fixture.stored, id)
		require.NoError(t, err)
		ids[id] = fixture.id
		detail, err := query.NewSQLiteEngine(st.DB()).GetMessage(t.Context(), id)
		require.NoError(t, err)
		require.Equal(t, fixture.want, detail.SentAt.UTC().Format(time.RFC3339Nano), "canonical get_message instant")
	}
	// Any attempt to fix archive/provider state instead of its derived cache
	// must fail. Cache journals remain writable for their normal acknowledgments.
	for _, table := range []string{"messages", "sources", "sync_runs", "message_raw", "message_bodies", "calendar_events", "calendar_event_attendees"} {
		for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
			_, err := st.DB().Exec(fmt.Sprintf(`CREATE TRIGGER reject_cache_repair_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'canonical archive writes forbidden'); END`, table, operation, operation, table))
			require.NoError(t, err)
		}
	}
	require.NoError(t, st.Close())
	return fixtures, ids
}

func assertLegacyCalendarCache(t *testing.T, dbPath, analyticsDir string, fixtures []legacyCalendarTime, ids map[int64]string) {
	t.Helper()
	duck, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { require.NoError(t, duck.Close()) }()
	path := filepath.Join(analyticsDir, "messages", "**", "*.parquet")
	var timestampType string
	require.NoError(t, duck.QueryRow(`SELECT typeof(sent_at) FROM read_parquet(?) LIMIT 1`, path).Scan(&timestampType))
	assert.Equal(t, "TIMESTAMP", timestampType, "existing Parquet type contract")
	for _, fixture := range fixtures {
		var got time.Time
		var year, month int
		require.NoError(t, duck.QueryRow(`SELECT sent_at, year, month FROM read_parquet(?, hive_partitioning=true) WHERE source_message_id = ?`, path, fixture.id).Scan(&got, &year, &month))
		assert.Equal(t, fixture.want, got.UTC().Format(time.RFC3339Nano), fixture.id)
		expected, err := time.Parse(time.RFC3339Nano, fixture.want)
		require.NoError(t, err)
		assert.Equal(t, expected.Year(), year, "UTC year partition: %s", fixture.id)
		assert.Equal(t, int(expected.Month()), month, "UTC month: %s", fixture.id)
	}
	st, err := store.Open(dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()
	for id, key := range ids {
		var stored string
		require.NoError(t, st.DB().QueryRow(`SELECT CAST(sent_at AS TEXT) FROM messages WHERE id = ?`, id).Scan(&stored))
		for _, fixture := range fixtures {
			if fixture.id == key {
				assert.Equal(t, fixture.stored, stored, "canonical timestamp bytes are unchanged")
			}
		}
	}
	from := time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC)
	page, err := st.ListCalendarOccurrencesContext(t.Context(), from, from.AddDate(0, 0, 16))
	require.NoError(t, err)
	require.Empty(t, page.Unexpandable)
	var weekly []string
	var allDay []store.CalendarOccurrence
	for _, occurrence := range page.Occurrences {
		if ids[occurrence.SeriesMessageID] == "weekly" {
			weekly = append(weekly, occurrence.Start.UTC().Format(time.RFC3339))
		}
		if ids[occurrence.MessageID] == "all-day" {
			allDay = append(allDay, occurrence)
		}
	}
	assert.Equal(t, []string{"2026-03-22T08:00:00Z", "2026-03-29T07:00:00Z", "2026-04-05T07:00:00Z"}, weekly)
	require.Len(t, allDay, 1)
	assert.True(t, allDay[0].AllDay)
	assert.Equal(t, "2026-03-29", allDay[0].StartDate)
	assert.Equal(t, "2026-03-30", allDay[0].EndDate)
}

func TestBuildCache_LegacyCalendarOffsetsPreserveInstantsWithoutArchiveWrites(t *testing.T) {
	// serial: selects the platform snapshot implementation through a package global.
	for _, mode := range []string{"csv", "scanner"} {
		t.Run(mode, func(t *testing.T) {
			original := cacheSnapshotGOOS
			cacheSnapshotGOOS = "linux"
			t.Cleanup(func() { cacheSnapshotGOOS = original })
			t.Setenv("MSGVAULT_FORCE_CSV_SNAPSHOT", "")
			if mode == "csv" {
				t.Setenv("MSGVAULT_FORCE_CSV_SNAPSHOT", "1")
			} else {
				duck, err := sql.Open("duckdb", "")
				require.NoError(t, err)
				defer func() { require.NoError(t, duck.Close()) }()
				requireSQLiteScanner(t, duck)
			}
			root := t.TempDir()
			dbPath, analyticsDir := filepath.Join(root, "archive.db"), filepath.Join(root, "analytics")
			fixtures, ids := legacyCalendarArchive(t, dbPath)
			_, err := buildCache(dbPath, analyticsDir, false)
			require.NoError(t, err)
			assertLegacyCalendarCache(t, dbPath, analyticsDir, fixtures, ids)
		})
	}
}

func TestBuildCache_RepairsPreviousCalendarTimePublication(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	dbPath, analyticsDir := filepath.Join(root, "archive.db"), filepath.Join(root, "analytics")
	fixtures, ids := legacyCalendarArchive(t, dbPath)
	_, err := buildCache(dbPath, analyticsDir, false)
	require.NoError(err)
	state, err := query.ReadCacheSyncState(analyticsDir)
	require.NoError(err)
	// Recreate a valid older publication with the actual +02:00 wall-clock
	// error. Refresh its file fingerprint so schema compatibility alone, not
	// corruption detection, is what requires a repair.
	duck, err := sql.Open("duckdb", "")
	require.NoError(err)
	var shard string
	require.NoError(duck.QueryRow(`SELECT filename FROM read_parquet(?, filename=true) WHERE source_message_id = 'summer'`,
		filepath.Join(analyticsDir, "messages", "**", "*.parquet")).Scan(&shard))
	staged := filepath.Join(root, "old-time.parquet")
	_, err = duck.Exec(fmt.Sprintf(`COPY (SELECT * REPLACE (
		CASE WHEN source_message_id = 'summer' THEN TIMESTAMP '2026-06-02 09:00:00'
		ELSE sent_at END AS sent_at) FROM read_parquet(?, hive_partitioning=false)) TO '%s' (FORMAT PARQUET)`, staged), shard)
	require.NoError(err)
	require.NoError(duck.Close())
	require.NoError(os.Rename(staged, shard))
	state.DatasetFingerprint, err = query.CacheDatasetFingerprint(analyticsDir)
	require.NoError(err)
	state.SchemaVersion = 35
	encoded, err := json.Marshal(state)
	require.NoError(err)
	require.NoError(os.WriteFile(query.CacheStatePath(analyticsDir), encoded, 0o600))
	stale := cacheNeedsBuild(dbPath, analyticsDir)
	require.True(stale.NeedsBuild, "the previous cache must not silently remain current")
	require.True(stale.FullRebuild)
	result, err := buildCache(dbPath, analyticsDir, false)
	require.NoError(err)
	require.False(result.Skipped)
	assertLegacyCalendarCache(t, dbPath, analyticsDir, fixtures, ids)
	current := cacheNeedsBuild(dbPath, analyticsDir)
	require.False(current.NeedsBuild)
	result, err = buildCache(dbPath, analyticsDir, false)
	require.NoError(err)
	require.True(result.Skipped, "repair runs once per publication")
}

// Keep generated offsets on the same instant, including quarter-hour zones.
func TestBuildCache_LegacyOffsetDomain(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	root := t.TempDir()
	dbPath, analyticsDir := filepath.Join(root, "archive.db"), filepath.Join(root, "analytics")
	st := testutil.NewSQLiteTestStoreAt(t, dbPath)
	source, err := st.GetOrCreateSource("gcal", "calendar@example.com")
	require.NoError(err)
	conversation, err := st.EnsureConversationWithType(source.ID, "offsets", "calendar", "Offset examples")
	require.NoError(err)
	want := time.Date(2026, 1, 1, 0, 15, 30, 123456000, time.UTC)
	for minutes := -12 * 60; minutes <= 14*60; minutes += 15 {
		_, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conversation,
			SourceMessageID: strconv.Itoa(minutes), MessageType: "calendar_event",
			SentAt: sql.NullTime{Time: want.In(time.FixedZone("", minutes*60)), Valid: true}})
		require.NoError(err)
	}
	require.NoError(st.Close())
	_, err = buildCache(dbPath, analyticsDir, false)
	require.NoError(err)
	duck, err := sql.Open("duckdb", "")
	require.NoError(err)
	defer func() { require.NoError(duck.Close()) }()
	var minimum, maximum time.Time
	var count int
	require.NoError(duck.QueryRow(`SELECT min(sent_at), max(sent_at), count(*) FROM read_parquet(?)`, filepath.Join(analyticsDir, "messages", "**", "*.parquet")).Scan(&minimum, &maximum, &count))
	assert.Equal(105, count)
	assert.Equal(want, minimum.UTC())
	assert.Equal(want, maximum.UTC())
}

// BenchmarkBuildCacheLegacyOffsets measures the one-time read-only rebuild on
// a 100,000-message archive with sender/attendee relationships. The source has
// legacy offset-bearing timestamps; message bodies are deliberately absent
// because cache publication must never read them.
func BenchmarkBuildCacheLegacyOffsets(b *testing.B) {
	original := cacheSnapshotGOOS
	cacheSnapshotGOOS = "linux"
	b.Cleanup(func() { cacheSnapshotGOOS = original })
	root := b.TempDir()
	dbPath := filepath.Join(root, "archive.db")
	st, err := store.Open(dbPath)
	require.NoError(b, err)
	require.NoError(b, st.InitSchema())
	source, err := st.GetOrCreateSource("gcal", "calendar@example.com")
	require.NoError(b, err)
	conversation, err := st.EnsureConversationWithType(source.ID, "synthetic-series", "calendar", "Synthetic events")
	require.NoError(b, err)
	organizer, err := st.EnsureParticipant("organizer@example.com", "Organizer", "example.com")
	require.NoError(b, err)
	guest, err := st.EnsureParticipant("guest@example.com", "Guest", "example.com")
	require.NoError(b, err)
	_, err = st.DB().Exec(`WITH RECURSIVE seq(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM seq WHERE i < 100000)
        INSERT INTO messages (source_id, source_message_id, conversation_id, message_type, sent_at, sender_id, subject)
        SELECT ?, CAST(i AS TEXT), ?, 'calendar_event',
               strftime('%Y-%m-%dT%H:%M:%S', '2023-01-01', printf('+%d minutes', i*17)) || '+02:00', ?, 'Synthetic event'
        FROM seq`, source.ID, conversation, organizer)
	require.NoError(b, err)
	_, err = st.DB().Exec(`INSERT INTO message_recipients (message_id, participant_id, recipient_type)
        SELECT id, ?, 'from' FROM messages UNION ALL SELECT id, ?, 'to' FROM messages`, organizer, guest)
	require.NoError(b, err)
	require.NoError(b, st.Close())
	b.ResetTimer()
	for range b.N {
		result, err := buildCache(dbPath, filepath.Join(root, "analytics"), true)
		require.NoError(b, err)
		require.Equal(b, int64(100000), result.ExportedCount)
	}
}
