package query

import (
	"context"
	"fmt"
	"go.kenn.io/msgvault/internal/store"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExploreEmailWorkItemsRetainsPermittedFamilyAfterWitnessFilters(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	engine := emailWorkPopulationFixture(t)
	ctx := context.Background()
	individual, err := engine.Explore(ctx, ExploreRequest{})
	requirements.NoError(err)
	assertions.Equal(int64(3), individual.TotalCount)
	work := ExploreRequest{Context: Context{RowUnit: RowUnitEmailWork, WorkAuthorityKey: "qa-reader"}}
	result, err := engine.Explore(ctx, work)
	requirements.NoError(err)
	requirements.Len(result.Rows, 1)
	assertions.Equal(int64(1), result.TotalCount)
	row := result.Rows[0]
	assertions.Equal(EntryEmailWork, row.Kind)
	assertions.Equal(int64(3), row.MessageCount)
	requirements.NotNil(row.EmailWork)
	assertions.Equal([]int64{1, 2, 3}, row.EmailWork.SourceIDs)
	assertions.Equal("actionable", row.EmailWork.State)
	assertions.Equal(1, row.EmailWork.OutstandingCount)
	before := time.Date(2026, 1, 1, 1, 30, 0, 0, time.UTC)
	work.Context.SourceIDs = []int64{1, 2}
	work.Context.Before = &before
	result, err = engine.Explore(ctx, work)
	requirements.NoError(err)
	requirements.Len(result.Rows, 1)
	assertions.Equal(int64(2), result.Rows[0].MessageCount, "the outbound that missed the date predicate remains part of its permitted family")
	assertions.Equal("handled", result.Rows[0].EmailWork.State)
	assertions.True(result.Rows[0].OccurredAt.Before(before), "the visible time and anchor belong to a matching copy while handling retains its later reply")
	assertions.Equal(int64(1), result.Rows[0].SourceID)
	assertions.Equal([]int64{1, 2}, result.Rows[0].EmailWork.SourceIDs)
	work.Context.SourceIDs = []int64{1, 3}
	work.Context.Before = nil
	result, err = engine.Explore(ctx, work)
	requirements.NoError(err)
	assertions.Equal(int64(2), result.TotalCount, "excluded B cannot connect A/C in the actual row query")
	for _, row := range result.Rows {
		assertions.Equal(int64(1), row.MessageCount)
		assertions.NotContains(row.EmailWork.SourceIDs, int64(2))
	}
	work.Context.SourceIDs = nil
	work.Page = PageSpec{Limit: 1, Offset: 10}
	result, err = engine.Explore(ctx, work)
	requirements.NoError(err)
	assertions.Empty(result.Rows)
	assertions.Equal(int64(1), result.TotalCount)
}

func TestExploreEmailWorkGroupsSelectionAndSearchAgree(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	engine := emailWorkPopulationFixture(t)
	ctx := context.Background()
	request := ExploreRequest{Context: Context{RowUnit: RowUnitEmailWork, WorkAuthorityKey: "qa-reader"}}
	result, err := engine.Explore(ctx, request)
	requirements.NoError(err)
	requirements.Len(result.Rows, 1)
	key := result.Rows[0].Key
	groups, err := engine.ExploreGroups(ctx, ExploreGroupRequest{Explore: request, Dimension: "source"})
	requirements.NoError(err)
	requirements.Len(groups.Rows, 3)
	sourceKeys := map[string]int64{}
	for _, group := range groups.Rows {
		sourceKeys[group.Key] = group.Count
	}
	assertions.Equal(map[string]int64{"1": 1, "2": 1, "3": 1}, sourceKeys, "each scoped account witnesses the same work unit, rather than only its newest anchor")
	selection, err := engine.ExploreSelectionStats(ctx, ExploreSelectionRequest{Explore: request, IncludedKeys: []string{key}, IncludeDeletableMessageIDs: true})
	requirements.NoError(err)
	assertions.Equal(int64(1), selection.Count)
	assertions.Zero(selection.DeletableCount, "consolidated provider mutation remains unavailable")
	assertions.Empty(selection.DeletableMessageIDs)
	assertions.Nil(selection.RawExportMessageID, "a family must not silently raw-export only its newest copy")
	_, err = engine.ExploreSelectionStats(ctx, ExploreSelectionRequest{Explore: request, IncludedKeys: []string{"email-work:" + fmt.Sprintf("%032x", 3)}})
	requirements.ErrorIs(err, ErrEmailWorkRevisionChanged, "an old scoped alias must not silently relocate an explicit selection after union")

	request.Search = SearchSpec{Mode: SearchFullText, Query: "[QA]", CandidateMessageIDs: []int64{1}, LexicalIndexRevision: "fts5:qa"}
	searched, err := engine.Explore(ctx, request)
	requirements.NoError(err)
	requirements.Len(searched.Rows, 1)
	assertions.Equal(key, searched.Rows[0].Key)
	assertions.Equal(int64(3), searched.Rows[0].MessageCount)
	matched, err := engine.ExploreMatchCounts(ctx, ExploreMatchCountsRequest{Explore: request, RowKeys: []string{key}})
	requirements.NoError(err)
	assertions.Equal(int64(1), matched.Counts[key], "one matching copy is distinct from three retained source messages")
	groups, err = engine.ExploreGroups(ctx, ExploreGroupRequest{Explore: request, Dimension: "participant"})
	requirements.NoError(err)
	var foundOutboundOwner bool
	for _, group := range groups.Rows {
		if group.Key == "3" {
			foundOutboundOwner = true
			assertions.Equal(int64(1), group.Count)
		}
	}
	assertions.True(foundOutboundOwner, "the permitted outbound owner remains discoverable although it was not the search witness")
}

func TestEmailWorkMembersPreservesProvenanceAliasesScopeAndRevision(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	engine := emailWorkPopulationFixture(t)
	ctx := context.Background()
	explore := ExploreRequest{Context: Context{RowUnit: RowUnitEmailWork, WorkAuthorityKey: "qa-reader"}}
	rows, err := engine.Explore(ctx, explore)
	requirements.NoError(err)
	request := EmailWorkMembersRequest{Explore: explore, UID: fmt.Sprintf("%032x", 3), ExpectedCacheRevision: rows.CacheRevision}
	family, err := engine.EmailWorkMembers(ctx, request)
	requirements.NoError(err)
	assertions.Equal(fmt.Sprintf("%032x", 1), family.UID)
	assertions.Equal(request.UID, family.RequestedUID, "old alias follows its own current scoped anchor")
	requirements.Len(family.Members, 3)
	for i, member := range family.Members {
		assertions.Equal(int64(i+1), member.MessageID)
		assertions.Equal(fmt.Sprintf("%032x", i+1), member.MessageUID)
		assertions.Equal(int64(i+1), member.SourceID)
		assertions.Equal(fmt.Sprintf("msg%d", i+1), member.SourceMessageID)
	}
	assertions.Equal([]int64{3}, family.OutstandingMessageIDs)
	assertions.Equal([]int64{2}, family.EvidenceMessageIDs)
	scoped := NewScopedEngine(engine, []int64{1, 3})
	reader, ok := scoped.(EmailWorkExplorer)
	requirements.True(ok)
	family, err = reader.EmailWorkMembers(ctx, request)
	requirements.NoError(err)
	assertions.Equal(request.UID, family.UID)
	requirements.Len(family.Members, 1)
	assertions.Equal(int64(3), family.Members[0].SourceID)
	denied := NewScopedEngine(engine, []int64{})
	deniedReader, ok := denied.(EmailWorkExplorer)
	requirements.True(ok)
	_, err = deniedReader.EmailWorkMembers(ctx, request)
	requirements.ErrorIs(err, store.ErrEmailWorkAliasOutOfScope)
	deniedExplorer, ok := denied.(Explorer)
	requirements.True(ok)
	empty, err := deniedExplorer.Explore(ctx, explore)
	requirements.NoError(err)
	assertions.Zero(empty.TotalCount)
	request.Page = PageSpec{Limit: 1, Offset: 1}
	family, err = engine.EmailWorkMembers(ctx, request)
	requirements.NoError(err)
	requirements.Len(family.Members, 1)
	assertions.Equal(int64(2), family.Members[0].MessageID)
	request.ExpectedCacheRevision = "old-cache-publication"
	_, err = engine.EmailWorkMembers(ctx, request)
	requirements.ErrorIs(err, ErrEmailWorkRevisionChanged)
}

func TestEmailWorkMembersRejectsLiveDeletedOrReusedAnchors(t *testing.T) {
	requirements := require.New(t)
	engine := emailWorkPopulationFixture(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	requirements.NoError(err)
	t.Cleanup(func() { requirements.NoError(st.Close()) })
	requirements.NoError(st.InitSchema())
	for i := 1; i <= 3; i++ {
		source, err := st.GetOrCreateSource("gmail", fmt.Sprintf("owner-%d@example.test", i))
		requirements.NoError(err)
		conversation, err := st.EnsureConversation(source.ID, "qa-live", "[QA] Live identity")
		requirements.NoError(err)
		id, err := st.PersistMessage(&store.MessagePersistData{Message: &store.Message{SourceID: source.ID, ConversationID: conversation, SourceMessageID: fmt.Sprintf("msg%d", i), MessageType: "email"}, RawMIME: []byte(fmt.Sprintf("Message-ID: <message-%d@example.test>\r\n\r\n[QA]", i))})
		requirements.NoError(err)
		// Give both disposable fixtures the same opaque namespace; allocation,
		// deletion tombstones and replacement are still the actual native lifecycle.
		_, err = st.DB().Exec("UPDATE email_work_seeds SET uid=? WHERE live_message_id=?", fmt.Sprintf("%032x", i), id)
		requirements.NoError(err)
	}
	engine.sqliteDB = st.DB()
	request := EmailWorkMembersRequest{Explore: ExploreRequest{Context: Context{RowUnit: RowUnitEmailWork, WorkAuthorityKey: "qa-reader"}}, UID: fmt.Sprintf("%032x", 3)}
	_, err = engine.EmailWorkMembers(context.Background(), request)
	requirements.NoError(err)
	_, err = st.DB().Exec("DELETE FROM messages WHERE id=3")
	requirements.NoError(err)
	source, err := st.GetSourceByID(3)
	requirements.NoError(err)
	conversation, err := st.EnsureConversation(source.ID, "new-live", "[QA] Unrelated replacement")
	requirements.NoError(err)
	id, err := st.PersistMessage(&store.MessagePersistData{Message: &store.Message{SourceID: source.ID, ConversationID: conversation, SourceMessageID: "replacement", MessageType: "email"}, RawMIME: []byte("Message-ID: <unrelated@example.test>\r\n\r\n[QA]")})
	requirements.NoError(err)
	requirements.Equal(int64(3), id, "SQLite fixture must actually reuse the deleted physical ID")
	_, err = engine.EmailWorkMembers(context.Background(), request)
	requirements.ErrorIs(err, store.ErrEmailWorkAliasGone)
	request.UID = fmt.Sprintf("%032x", 1)
	_, err = engine.EmailWorkMembers(context.Background(), request)
	requirements.ErrorIs(err, ErrEmailWorkRevisionChanged, "an old family must not drill into a reused member either")
}

func TestEmailWorkMemberPageRetainsRequestedImmutableAnchor(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	engine := emailWorkPopulationFixture(t)
	response, err := engine.EmailWorkMembers(context.Background(), EmailWorkMembersRequest{Explore: ExploreRequest{Context: Context{RowUnit: RowUnitEmailWork, WorkAuthorityKey: "synthetic-reader"}}, UID: fmt.Sprintf("%032x", 3), Page: PageSpec{Limit: 1}})
	requirements.NoError(err)
	requirements.Len(response.Members, 1)
	assertions.Equal(int64(1), response.Members[0].MessageID)
	requirements.NotNil(response.RequestedAnchor)
	assertions.Equal(int64(3), response.RequestedAnchor.MessageID)
	assertions.Equal(fmt.Sprintf("%032x", 3), response.RequestedAnchor.MessageUID)
	assertions.Equal(int64(3), response.RequestedAnchor.SourceID)
}
