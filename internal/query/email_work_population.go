package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"go.kenn.io/msgvault/internal/mime"
	"go.kenn.io/msgvault/internal/store"
)

// EmailWorkScope is trusted server-side population scope, never a request's
// assertion of authority. Nil SourceIDs means all sources the authority allows;
// a non-nil empty set means none. Unequal access scopes cannot share memoized facts.
type EmailWorkScope struct {
	SourceIDs    []int64
	AuthorityKey string `json:"-"`
}

type emailWorkPopulation struct {
	relations           emailWorkRelations
	builtCost           int64
	revision            string
	recoverySnapshot    string
	key                 string
	activities          map[int64]store.EmailWorkActivity
	components          [][]int64
	seeds               map[string]store.EmailWorkSeed
	componentForMessage map[int64]int
	handling            []store.EmailWorkHandling
	canonicalSeeds      []store.EmailWorkSeed
	cost                int64
	localActions        []store.EmailWorkAction
}

const emailWorkMemoBudget = 64 << 20
const emailWorkMemoEntries = 4

// emailWorkPopulationLocked requires the native query slot and shared cache
// read lock; callers retain them through every use of this immutable projection.
func (e *DuckDBEngine) emailWorkPopulationLocked(ctx context.Context, scope EmailWorkScope) (*emailWorkPopulation, error) {
	if scope.AuthorityKey == "" {
		return nil, fmt.Errorf("%w: missing effective work-item authority", ErrInvalidExploreRequest)
	}
	sources := slices.Clone(scope.SourceIDs)
	for _, id := range sources {
		if id < 1 {
			return nil, fmt.Errorf("%w: source IDs must be positive", ErrInvalidExploreRequest)
		}
	}
	slices.Sort(sources)
	sources = slices.Compact(sources)
	state, err := ReadCacheSyncState(e.analyticsDir)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(struct {
		Authority string  `json:"authority"`
		Sources   []int64 `json:"sources"`
	}{scope.AuthorityKey, sources})
	if err != nil {
		return nil, err
	}
	key := state.Revision() + ":" + string(encoded)
	e.emailWorkMu.Lock()
	defer e.emailWorkMu.Unlock()
	for i, population := range e.emailWorkMemo {
		if population.key == key {
			e.emailWorkMemo = append(append([]*emailWorkPopulation{population}, e.emailWorkMemo[:i]...), e.emailWorkMemo[i+1:]...)
			return population, nil
		}
	}
	// Query serialization means no other request can still borrow an evicted
	// handle. Drop transactionally before replacement to bound native resources.
	for i, item := range slices.Backward(e.emailWorkMemo) {
		if item.revision != state.Revision() || len(e.emailWorkMemo) >= emailWorkMemoEntries {
			if err := e.dropEmailWorkRelations(item); err != nil {
				return nil, err
			}
			e.emailWorkMemo = append(e.emailWorkMemo[:i], e.emailWorkMemo[i+1:]...)
		}
	}
	population, err := e.readEmailWorkPopulation(ctx, sources)
	if err != nil {
		return nil, err
	}
	population.key = key
	population.revision = state.Revision()
	population.recoverySnapshot = state.RecoverySnapshot
	population.builtCost = population.cost
	if err := e.materializeEmailWorkPopulation(ctx, population); err != nil {
		return nil, err
	}
	// Native relations survive large Go populations; aggregate retained Go facts
	// remain bounded independently of the existing native memory/spill policy.
	if population.cost > emailWorkMemoBudget {
		population.releaseGoFacts()
	}
	e.emailWorkMemo = append([]*emailWorkPopulation{population}, e.emailWorkMemo...)
	var retained int64
	for _, item := range e.emailWorkMemo {
		if retained+item.cost > emailWorkMemoBudget {
			item.releaseGoFacts()
		}
		retained += item.cost
	}

	return population, nil
}

func emailWorkSourcePredicate(alias string, sources []int64) (string, []any) {
	if sources == nil {
		return "TRUE", nil
	}
	if len(sources) == 0 {
		return "FALSE", nil
	}
	args := make([]any, len(sources))
	for i, id := range sources {
		args[i] = id
	}
	return alias + "source_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(sources)), ",") + ")", args
}

func (e *DuckDBEngine) readEmailWorkPopulation(ctx context.Context, sources []int64) (*emailWorkPopulation, error) {
	population := &emailWorkPopulation{activities: map[int64]store.EmailWorkActivity{}, seeds: map[string]store.EmailWorkSeed{}, componentForMessage: map[int64]int{}}
	predicate, args := emailWorkSourcePredicate("", sources)
	owners := map[int64][]string{}
	err := scanEmailWorkRows(ctx, e.db, fmt.Sprintf("SELECT source_id,address FROM read_parquet('%s') WHERE %s", e.parquetPath(DatasetEmailWorkIdentities), predicate), args, func(rows *sql.Rows) error {
		var sourceID int64
		var address string
		if err := rows.Scan(&sourceID, &address); err != nil {
			return err
		}
		owners[sourceID] = append(owners[sourceID], address)
		return nil
	})
	if err != nil {
		return nil, err
	}
	predicate, args = emailWorkSourcePredicate("d.", sources)
	providerComplete := map[int64]bool{}
	err = scanEmailWorkRows(ctx, e.db, fmt.Sprintf(`SELECT d.message_id,d.source_id,d.provider_thread_id,d.native_sent,d.native_draft,d.provider_complete,m.sent_at,f.facts
 FROM read_parquet('%s') d JOIN messages m ON m.id=d.message_id
 LEFT JOIN read_parquet('%s') f ON f.message_id=d.message_id WHERE %s ORDER BY d.message_id`, e.parquetPath(DatasetEmailWorkDelivery), e.parquetPath(DatasetEmailWorkFacts), predicate), args, func(rows *sql.Rows) error {
		var activity store.EmailWorkActivity
		var thread, facts sql.NullString
		var occurred sql.NullTime
		var complete bool
		if err := rows.Scan(&activity.MessageID, &activity.SourceID, &thread, &activity.PositiveSent, &activity.Draft, &complete, &occurred, &facts); err != nil {
			return err
		}
		activity.ProviderThreadID = thread.String
		activity.OccurredAt = occurred.Time
		if !facts.Valid {
			activity.Linkage = mime.EmailLinkage{Version: mime.EmailLinkageVersion, Outcome: "pending_extraction"}
		} else if err := json.Unmarshal([]byte(facts.String), &activity.Linkage); err != nil {
			activity.Linkage = mime.EmailLinkage{Version: mime.EmailLinkageVersion, Outcome: "invalid_facts"}
		}
		population.activities[activity.MessageID] = activity
		providerComplete[activity.MessageID] = complete
		// Charge retained evidence strings and a conservative per-message cost.
		population.cost += int64(len(facts.String) + len(thread.String) + 512)
		return nil
	})
	if err != nil {
		return nil, err
	}
	envelopes := map[int64][]store.EmailWorkEnvelope{}
	envelopeColumn := "NULL::VARCHAR"
	if e.hasCol("message_recipients", "envelope_address") {
		envelopeColumn = "mr.envelope_address"
	}
	err = scanEmailWorkRows(ctx, e.db, fmt.Sprintf(`SELECT mr.message_id,mr.recipient_type,%s FROM message_recipients mr
 JOIN read_parquet('%s') d ON d.message_id=mr.message_id WHERE %s`, envelopeColumn, e.parquetPath(DatasetEmailWorkDelivery), predicate), args, func(rows *sql.Rows) error {
		var id int64
		var kind string
		var address sql.NullString
		if err := rows.Scan(&id, &kind, &address); err != nil {
			return err
		}
		envelopes[id] = append(envelopes[id], store.EmailWorkEnvelope{Kind: kind, Address: address.String})
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = scanEmailWorkRows(ctx, e.db, fmt.Sprintf("SELECT ordinal,uid,anchor_message_id,live_message_id FROM read_parquet('%s')", e.parquetPath(DatasetEmailWorkSeeds)), nil, func(rows *sql.Rows) error {
		var seed store.EmailWorkSeed
		if err := rows.Scan(&seed.Ordinal, &seed.UID, &seed.AnchorMessageID, &seed.LiveMessageID); err != nil {
			return err
		}
		population.seeds[seed.UID] = seed
		population.cost += int64(len(seed.UID) + 128)
		return nil
	})
	if err != nil {
		return nil, err
	}
	localLinks, localHandled, err := e.readEmailWorkLocalActions(ctx, population, sources)
	if err != nil {
		return nil, err
	}
	members := make([]store.EmailWorkMember, 0, len(population.activities))
	attributor := store.NewEmailWorkAttributor(owners)
	for id, activity := range population.activities {
		activity = attributor.Attribute(activity, envelopes[id], providerComplete[id])
		activity.LocalHandled = localHandled[id]
		population.activities[id] = activity
		members = append(members, activity.EmailWorkMember)
	}
	population.components, err = store.PartitionEmailWorkMembersWithLocalLinks(members, localLinks)
	if err != nil {
		return nil, err
	}
	for i, component := range population.components {
		activities := make([]store.EmailWorkActivity, 0, len(component))
		for _, id := range component {
			population.componentForMessage[id] = i
			activities = append(activities, population.activities[id])
		}
		population.handling = append(population.handling, store.EvaluateEmailWorkHandling(activities))
	}
	population.canonicalSeeds = make([]store.EmailWorkSeed, len(population.components))
	for _, seed := range population.seeds {
		if !seed.LiveMessageID.Valid {
			continue
		}
		component, found := population.componentForMessage[seed.LiveMessageID.Int64]
		if !found {
			continue
		}
		incumbent := population.canonicalSeeds[component]
		if incumbent.Ordinal == 0 || seed.Ordinal < incumbent.Ordinal {
			population.canonicalSeeds[component] = seed
		}
	}
	return population, nil
}

func scanEmailWorkRows(ctx context.Context, db *sql.DB, statement string, args []any, visit func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := visit(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// resolveAlias follows only the alias's own live anchor inside this exact
// scoped publication; it cannot relocate an excluded or deleted anchor.
func (p *emailWorkPopulation) resolveAlias(uid string) (int, error) {
	seed, found := p.seeds[uid]
	if !found {
		return 0, store.ErrEmailWorkAliasMissing
	}
	if !seed.LiveMessageID.Valid {
		return 0, store.ErrEmailWorkAliasGone
	}
	component, found := p.componentForMessage[seed.LiveMessageID.Int64]
	if !found {
		return 0, store.ErrEmailWorkAliasOutOfScope
	}
	return component, nil
}
