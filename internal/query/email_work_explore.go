package query

import (
	"context"
	"fmt"
	"go.kenn.io/msgvault/internal/identityindex"
	"slices"
	"strings"
)

func (e *DuckDBEngine) exploreLogicalSQLLocked(ctx context.Context, request ExploreRequest, conditions, rank string, withLists bool) (string, *emailWorkPopulation, func(), error) {
	if len(request.Context.HandlingStates) > 0 && request.Context.RowUnit != RowUnitEmailWork {
		return "", nil, nil, fmt.Errorf("%w: handling filters require related email rows", ErrInvalidExploreRequest)
	}
	for _, state := range request.Context.HandlingStates {
		if !slices.Contains([]string{"handled", "actionable", "unknown"}, state) {
			return "", nil, nil, fmt.Errorf("%w: unsupported handling state", ErrInvalidExploreRequest)
		}
	}
	if request.Context.RowUnit == RowUnitDefault {
		if withLists {
			return buildExploreLogicalSQLWithCandidateRank(conditions, rank), nil, func() {}, nil
		}
		if rank == "NULL::BIGINT" {
			return buildExploreLogicalSQLNoLists(conditions), nil, func() {}, nil
		}
		return buildExploreFilteredClassifiedCTE(conditions, rank) + exploreLogicalEntriesCTE(false), nil, func() {}, nil
	}
	if request.Context.RowUnit != RowUnitEmailWork {
		return "", nil, nil, fmt.Errorf("%w: unsupported row unit", ErrInvalidExploreRequest)
	}
	sourceIDs := emailWorkEffectiveSources(request.Context.SourceIDs)
	population, err := e.emailWorkPopulationLocked(ctx, EmailWorkScope{SourceIDs: sourceIDs, AuthorityKey: request.Context.WorkAuthorityKey})
	if err != nil {
		return "", nil, nil, err
	}
	table := fmt.Sprintf("_email_work_members_%d", e.tempTableSeq.Add(1))
	if _, err := e.db.ExecContext(ctx, "CREATE TEMP TABLE "+table+" (message_id BIGINT,work_key VARCHAR,handling_state VARCHAR)"); err != nil {
		return "", nil, nil, err
	}
	cleanup := func() { _, _ = e.db.Exec("DROP TABLE IF EXISTS " + table) }
	var values []string
	var args []any
	flush := func() error {
		if len(values) == 0 {
			return nil
		}
		_, err := e.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES "+strings.Join(values, ","), args...)
		values = nil
		args = nil
		return err
	}
	for i, members := range population.components {
		seed := population.canonicalSeeds[i]
		if seed.UID == "" {
			continue
		} // Unprocessed legacy mail stays readable as ordinary rows.
		for _, id := range members {
			values = append(values, "(?,?,?)")
			args = append(args, id, "email-work:"+seed.UID, population.handling[i].State)
			if len(values) == 1000 {
				if err := flush(); err != nil {
					cleanup()
					return "", nil, nil, err
				}
			}
		}
	}
	if err := flush(); err != nil {
		cleanup()
		return "", nil, nil, err
	}
	// Conditions and ranking are evaluated once on witnesses. Complete permitted
	// work families, including completed outbound copies outside Inbox, are then
	// projected from the same scoped map. No account outside it enters the row.
	stateCondition := ""
	legacyCondition := ""
	if len(request.Context.HandlingStates) > 0 {
		quoted := make([]string, len(request.Context.HandlingStates))
		for i, state := range request.Context.HandlingStates {
			quoted[i] = "'" + state + "'"
		}
		stateCondition = " WHERE w.handling_state IN (" + strings.Join(quoted, ",") + ")"
		if slices.Contains(request.Context.HandlingStates, "unknown") {
			legacyCondition = " AND COALESCE(NULLIF(message_type,''),'email')='email'"
		} else {
			legacyCondition = " AND FALSE"
		}
	}
	sql := `WITH witnesses AS (
 SELECT *, ` + rank + ` AS candidate_rank FROM analytical_entries WHERE ` + conditions + `
), matched_work AS (
 SELECT w.work_key,a.message_id,a.candidate_rank FROM witnesses a JOIN ` + table + ` w ON w.message_id=a.message_id` + stateCondition + `
), classified AS (
 SELECT a.*, ` + sqlIsChatPredicate("message_type", "conversation_type") + ` AS is_chat,
 ` + identityindex.EntryKindSQL("message_type") + ` AS entry_kind,
 NULL::VARCHAR AS work_key,true AS is_match_witness FROM witnesses a
 WHERE NOT EXISTS (SELECT 1 FROM ` + table + ` w WHERE w.message_id=a.message_id)` + legacyCondition + `
 UNION ALL
 SELECT a.*, mw.candidate_rank,false AS is_chat,'email_work_item' AS entry_kind,w.work_key,mw.message_id IS NOT NULL AS is_match_witness
 FROM analytical_entries a JOIN ` + table + ` w ON w.message_id=a.message_id
 LEFT JOIN matched_work mw ON mw.message_id=a.message_id
 WHERE w.work_key IN (SELECT work_key FROM matched_work)
)` + exploreLogicalEntriesCTE(withLists)
	return sql, population, cleanup, nil
}

// Native scoped engines use an impossible source to represent empty access.
// Preserve that deny-all state; Context's ordinary empty filter means all.
func emailWorkEffectiveSources(sources []int64) []int64 {
	if len(sources) == 1 && sources[0] == noVisibleSource {
		return []int64{}
	}
	if len(sources) == 0 {
		return nil
	}
	return sources
}

func exploreEmailWorkEntriesCTE(withLists bool) string {
	lists := ""
	if withLists {
		lists = `
 list_sort(list_distinct(flatten(list(participant_ids)))) AS participant_ids,
 list_sort(list_distinct(flatten(list(participant_labels)))) AS participant_labels,
 list_sort(list_distinct(flatten(list(participant_domains)))) AS participant_domains,`
	}
	return `
 UNION ALL
 SELECT work_key AS entry_key,'email_work_item' AS entry_kind,
 arg_max(message_id,struct_pack(occurred_at:=occurred_at,message_id:=message_id)) FILTER(WHERE is_match_witness) AS anchor_message_id,
 NULL::BIGINT AS conversation_id,MAX(occurred_at) FILTER(WHERE is_match_witness) AS occurred_at,
 arg_max(source_id,struct_pack(occurred_at:=occurred_at,message_id:=message_id)) FILTER(WHERE is_match_witness) AS source_id,
 arg_max(source_type,struct_pack(occurred_at:=occurred_at,message_id:=message_id)) FILTER(WHERE is_match_witness) AS source_type,
 arg_max(source_identifier,struct_pack(occurred_at:=occurred_at,message_id:=message_id)) FILTER(WHERE is_match_witness) AS source_identifier,
 CASE WHEN BOOL_OR(inbox_state='inbox') THEN 'inbox' WHEN BOOL_OR(inbox_state='unknown') THEN 'unknown' ELSE 'outside' END AS inbox_state,
 CASE WHEN BOOL_OR(read_state='unread') THEN 'unread' WHEN BOOL_OR(read_state='unknown') THEN 'unknown' ELSE 'read' END AS read_state,
 'email' AS message_type,arg_max(list_id,occurred_at) AS list_id,'email_thread' AS conversation_type,
 arg_max(COALESCE(NULLIF(subject,''),snippet,''),struct_pack(occurred_at:=occurred_at,message_id:=message_id)) FILTER(WHERE is_match_witness) AS title,
 arg_max(snippet,struct_pack(occurred_at:=occurred_at,message_id:=message_id)) FILTER(WHERE is_match_witness) AS preview,` + lists + `
 arg_min(message_id,struct_pack(candidate_rank:=candidate_rank,message_id:=message_id)) FILTER(WHERE candidate_rank IS NOT NULL) AS strongest_matched_message_id,
 COUNT(*)::BIGINT AS message_count,COUNT(*) FILTER(WHERE is_match_witness)::BIGINT AS matched_message_count,
 list_sort(list_distinct(list(source_id))) AS member_source_ids,SUM(size_estimate+attachment_size)::BIGINT AS estimated_bytes,
 false AS deletable,BOOL_OR(has_attachments) AS has_attachments,
 arg_max(is_from_me,struct_pack(occurred_at:=occurred_at,message_id:=message_id)) AS is_from_me,
 SUM(attachment_count)::BIGINT AS attachment_count,SUM(attachment_size)::BIGINT AS attachment_size,
 BOOL_AND(deleted_from_source) AS deleted_from_source
 FROM classified WHERE work_key IS NOT NULL GROUP BY work_key`
}

func emailWorkSummary(population *emailWorkPopulation, key string) *EmailWorkSummary {
	if population == nil || !strings.HasPrefix(key, "email-work:") {
		return nil
	}
	uid := strings.TrimPrefix(key, "email-work:")
	component, err := population.resolveAlias(uid)
	if err != nil {
		return nil
	}
	handling := population.handling[component]
	sources := make([]int64, 0)
	for _, id := range population.components[component] {
		sources = append(sources, population.activities[id].SourceID)
	}
	slices.Sort(sources)
	sources = slices.Compact(sources)
	return &EmailWorkSummary{UID: uid, State: handling.State, SourceIDs: sources, OutstandingCount: len(handling.OutstandingEventIDs), EvidenceCount: len(handling.OutboundEvidenceIDs), Reasons: slices.Clone(handling.Reasons)}
}

func validateEmailWorkSelection(population *emailWorkPopulation, keys []string) error {
	for _, key := range keys {
		if !strings.HasPrefix(key, "email-work:") {
			continue
		}
		uid := strings.TrimPrefix(key, "email-work:")
		component, err := population.resolveAlias(uid)
		if err != nil || population.canonicalSeeds[component].UID != uid {
			return ErrEmailWorkRevisionChanged
		}
	}
	return nil
}
