package store_test

import (
	"database/sql"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/search"
	"go.kenn.io/msgvault/internal/sourcescope"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestUpstreamSearchPagesScopeBeforeRankCountAndLimit(t *testing.T) {
	assertions, requirements := assert.New(t), require.New(t)
	st := testutil.NewTestStore(t)
	var allowedSource int64
	var allowed []int64
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 2 {
		source, err := st.GetOrCreateSource("gmail", fmt.Sprintf("page-%d@example.test", i))
		requirements.NoError(err)
		conv, err := st.EnsureConversation(source.ID, "thread", "Synthetic")
		requirements.NoError(err)
		if i == 1 {
			allowedSource = source.ID
		}
		for j := range 3 {
			subject, body := "update", "glacier"
			if j == 0 || i == 0 {
				subject, body = "glacier", ""
			}
			id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conv, SourceMessageID: strconv.Itoa(j), MessageType: "email", Subject: sql.NullString{String: subject, Valid: true}, SentAt: sql.NullTime{Time: day.Add(time.Duration(i*10+j) * time.Hour), Valid: true}})
			requirements.NoError(err)
			requirements.NoError(st.UpsertFTS(id, subject, body, "", "", ""))
			if i == 1 {
				allowed = append(allowed, id)
			}
		}
	}
	want := []int64{allowed[0], allowed[2], allowed[1]}
	for _, scope := range []struct {
		name     string
		ids      []int64
		explicit bool
		total    int64
	}{
		{"delegated", []int64{allowedSource}, false, 3},
		{"explicit", []int64{allowedSource}, true, 3},
		{"empty", []int64{}, false, 0},
		{"unrestricted", nil, false, 6},
	} {
		t.Run(scope.name, func(t *testing.T) {
			assertions, requirements := assert.New(t), require.New(t)
			ctx := t.Context()
			if scope.ids != nil {
				ctx = sourcescope.WithSourceIDs(ctx, scope.ids)
			}
			q := &search.Query{TextTerms: []string{"glacier"}}
			if scope.explicit {
				ctx = t.Context()
				q.AccountIDs = scope.ids
			}
			full, total, err := st.SearchMessagesQueryContext(ctx, q, 0, 20)
			requirements.NoError(err)
			assertions.Equal(scope.total, total)
			requirements.Len(full, int(scope.total))
			for offset := range int(scope.total) + 1 {
				page, count, err := st.SearchMessagesQueryContext(ctx, q, offset, 1)
				requirements.NoError(err)
				assertions.Equal(scope.total, count)
				if offset == int(scope.total) {
					assertions.Empty(page)
					continue
				}
				requirements.Len(page, 1)
				assertions.Equal(full[offset], page[0])
				if scope.total == 3 {
					assertions.Equal(want[offset], page[0].ID)
					assertions.Equal(allowedSource, page[0].SourceID)
				}
			}
		})
	}
	// The timeline planner must use the same effective source binding, including
	// an explicitly empty binding, before it selects a page.
	engine := query.NewEngine(st.DB(), st.IsPostgreSQL())
	for _, ids := range [][]int64{{allowedSource}, {}} {
		for offset := range 3 {
			rows, err := engine.ListMessages(sourcescope.WithSourceIDs(t.Context(), ids), query.MessageFilter{Sorting: query.MessageSorting{Field: query.MessageSortByDate, Direction: query.SortDesc}, Pagination: query.Pagination{Limit: 1, Offset: offset}})
			requirements.NoError(err)
			if len(ids) == 0 {
				assertions.Empty(rows)
				continue
			}
			requirements.Len(rows, 1)
			assertions.Equal(allowed[2-offset], rows[0].ID)
			assertions.Equal(allowedSource, rows[0].SourceID)
		}
	}
}
