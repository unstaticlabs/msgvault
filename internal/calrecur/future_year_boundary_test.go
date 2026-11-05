package calrecur

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These independent UTC expectations describe ordinary winter wall times in
// the northern zones, summer wall times in Sydney, and a zone without DST.
var futureYearBoundaryZones = []struct {
	name          string
	offsetSeconds int
}{
	{"Europe/Paris", 3600},
	{"America/New_York", -5 * 3600},
	{"Australia/Sydney", 11 * 3600},
	{"Pacific/Kiritimati", 14 * 3600},
}

func TestForwardZoneBoundsPreservesNativeIntervalsAndAdvances(t *testing.T) {
	for _, zone := range futureYearBoundaryZones {
		t.Run(zone.name, func(t *testing.T) {
			assertions, requirements := assert.New(t), require.New(t)
			loc, err := time.LoadLocation(zone.name)
			requirements.NoError(err)
			for _, year := range []int{2040, 2044} {
				for day := 29; day <= 34; day++ {
					cursor := time.Date(year, time.December, day, 23, 59, 59, 123, time.UTC).In(loc)
					nativeStart, nativeEnd := cursor.ZoneBounds()
					start, end := forwardZoneBounds(cursor)
					assertions.Equal(nativeStart, start)
					if nativeEnd.IsZero() {
						assertions.True(end.IsZero())
					} else {
						assertions.True(end.After(cursor), "forward interval must advance: %s", cursor)
						if nativeEnd.After(cursor) {
							assertions.Equal(nativeEnd, end, "a valid native bound must remain unchanged")
						} else {
							assertions.True(end.Equal(time.Date(cursor.UTC().Year()+1, time.January, 1, 0, 0, 0, 0, time.UTC)))
						}
					}
				}
			}
		})
	}
}

func TestFutureLeapYearWallTimesAndCivilBoundaries(t *testing.T) {
	for _, zone := range futureYearBoundaryZones {
		for _, year := range []int{2040, 2044} {
			t.Run(fmt.Sprintf("%s/%d", zone.name, year), func(t *testing.T) {
				assertions, requirements := assert.New(t), require.New(t)
				loc, err := time.LoadLocation(zone.name)
				requirements.NoError(err)
				for day := 29; day <= 34; day++ {
					civil := time.Date(year, time.December, day, 0, 0, 0, 0, time.UTC)
					expected := civil.Add(-time.Duration(zone.offsetSeconds) * time.Second)
					boundary, exists, err := CivilDateBoundary(civil.Format(dateLayout), loc)
					requirements.NoError(err)
					assertions.True(exists)
					assertions.True(boundary.Equal(expected), "boundary for %s: got %s, want %s", civil.Format(dateLayout), boundary, expected)
					for _, clock := range []time.Duration{0, 12*time.Hour + 34*time.Minute + 56*time.Second, 24*time.Hour - time.Second} {
						anchor := civil.Add(clock)
						expected := anchor.Add(-time.Duration(zone.offsetSeconds) * time.Second)
						resolved, exists := firstLocalWallTime(anchor, loc)
						assertions.True(exists, "wall time %s exists", anchor)
						assertions.True(resolved.Equal(expected), "wall time %s: got %s, want %s", anchor, resolved, expected)
						parsed, err := parseLocalDateTime("20060102T150405", anchor.Format("20060102T150405"), loc)
						requirements.NoError(err)
						assertions.True(parsed.Equal(expected))
					}
				}
			})
		}
	}
}

func TestFutureLeapYearExplicitDatesAndExpansion(t *testing.T) {
	for _, zone := range futureYearBoundaryZones {
		for _, year := range []int{2040, 2044} {
			t.Run(fmt.Sprintf("%s/%d", zone.name, year), func(t *testing.T) {
				assertions, requirements := assert.New(t), require.New(t)
				loc, err := time.LoadLocation(zone.name)
				requirements.NoError(err)
				offset := time.Duration(zone.offsetSeconds) * time.Second
				civil := time.Date(year, time.December, 29, 9, 0, 0, 0, time.UTC)
				start := civil.Add(-offset)
				extra := time.Date(year, time.December, 31, 15, 30, 0, 0, time.UTC)
				excluded := civil.AddDate(0, 0, 1)
				s, err := ParseSet(start, false, "", loc, []string{
					"RRULE:FREQ=DAILY;COUNT=6",
					"RDATE;TZID=" + zone.name + ":" + extra.Format("20060102T150405"),
					"EXDATE;TZID=" + zone.name + ":" + excluded.Format("20060102T150405"),
				})
				requirements.NoError(err)
				requirements.Len(s.RDates, 1)
				requirements.Len(s.ExDates, 1)
				assertions.True(s.RDates[0].Instant.Equal(extra.Add(-offset)))
				assertions.True(s.ExDates[0].Instant.Equal(excluded.Add(-offset)))
				occurrences, _, err := Expand(t.Context(), s, time.Hour, 0, start.Add(-time.Hour), start.AddDate(0, 0, 7), 100)
				requirements.NoError(err)
				expectedCivil := []time.Time{civil, civil.AddDate(0, 0, 2), extra, civil.AddDate(0, 0, 3), civil.AddDate(0, 0, 4), civil.AddDate(0, 0, 5)}
				requirements.Len(occurrences, len(expectedCivil))
				for i, want := range expectedCivil {
					assertions.True(occurrences[i].Start.Equal(want.Add(-offset)), "occurrence %d: got %s, want %s", i, occurrences[i].Start, want.Add(-offset))
					assertions.Equal(time.Hour, occurrences[i].End.Sub(occurrences[i].Start))
				}

				allDay, err := ParseSet(time.Time{}, true, civil.Format(dateLayout), loc, []string{
					"RRULE:FREQ=DAILY;COUNT=6",
					"RDATE;VALUE=DATE:" + civil.AddDate(0, 0, 6).Format(keyDateLayout),
					"EXDATE;VALUE=DATE:" + excluded.Format(keyDateLayout),
				})
				requirements.NoError(err)
				days, _, err := Expand(t.Context(), allDay, 0, 1, civil.AddDate(0, 0, -1), civil.AddDate(0, 0, 8), 100)
				requirements.NoError(err)
				requirements.Len(days, 6)
				for i, dayOffset := range []int{0, 2, 3, 4, 5, 6} {
					date := time.Date(year, time.December, 29+dayOffset, 0, 0, 0, 0, time.UTC)
					assertions.Equal(date.Format(dateLayout), days[i].StartDate)
					assertions.True(days[i].Start.Equal(date.Add(-offset)))
					assertions.True(days[i].End.Equal(date.AddDate(0, 0, 1).Add(-offset)))
				}
			})
		}
	}
}
