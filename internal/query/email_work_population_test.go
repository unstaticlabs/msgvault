package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/mime"
	"go.kenn.io/msgvault/internal/store"
	"os"
)

func TestEmailWorkPopulationScopesBeforeLinkageAndHandling(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	engine := emailWorkPopulationFixture(t)
	read := func(sources []int64, authority string) *emailWorkPopulation {
		release, err := engine.acquireQuerySlot(context.Background())
		requirements.NoError(err)
		defer release()
		population, err := engine.emailWorkPopulationLocked(context.Background(), EmailWorkScope{SourceIDs: sources, AuthorityKey: authority})
		requirements.NoError(err)
		return population
	}
	ac := read([]int64{1, 3}, "qa-reader")
	assertions.Equal([][]int64{{1}, {3}}, ac.components, "A/C connected only through excluded B must remain separate")
	assertions.Len(ac.activities, 2)
	assertions.NotContains(ac.activities, int64(2))
	_, err := ac.resolveAlias(fmt.Sprintf("%032x", 2))
	requirements.ErrorIs(err, store.ErrEmailWorkAliasOutOfScope)
	component, err := ac.resolveAlias(fmt.Sprintf("%032x", 3))
	requirements.NoError(err)
	assertions.Equal(fmt.Sprintf("%032x", 3), ac.canonicalSeeds[component].UID)
	ab := read([]int64{1, 2}, "qa-reader")
	assertions.Equal([][]int64{{1, 2}}, ab.components)
	requirements.Len(ab.handling, 1)
	assertions.Equal("handled", ab.handling[0].State)
	assertions.Equal([]int64{2}, ab.handling[0].OutboundEvidenceIDs)
	assertions.Empty(ab.handling[0].OutstandingEventIDs)
	abc := read([]int64{1, 2, 3}, "qa-reader")
	assertions.Equal([][]int64{{1, 2, 3}}, abc.components)
	requirements.Len(abc.handling, 1)
	assertions.Equal("actionable", abc.handling[0].State, "newer inbound opens the received work again")
	assertions.Equal([]int64{3}, abc.handling[0].OutstandingEventIDs)
	component, err = abc.resolveAlias(fmt.Sprintf("%032x", 3))
	requirements.NoError(err)
	assertions.Equal(fmt.Sprintf("%032x", 1), abc.canonicalSeeds[component].UID, "late union resolves from the immutable anchor to the scoped oldest allocation")
	assertions.Same(abc, read([]int64{3, 2, 1, 1}, "qa-reader"), "source order and duplicates cannot rebuild the same publication projection")
	assertions.NotSame(abc, read([]int64{1, 2, 3}, "different-effective-authority"))
	all := read(nil, "qa-reader")
	assertions.Equal(abc.components, all.components)
	none := read([]int64{}, "qa-reader")
	assertions.Empty(none.components, "an explicitly empty source scope is not all sources")
	state, err := ReadCacheSyncState(engine.analyticsDir)
	requirements.NoError(err)
	state.PublishedAt = state.PublishedAt.Add(time.Second)
	encoded, err := json.Marshal(state)
	requirements.NoError(err)
	requirements.NoError(os.WriteFile(CacheStatePath(engine.analyticsDir), encoded, 0o600))
	revised := read([]int64{1, 2, 3}, "qa-reader")
	assertions.NotSame(abc, revised, "another committed publication cannot reuse stale component or handling facts")
	assertions.NotEqual(abc.revision, revised.revision)
}

func TestEmailWorkPopulationRejectsUntrustedScopeAndBoundsMemo(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	engine := emailWorkPopulationFixture(t)
	release, err := engine.acquireQuerySlot(context.Background())
	requirements.NoError(err)
	defer release()
	_, err = engine.emailWorkPopulationLocked(context.Background(), EmailWorkScope{})
	requirements.ErrorIs(err, ErrInvalidExploreRequest)
	_, err = engine.emailWorkPopulationLocked(context.Background(), EmailWorkScope{AuthorityKey: "qa", SourceIDs: []int64{-1}})
	requirements.ErrorIs(err, ErrInvalidExploreRequest)
	for i := range 10 {
		_, err = engine.emailWorkPopulationLocked(context.Background(), EmailWorkScope{AuthorityKey: fmt.Sprintf("effective-authority-%d", i)})
		requirements.NoError(err)
	}
	assertions.Len(engine.emailWorkMemo, emailWorkMemoEntries)
	var cost int64
	for _, population := range engine.emailWorkMemo {
		cost += population.cost
	}
	assertions.LessOrEqual(cost, int64(emailWorkMemoBudget))
}

func TestEmailWorkPopulationPreservesUnavailableEvidence(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	engine := emailWorkPopulationFixture(t, true)
	release, err := engine.acquireQuerySlot(context.Background())
	requirements.NoError(err)
	defer release()
	population, err := engine.emailWorkPopulationLocked(context.Background(), EmailWorkScope{AuthorityKey: "qa-reader"})
	requirements.NoError(err)
	assertions.Len(population.activities, 3, "unavailable extraction or provider evidence never drops readable mail")
	assertions.Equal("invalid_facts", population.activities[1].Linkage.Outcome)
	assertions.False(population.activities[2].AttributionComplete)
	assertions.Contains(population.activities[2].UnavailableReasons, "provider_evidence_unavailable")
	for _, handling := range population.handling {
		assertions.NotEqual("handled", handling.State)
	}
}

func emailWorkPopulationFixture(t *testing.T, unavailable ...bool) *DuckDBEngine {
	t.Helper()
	requirements := require.New(t)
	b := NewTestDataBuilder(t)
	var facts, seeds, identities, delivery []string
	for i := 1; i <= 3; i++ {
		owner := fmt.Sprintf("owner-%d@example.test", i)
		source := b.AddSource(owner)
		id := b.AddMessage(MessageOpt{SourceID: source, Subject: "[QA] same subject", SentAt: time.Date(2026, 1, 1, i, 0, 0, 0, time.UTC)})
		sender, recipient := "external@example.test", owner
		sent := false
		if i == 2 {
			sender = owner
			recipient = "external@example.test"
			sent = true
		}
		for _, envelope := range []struct{ kind, address string }{{"from", sender}, {"to", recipient}} {
			participant := b.AddParticipant(envelope.address, "example.test", "[QA]")
			b.AddRecipientWithEnvelope(id, participant, envelope.kind, "[QA]", envelope.address)
		}
		raw := fmt.Sprintf("Message-ID: <message-%d@example.test>\r\n", i)
		if i > 1 {
			raw += fmt.Sprintf("In-Reply-To: <message-%d@example.test>\r\n", i-1)
		}
		raw += "\r\n[QA] body"
		encoded, err := json.Marshal(mime.ExtractEmailLinkage([]byte(raw)))
		requirements.NoError(err)
		if len(unavailable) > 0 && unavailable[0] && i == 1 {
			encoded = []byte("{broken")
		}
		facts = append(facts, fmt.Sprintf("(%d::BIGINT,1::INTEGER,'complete',%s)", id, sqlStr(string(encoded))))
		seeds = append(seeds, fmt.Sprintf("(%d::BIGINT,'%032x',%d::BIGINT,%d::BIGINT)", i, i, id, id))
		identities = append(identities, fmt.Sprintf("(%d::BIGINT,%s)", source, sqlStr(owner)))
		complete := len(unavailable) == 0 || !unavailable[0] || i != 2
		delivery = append(delivery, fmt.Sprintf("(%d::BIGINT,%d::BIGINT,NULL::VARCHAR,%t,false,%t)", id, source, sent, complete))
	}
	pb := newParquetBuilder(t)
	b.addMessageTables(pb)
	b.addAuxiliaryTables(pb)
	b.addAttachmentsTable(pb)
	for _, spec := range []struct{ name, cols, rows string }{
		{DatasetEmailWorkFacts, "message_id, extraction_version, outcome, facts", strings.Join(facts, ",")},
		{DatasetEmailWorkSeeds, "ordinal, uid, anchor_message_id, live_message_id", strings.Join(seeds, ",")},
		{DatasetEmailWorkIdentities, "source_id, address", strings.Join(identities, ",")},
		{DatasetEmailWorkDelivery, "message_id, source_id, provider_thread_id, native_sent, native_draft, provider_complete", strings.Join(delivery, ",")},
	} {
		pb.addTable(spec.name, spec.name, spec.name+".parquet", spec.cols, spec.rows)
	}
	root, cleanup := pb.build()
	t.Cleanup(cleanup)
	engine, err := NewDuckDBEngine(root, "", nil)
	requirements.NoError(err)
	t.Cleanup(func() { requirements.NoError(engine.Close()) })
	return engine
}

func TestEmailWorkNativeProjectionLifecycle(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	e := emailWorkPopulationFixture(t)
	ctx := context.Background()
	read := func(authority string) *emailWorkPopulation {
		release, err := e.acquireQuerySlot(ctx)
		requirements.NoError(err)
		defer release()
		population, err := e.emailWorkPopulationLocked(ctx, EmailWorkScope{AuthorityKey: authority})
		requirements.NoError(err)
		return population
	}
	oldest := read("synthetic-first")
	oldNames := oldest.relations.names()
	for i := range 4 {
		read(fmt.Sprintf("synthetic-authority-%d", i))
	}
	assertions.Len(e.emailWorkMemo, 4)
	for _, name := range oldNames {
		var count int
		err := e.db.QueryRow("SELECT COUNT(*) FROM " + name).Scan(&count)
		requirements.Error(err, "fifth-scope eviction drops the entire native handle")
	}
	var tableCount int
	requirements.NoError(e.db.QueryRow("SELECT COUNT(*) FROM duckdb_tables() WHERE table_name LIKE '_email_work_projection_%'").Scan(&tableCount))
	assertions.Equal(16, tableCount)
	incumbent := e.emailWorkMemo[0]
	release, err := e.acquireQuerySlot(ctx)
	requirements.NoError(err)
	uncommitted, err := e.readEmailWorkPopulation(ctx, nil)
	requirements.NoError(err)
	// Native DDL has been created when a real primary-key constraint fails.
	// An invalid duplicated partition must not become a published handle.
	uncommitted.components[0] = append(uncommitted.components[0], uncommitted.components[0][0])
	requirements.Error(e.materializeEmailWorkPopulation(ctx, uncommitted))
	for _, name := range uncommitted.relations.names() {
		var count int
		requirements.Error(e.db.QueryRow("SELECT COUNT(*) FROM " + name).Scan(&count))
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	requirements.ErrorIs(e.materializeEmailWorkPopulation(cancelled, uncommitted), context.Canceled)
	release()
	assertions.Same(incumbent, read("synthetic-authority-3"), "a failed build retains prior usable projections")
	state, err := ReadCacheSyncState(e.analyticsDir)
	requirements.NoError(err)
	state.PublishedAt = state.PublishedAt.Add(time.Second)
	data, err := json.Marshal(state)
	requirements.NoError(err)
	requirements.NoError(os.WriteFile(CacheStatePath(e.analyticsDir), data, 0o600))
	replacement := read("synthetic-authority-3")
	assertions.NotSame(incumbent, replacement)
	assertions.Len(e.emailWorkMemo, 1, "a new publication retires old native handles")
	requirements.NoError(e.db.QueryRow("SELECT COUNT(*) FROM duckdb_tables() WHERE table_name LIKE '_email_work_projection_%'").Scan(&tableCount))
	assertions.Equal(4, tableCount)
	ownedTemp := e.tempDirectory
	requirements.NoError(e.Close())
	assertions.Nil(e.emailWorkMemo)
	if e.ownTempDirectory {
		_, err := os.Stat(ownedTemp)
		assertions.True(os.IsNotExist(err))
	}
	_, err = os.Stat(CacheStatePath(e.analyticsDir))
	assertions.NoError(err, "Close preserves the committed archive publication")
}
