package query

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Grow the actual native Parquet publication in bounded range queries, rather
// than simulating a memo hit or issuing one fixture insert per message.
func emailWorkScaleFixture(t *testing.T) *DuckDBEngine {
	t.Helper()
	e := emailWorkPopulationFixture(t)
	for _, spec := range []struct{ dataset, selectSQL string }{
		{"messages", `SELECT m.* REPLACE (g.id AS id, 'scale-'||g.id AS source_message_id, TIMESTAMP '2006-01-01'+(g.id%7300)*INTERVAL 1 DAY AS sent_at) FROM base m,range(1000,101000) g(id) WHERE m.id=1`},
		{DatasetEmailWorkFacts, `SELECT g.id AS message_id,extraction_version,outcome,replace(facts,'message-1@example.test','scale-'||g.id||'@example.test') AS facts FROM base m,range(1000,101000) g(id) WHERE m.message_id=1`},
		{DatasetEmailWorkSeeds, `SELECT g.id AS ordinal,lpad(lower(to_hex(g.id)),32,'0') AS uid,g.id AS anchor_message_id,g.id AS live_message_id FROM range(1000,101000) g(id)`},
		{DatasetEmailWorkDelivery, `SELECT g.id AS message_id,1::BIGINT AS source_id,CASE WHEN g.id<2200 THEN 'synthetic-wide-family' ELSE NULL END AS provider_thread_id,false AS native_sent,false AS native_draft,true AS provider_complete FROM range(1000,101000) g(id)`},
	} {
		var original string
		pattern := e.parquetPath(spec.dataset)
		if spec.dataset == "messages" {
			pattern = filepath.Join(e.analyticsDir, "messages", "*", "*.parquet")
		}
		err := e.db.QueryRow("SELECT file FROM glob(?) LIMIT 1", pattern).Scan(&original)
		require.NoError(t, err)
		matches := []string{original}
		output := filepath.Join(filepath.Dir(matches[0]), "scale.parquet")
		statement := fmt.Sprintf("COPY (WITH base AS (SELECT * FROM read_parquet('%s')) SELECT * FROM base UNION ALL %s) TO '%s' (FORMAT PARQUET)", matches[0], spec.selectSQL, output)
		_, err = e.db.Exec(statement)
		require.NoError(t, err)
		require.NoError(t, os.Remove(matches[0]))
	}
	fingerprint, err := CacheDatasetFingerprint(e.analyticsDir)
	require.NoError(t, err)
	state, err := ReadCacheSyncState(e.analyticsDir)
	require.NoError(t, err)
	state.DatasetFingerprint = fingerprint
	data, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(CacheStatePath(e.analyticsDir), data, 0o600))
	return e
}

func TestEmailWorkLargeScopeReusesPublication(t *testing.T) {
	checks := assert.New(t)
	must := require.New(t)
	e := emailWorkScaleFixture(t)
	ctx := context.Background()
	read := func() *emailWorkPopulation {
		release, err := e.acquireQuerySlot(ctx)
		must.NoError(err)
		defer release()
		p, err := e.emailWorkPopulationLocked(ctx, EmailWorkScope{AuthorityKey: "synthetic-scale"})
		must.NoError(err)
		return p
	}
	cold := time.Now()
	first := read()
	t.Logf("cold native projection elapsed=%s charged bytes=%d", time.Since(cold), first.builtCost)
	checks.Greater(first.builtCost, int64(emailWorkMemoBudget))
	checks.Empty(first.activities)
	warm := time.Now()
	second := read()
	t.Logf("warm native projection elapsed=%s", time.Since(warm))
	must.Same(first, second, "oversized scoped publication must not repartition for another request")
	request := ExploreRequest{Context: Context{RowUnit: RowUnitEmailWork, WorkAuthorityKey: "synthetic-scale"}, Page: PageSpec{Limit: 5}}
	result, err := e.Explore(ctx, request)
	must.NoError(err)
	checks.Equal(int64(98802), result.TotalCount)
	must.Len(result.Rows, 5)
	sequence := e.tempTableSeq.Load()
	targetStarted := time.Now()
	for id := int64(2200); id < 2300; id++ {
		response, err := e.EmailWorkMembers(ctx, EmailWorkMembersRequest{Explore: request, UID: fmt.Sprintf("%032x", id), ExpectedCacheRevision: first.revision, Page: PageSpec{Limit: 1}})
		must.NoError(err)
		must.Len(response.Members, 1)
		checks.Equal(id, response.Members[0].MessageID)
	}
	t.Logf("100 fixed native target reads elapsed=%s", time.Since(targetStarted))
	for offset := 0; offset < 1200; offset += 500 {
		response, err := e.EmailWorkMembers(ctx, EmailWorkMembersRequest{Explore: request, UID: fmt.Sprintf("%032x", 1000), ExpectedCacheRevision: first.revision, Page: PageSpec{Limit: 500, Offset: offset}})
		must.NoError(err)
		checks.Equal(1200, response.TotalCount)
		must.Len(response.Members, min(500, 1200-offset))
		checks.Equal(int64(1000+offset), response.Members[0].MessageID)
	}
	checks.Equal(sequence, e.tempTableSeq.Load(), "repeated page/target reads create no new native projection tables")
	var planKey, plan string
	err = e.db.QueryRowContext(ctx, "EXPLAIN ANALYZE SELECT component FROM "+first.relations.aliases+" WHERE uid=?", fmt.Sprintf("%032x", 2200)).Scan(&planKey, &plan)
	must.NoError(err)
	checks.Contains(plan, "Index Scan", "fixed opaque targets use the native UID index rather than scanning the whole projection")
	t.Logf("native alias lookup profile: %s", plan)
	var nativeMemory, nativeSpill int64
	must.NoError(e.db.QueryRowContext(ctx, "SELECT SUM(memory_usage_bytes),SUM(temporary_storage_bytes) FROM duckdb_memory()").Scan(&nativeMemory, &nativeSpill))
	t.Logf("native memory bytes=%d; observed temporary spill bytes=%d (separate from retained Go charge)", nativeMemory, nativeSpill)
	var retained int64
	for _, handle := range e.emailWorkMemo {
		retained += handle.cost
	}
	checks.LessOrEqual(retained, int64(emailWorkMemoBudget))
}
