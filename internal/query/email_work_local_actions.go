package query

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"go.kenn.io/msgvault/internal/store"
)

// Only actions whose every immutable target is present in this permitted
// population can affect or appear in its projection. A hidden source cannot
// supply grouping or local handled evidence through a partially visible action.
func (e *DuckDBEngine) readEmailWorkLocalActions(ctx context.Context, population *emailWorkPopulation, sources []int64) ([][]int64, map[int64]bool, error) {
	predicate, args := emailWorkSourcePredicate("d.", sources)
	statement := fmt.Sprintf(`SELECT a.action_uid,a.kind,a.revision,a.active,a.actor_kind,a.actor_user_id,a.created_at,a.target_uid,a.target_count
 FROM read_parquet('%s') a JOIN read_parquet('%s') seed ON seed.uid=a.target_uid
 JOIN read_parquet('%s') d ON d.message_id=seed.live_message_id
 WHERE %s ORDER BY a.action_uid,a.target_uid`, e.parquetPath(DatasetEmailWorkActions), e.parquetPath(DatasetEmailWorkSeeds), e.parquetPath(DatasetEmailWorkDelivery), predicate)
	actions := map[string]*store.EmailWorkAction{}
	counts := map[string]int{}
	err := scanEmailWorkRows(ctx, e.db, statement, args, func(rows *sql.Rows) error {
		action := &store.EmailWorkAction{}
		var target string
		var count int
		if err := rows.Scan(&action.UID, &action.Kind, &action.Revision, &action.Active, &action.ActorKind, &action.ActorUserID, &action.CreatedAt, &target, &count); err != nil {
			return err
		}
		if _, present := population.activities[population.seeds[target].LiveMessageID.Int64]; !present {
			return nil
		}
		incumbent := actions[action.UID]
		if incumbent == nil {
			incumbent = action
			incumbent.Targets = []string{}
			actions[action.UID] = incumbent
			counts[action.UID] = count
		}
		incumbent.Targets = append(incumbent.Targets, target)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	var uids []string
	for uid := range actions {
		uids = append(uids, uid)
	}
	slices.Sort(uids)
	links := [][]int64{}
	handled := map[int64]bool{}
	population.localActions = []store.EmailWorkAction{}
	for _, uid := range uids {
		action := actions[uid]
		if counts[uid] < 1 || len(action.Targets) != counts[uid] {
			continue
		}
		if action.Kind != "link" && action.Kind != "handled" {
			continue
		}
		if action.Kind == "link" && len(action.Targets) < 2 {
			continue
		}
		population.localActions = append(population.localActions, *action)
		population.cost += int64(len(action.UID) + len(action.Targets)*64 + 256)
		if !action.Active {
			continue
		}
		ids := make([]int64, len(action.Targets))
		for i, target := range action.Targets {
			ids[i] = population.seeds[target].LiveMessageID.Int64
		}
		if action.Kind == "link" {
			links = append(links, ids)
		} else {
			for _, id := range ids {
				handled[id] = true
			}
		}
	}
	return links, handled, nil
}
