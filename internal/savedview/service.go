// Package savedview defines the Saved View service contract shared by MCP and
// the daemon client, plus the version-1 executable-state vocabulary that the
// Web UI, the daemon client, and the MCP input schema all agree on.
package savedview

import (
	"context"
	"encoding/json"
	"fmt"

	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

// Reader exposes durable Saved View definitions and executes them through the
// archive's canonical Explore machinery.
type Reader interface {
	ListSavedViews(ctx context.Context) ([]store.SavedView, error)
	GetSavedView(ctx context.Context, id int64) (*store.SavedView, error)
	RunSavedView(ctx context.Context, id int64, limit int, cursor string) (*RunPage, error)
}

// Writer mutates Saved Views while preserving the store/API optimistic
// revision contract.
type Writer interface {
	CreateSavedView(ctx context.Context, input store.SavedViewInput) (*store.SavedView, error)
	UpdateSavedView(ctx context.Context, id, expectedRevision int64, patch Patch) (*store.SavedView, error)
	DeleteSavedView(ctx context.Context, id, expectedRevision int64) error
}

// Service is the full Saved View surface an embedder wires into MCP.
type Service interface {
	Reader
	Writer
}

// Patch contains only fields supplied by the caller. An empty Description
// string clears the existing description, matching the HTTP API.
type Patch struct {
	Name           *string
	Description    *string
	CanonicalState *store.SavedViewStateEnvelope
	SchemaVersion  *int
}

// ResultKind names which typed array a RunPage carries.
type ResultKind string

const (
	ResultEntries ResultKind = "entries"
	ResultGroups  ResultKind = "groups"
	ResultFiles   ResultKind = "files"
)

// RunPage is one typed page from the Explore surface selected by a Saved
// View's grouping and presentation definition.
type RunPage struct {
	View                   store.SavedView         `json:"view"`
	ResultKind             ResultKind              `json:"result_kind"`
	Rows                   []query.EntryRow        `json:"rows,omitempty"`
	Groups                 []query.ExploreGroupRow `json:"groups,omitempty"`
	Files                  []query.ExploreFileFact `json:"files,omitempty"`
	TotalCount             *int64                  `json:"total_count,omitempty"`
	Returned               int                     `json:"returned"`
	HasMore                bool                    `json:"has_more"`
	NextCursor             string                  `json:"next_cursor,omitempty"`
	CacheRevision          string                  `json:"cache_revision"`
	SearchProvenance       query.SearchProvenance  `json:"search_provenance"`
	CandidateSnapshotID    string                  `json:"candidate_snapshot_id,omitempty"`
	CandidatePoolSaturated bool                    `json:"candidate_pool_saturated,omitempty"`
	SearchDeletionScope    string                  `json:"search_deletion_scope,omitempty"`
}

// Version-1 executable vocabulary. The Web UI's Saved Views workspace applies
// the same rules before opening a view, so a definition the Web UI can open is
// one the daemon client can execute, and vice versa.
var (
	FilterFields    = store.SavedViewFilterFields
	FilterAliases   = store.SavedViewFilterAliases
	FilterOperators = store.SavedViewFilterOperators
	SearchModes     = store.SavedViewSearchModes
	Presentations   = store.SavedViewPresentations
	SortFields      = store.SavedViewSortFields
	SortDirections  = store.SavedViewSortDirections
	Columns         = store.SavedViewColumns
)

// DefaultSearchMode applies when a definition carries a query without a mode.
const DefaultSearchMode = "full_text"

// FilterDimension maps a v1 filter field, including legacy aliases, onto the
// Explore dimension it executes against.
func FilterDimension(field string) string {
	if dimension, ok := FilterAliases[field]; ok {
		return dimension
	}
	return field
}

// ExecutableState decodes a Saved View's canonical state and checks it against
// the v1 executable contract. Errors wrap store.ErrSavedViewUnsupportedSchemaVersion
// or store.ErrSavedViewInvalidState so callers can map them uniformly.
func ExecutableState(view store.SavedView) (store.SavedViewStateEnvelope, error) {
	if view.SchemaVersion != store.CurrentSavedViewSchemaVersion {
		return store.SavedViewStateEnvelope{}, fmt.Errorf(
			"%w: Saved View %d uses schema version %d",
			store.ErrSavedViewUnsupportedSchemaVersion, view.ID, view.SchemaVersion,
		)
	}
	var state store.SavedViewStateEnvelope
	if err := json.Unmarshal(view.CanonicalState, &state); err != nil {
		return store.SavedViewStateEnvelope{}, fmt.Errorf(
			"%w: decode Saved View %d: %w", store.ErrSavedViewInvalidState, view.ID, err,
		)
	}
	if err := validateExecutable(state); err != nil {
		return store.SavedViewStateEnvelope{}, fmt.Errorf(
			"%w: Saved View %d: %w", store.ErrSavedViewInvalidState, view.ID, err,
		)
	}
	return state, nil
}

func validateExecutable(state store.SavedViewStateEnvelope) error {
	return store.ValidateSavedViewState(state)
}

// EmailWorkReader keeps a work-item read on the exact Saved View and archive
// revisions that produced its row, rather than silently broadening account scope.
type EmailWorkReader interface {
	GetEmailWorkItem(ctx context.Context, viewID, viewRevision int64, uid, cacheRevision string, limit, offset int) (*query.EmailWorkMembersResponse, error)
}

// EmailWorkRecoveryPlan fixes local intent and optimistic revisions. It grants
// no authority: the daemon rechecks the current caller and every message copy.
type EmailWorkRecoveryPlan struct {
	UID               string                        `json:"uid"`
	Kind              string                        `json:"kind"`
	Targets           []store.EmailWorkActionTarget `json:"targets"`
	Snapshot          string                        `json:"snapshot"`
	CacheRevision     string                        `json:"cache_revision"`
	SavedViewID       int64                         `json:"saved_view_id"`
	SavedViewRevision int64                         `json:"saved_view_revision"`
	Messages          []query.EmailWorkMemberFact   `json:"messages,omitempty"`
}

type EmailWorkRecoveryResult struct {
	Action     *store.EmailWorkAction `json:"action"`
	CacheState string                 `json:"cache_state"`
}

// EmailWorkRecovery preserves the native Member curation surface through the
// revision-pinned Saved View workflow; it never modifies an email provider.
type EmailWorkRecovery interface {
	PreviewEmailWorkAction(ctx context.Context, viewID, viewRevision int64, kind string, uids []string, cacheRevision string) (*EmailWorkRecoveryPlan, error)
	ApplyEmailWorkAction(ctx context.Context, plan EmailWorkRecoveryPlan) (*EmailWorkRecoveryResult, error)
	UndoEmailWorkAction(ctx context.Context, viewID, viewRevision int64, uid, snapshot, cacheRevision string, actionRevision int64) (*EmailWorkRecoveryResult, error)
}
