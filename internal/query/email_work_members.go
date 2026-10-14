package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"go.kenn.io/msgvault/internal/store"
	"slices"
	"strings"
)

var ErrEmailWorkRevisionChanged = errors.New("email work item changed; refresh it before continuing")

func (e *DuckDBEngine) EmailWorkMembers(ctx context.Context, request EmailWorkMembersRequest) (*EmailWorkMembersResponse, error) {
	if err := validateExploreAnalysisPage(request.Page, maxExploreLimit); err != nil {
		return nil, err
	}
	release, err := e.acquireQuerySlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	sources := emailWorkEffectiveSources(request.Explore.Context.SourceIDs)
	population, err := e.emailWorkPopulationLocked(ctx, EmailWorkScope{SourceIDs: sources, AuthorityKey: request.Explore.Context.WorkAuthorityKey})
	if err != nil {
		return nil, err
	}
	if request.ExpectedCacheRevision != "" && request.ExpectedCacheRevision != population.revision {
		return nil, ErrEmailWorkRevisionChanged
	}
	if request.Page.Offset > 0 && request.ExpectedCacheRevision == "" {
		return nil, fmt.Errorf("%w: paginated members require a cache revision", ErrInvalidExploreRequest)
	}
	alias, err := e.emailWorkAliasLocked(ctx, population, request.UID)
	if err != nil {
		return nil, err
	}
	if e.sqliteDB != nil {
		var live sql.NullInt64
		var source sql.NullInt64
		err := e.sqliteDB.QueryRowContext(ctx, `SELECT seed.live_message_id,m.source_id FROM email_work_seeds seed LEFT JOIN messages m ON m.id=seed.live_message_id WHERE seed.uid=?`, request.UID).Scan(&live, &source)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrEmailWorkAliasMissing
		}
		if err != nil {
			return nil, err
		}
		if !live.Valid {
			return nil, store.ErrEmailWorkAliasGone
		}
		if live.Int64 != alias.seed.LiveMessageID.Int64 || !source.Valid || source.Int64 != alias.sourceID {
			return nil, ErrEmailWorkRevisionChanged
		}
	}
	var canonical, summaryJSON, handlingJSON string
	var total int
	if err := e.db.QueryRowContext(ctx, "SELECT uid,member_count,summary,handling FROM "+population.relations.components+" WHERE component=?", alias.component).Scan(&canonical, &total, &summaryJSON, &handlingJSON); err != nil {
		return nil, err
	}
	var summary EmailWorkSummary
	if err := json.Unmarshal([]byte(summaryJSON), &summary); err != nil {
		return nil, err
	}
	var handling store.EmailWorkHandling
	if err := json.Unmarshal([]byte(handlingJSON), &handling); err != nil {
		return nil, err
	}
	limit := request.Page.Limit
	if limit == 0 {
		limit = defaultExploreLimit
	}
	page := []int64{}
	if err := scanEmailWorkRows(ctx, e.db, "SELECT message_id FROM "+population.relations.members+" WHERE component=? ORDER BY member_ordinal LIMIT ? OFFSET ?", []any{alias.component, limit, request.Page.Offset}, func(rows *sql.Rows) error {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		page = append(page, id)
		return nil
	}); err != nil {
		return nil, err
	}
	response := &EmailWorkMembersResponse{RecoverySnapshot: population.recoverySnapshot, LocalActions: []store.EmailWorkAction{}, UID: canonical, RequestedUID: request.UID, CacheRevision: population.revision, Members: []EmailWorkMemberFact{}, TotalCount: total, Summary: summary, OutstandingMessageIDs: slices.Clone(handling.OutstandingEventIDs), EvidenceMessageIDs: slices.Clone(handling.OutboundEvidenceIDs)}
	if err := scanEmailWorkRows(ctx, e.db, "SELECT payload FROM "+population.relations.actions+" WHERE component=? ORDER BY uid", []any{alias.component}, func(rows *sql.Rows) error {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		var action store.EmailWorkAction
		if err := json.Unmarshal([]byte(payload), &action); err != nil {
			return err
		}
		response.LocalActions = append(response.LocalActions, action)
		return nil
	}); err != nil {
		return nil, err
	}
	if len(page) == 0 {
		return response, nil
	}
	scanIDs := slices.Clone(page)
	if !slices.Contains(scanIDs, alias.seed.LiveMessageID.Int64) {
		scanIDs = append(scanIDs, alias.seed.LiveMessageID.Int64)
	}
	args := make([]any, len(scanIDs))
	for i, id := range scanIDs {
		args[i] = id
	}
	rows, err := e.db.QueryContext(ctx, `SELECT message_id,analytical_entries.source_id,source_type,source_identifier,source_message_id,conversation_id,occurred_at,
 COALESCE(NULLIF(subject,''),snippet,''),snippet,inbox_state,read_state,
 COALESCE((SELECT CAST(to_json(list_sort(list_distinct(list(l.name)))) AS VARCHAR) FROM message_labels ml JOIN labels l ON l.id=ml.label_id WHERE ml.message_id=analytical_entries.message_id),'[]'),w.uid,w.activity
 FROM analytical_entries JOIN `+population.relations.members+` w USING(message_id) WHERE message_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(scanIDs)), ",")+`) ORDER BY message_id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		member := EmailWorkMemberFact{LocalReadState: store.EmailWorkUnknown}
		var labelsJSON, activityJSON string
		if err := rows.Scan(&member.MessageID, &member.SourceID, &member.SourceType, &member.SourceIdentifier, &member.SourceMessageID, &member.ConversationID, &member.OccurredAt, &member.Title, &member.Preview, &member.InboxState, &member.ReadState, &labelsJSON, &member.MessageUID, &activityJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(labelsJSON), &member.Labels); err != nil {
			return nil, err
		}
		var activity store.EmailWorkActivity
		if err := json.Unmarshal([]byte(activityJSON), &activity); err != nil {
			return nil, err
		}
		member.RFCMessageID = activity.Linkage.MessageID
		member.ReplyIDs = slices.Clone(activity.Linkage.Replies)
		member.ReferenceIDs = slices.Clone(activity.Linkage.References)
		member.AttachedOriginalIDs = slices.Clone(activity.Linkage.AttachedOriginals)
		member.UnavailableReasons = slices.Clone(activity.UnavailableReasons)
		member.ProviderThreadID = activity.ProviderThreadID
		member.LinkageOutcome = activity.Linkage.Outcome
		member.NativeSent = activity.PositiveSent
		member.NativeDraft = activity.Draft
		member.Incoming = activity.Incoming
		member.AttributionComplete = activity.AttributionComplete
		member.ConfirmedSourceSender = activity.ConfirmedSourceSender
		member.Preview = FlattenSnippet(member.Preview)
		if member.MessageID == alias.seed.LiveMessageID.Int64 {
			response.RequestedAnchor = &member
		}
		if slices.Contains(page, member.MessageID) {
			response.Members = append(response.Members, member)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if e.sqliteDB != nil {
		localReadStates := map[int64]string{}
		verifiedMembers := slices.Clone(response.Members)
		if response.RequestedAnchor != nil && !slices.Contains(page, response.RequestedAnchor.MessageID) {
			verifiedMembers = append(verifiedMembers, *response.RequestedAnchor)
		}
		for _, member := range verifiedMembers {
			var sourceID int64
			var providerID string
			var uid sql.NullString
			var localRead sql.NullBool
			err := e.sqliteDB.QueryRowContext(ctx, `SELECT m.source_id,m.source_message_id,seed.uid,m.is_read FROM messages m LEFT JOIN email_work_seeds seed ON seed.live_message_id=m.id WHERE m.id=?`, member.MessageID).Scan(&sourceID, &providerID, &uid, &localRead)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrEmailWorkRevisionChanged
			}
			if err != nil {
				return nil, err
			}
			if sourceID != member.SourceID || providerID != member.SourceMessageID || (member.MessageUID != "" && uid.String != member.MessageUID) {
				return nil, ErrEmailWorkRevisionChanged
			}
			state := store.EmailWorkUnknown
			if localRead.Valid {
				if localRead.Bool {
					state = "read"
				} else {
					state = "unread"
				}
			}
			localReadStates[member.MessageID] = state
		}
		for i := range response.Members {
			response.Members[i].LocalReadState = localReadStates[response.Members[i].MessageID]
		}
		if response.RequestedAnchor != nil {
			response.RequestedAnchor.LocalReadState = localReadStates[response.RequestedAnchor.MessageID]
		}
	}
	return response, nil
}
