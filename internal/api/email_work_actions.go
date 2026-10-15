package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

// EmailWorkActionStore is the existing native Member curation capability.
// It records local assertions and never calls an email provider.
type EmailWorkActionStore interface {
	EmailWorkRecoverySnapshot(ctx context.Context) (string, error)
	EmailWorkAction(ctx context.Context, uid string) (*store.EmailWorkAction, error)
	CreateEmailWorkAction(ctx context.Context, input store.EmailWorkActionInput) (*store.EmailWorkAction, error)
	UndoEmailWorkAction(ctx context.Context, uid, snapshot string, revision int64, view ...int64) (*store.EmailWorkAction, error)
}

type EmailWorkActionRequest struct {
	UID               string                        `json:"uid,omitempty" maxLength:"32"`
	Kind              string                        `json:"kind" enum:"link,handled"`
	Targets           []store.EmailWorkActionTarget `json:"targets" minItems:"1" maxItems:"100"`
	Predicate         ExploreHTTPRequest            `json:"predicate"`
	CacheRevision     string                        `json:"cache_revision" minLength:"1"`
	Snapshot          string                        `json:"snapshot,omitempty" maxLength:"64"`
	SavedViewID       int64                         `json:"saved_view_id,omitempty" minimum:"0"`
	SavedViewRevision int64                         `json:"saved_view_revision,omitempty" minimum:"0"`
}

type EmailWorkActionPreview struct {
	EmailWorkActionRequest

	Messages []query.EmailWorkMemberFact `json:"messages"`
}

type EmailWorkActionResponse struct {
	Action     *store.EmailWorkAction `json:"action"`
	CacheState string                 `json:"cache_state" enum:"ready,stale"`
}

type EmailWorkUndoRequest struct {
	UID               string             `json:"uid" minLength:"32" maxLength:"32"`
	ExpectedRevision  int64              `json:"expected_revision" minimum:"1"`
	Snapshot          string             `json:"snapshot" minLength:"64" maxLength:"64"`
	Predicate         ExploreHTTPRequest `json:"predicate"`
	CacheRevision     string             `json:"cache_revision" minLength:"1"`
	SavedViewID       int64              `json:"saved_view_id,omitempty" minimum:"0"`
	SavedViewRevision int64              `json:"saved_view_revision,omitempty" minimum:"0"`
}

func (s *Server) registerEmailWorkActionRoutes(api huma.API) {
	registerExploreRoute[EmailWorkActionRequest, EmailWorkActionPreview](api, "previewEmailWorkAction", "/explore/email-work/preview", "Review a fixed-target local link or handled confirmation", s.handleEmailWorkActionPreview)
	registerAPIV1RawHumaJSONRouteWithRequest[EmailWorkActionRequest, EmailWorkActionResponse](api, "applyEmailWorkAction", http.MethodPost, "/explore/email-work/actions", "Save an undoable local email work assertion", s.handleEmailWorkActionApply)
	registerAPIV1RawHumaJSONRouteWithRequest[EmailWorkUndoRequest, EmailWorkActionResponse](api, "undoEmailWorkAction", http.MethodPost, "/explore/email-work/undo", "Undo one reviewed local email work assertion", s.handleEmailWorkActionUndo)
}

func (s *Server) emailWorkActionWriter(w http.ResponseWriter) EmailWorkActionStore {
	writer, ok := s.store.(EmailWorkActionStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "work_recovery_unavailable", "Local recovery is unavailable on this archive")
		return nil
	}
	return writer
}

func (s *Server) writeEmailWorkActionError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrSavedViewRevisionConflict):
		writeError(w, http.StatusConflict, "saved_view_revision_conflict", "The Saved View changed. Read its current definition and review the fixed messages again.")
	case errors.Is(err, store.ErrEmailWorkActionChanged):
		writeError(w, http.StatusConflict, "work_recovery_changed", "The archive or local recovery changed. Refresh and review the fixed messages again.")
	case errors.Is(err, store.ErrEmailWorkActionInvalid):
		writeError(w, http.StatusBadRequest, "invalid_work_recovery", "Choose valid, distinct email copies and a local link or handled confirmation.")
	case errors.Is(err, store.ErrEmailWorkAliasGone), errors.Is(err, store.ErrEmailWorkAliasMissing), errors.Is(err, store.ErrEmailWorkAliasOutOfScope):
		writeError(w, http.StatusNotFound, "work_item_unavailable", "A selected email copy is unavailable in the permitted accounts.")
	case errors.Is(err, query.ErrEmailWorkRevisionChanged), errors.Is(err, query.ErrCacheUnavailable):
		s.writeExploreError(r.Context(), w, err)
	default:
		if s.writeIfContextError(w, err) {
			return
		}
		s.logger.Error("local email work recovery failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "work_recovery_unavailable", "Local recovery could not be completed. Your archived copies were preserved.")
	}
}

func (s *Server) reviewEmailWorkTargets(ctx context.Context, request EmailWorkActionRequest, expectedCache string) ([]query.EmailWorkMemberFact, string, error) {
	if (request.Kind != "link" && request.Kind != "handled") || len(request.Targets) < 1 || len(request.Targets) > 100 || (request.Kind == "link" && len(request.Targets) < 2) {
		return nil, "", store.ErrEmailWorkActionInvalid
	}
	if request.SavedViewID != 0 || request.SavedViewRevision != 0 {
		if s.savedViewStore == nil {
			return nil, "", store.ErrSavedViewRevisionConflict
		}
		view, err := s.savedViewStore.GetSavedView(ctx, request.SavedViewID)
		if err != nil || view.Revision != request.SavedViewRevision {
			return nil, "", store.ErrSavedViewRevisionConflict
		}
	}
	prepared, err := s.prepareResolvedExplorePredicate(ctx, request.Predicate)
	if err != nil {
		return nil, "", store.ErrEmailWorkActionInvalid
	}
	prepared.query.Context.RowUnit = query.RowUnitEmailWork
	prepared.query.Context.WorkAuthorityKey = emailWorkAuthorityKey(ctx)
	reader, ok := s.queryEngineForContext(ctx).(query.EmailWorkExplorer)
	if !ok {
		return nil, "", query.ErrCacheUnavailable
	}
	messages := make([]query.EmailWorkMemberFact, 0, len(request.Targets))
	seen := map[string]bool{}
	var snapshot string
	for _, target := range request.Targets {
		if len(target.UID) != 32 || target.SourceID < 1 || seen[target.UID] {
			return nil, "", store.ErrEmailWorkActionInvalid
		}
		seen[target.UID] = true
		result, err := reader.EmailWorkMembers(ctx, query.EmailWorkMembersRequest{Explore: prepared.query, UID: target.UID, ExpectedCacheRevision: expectedCache, Page: query.PageSpec{Limit: 1}})
		if err != nil {
			return nil, "", err
		}
		anchor := result.RequestedAnchor
		if anchor == nil || anchor.MessageUID != target.UID || anchor.SourceID != target.SourceID {
			return nil, "", store.ErrEmailWorkActionChanged
		}
		if result.RecoverySnapshot == "" || (snapshot != "" && snapshot != result.RecoverySnapshot) {
			return nil, "", store.ErrEmailWorkActionChanged
		}
		snapshot = result.RecoverySnapshot
		messages = append(messages, *anchor)
	}
	return messages, snapshot, nil
}

func (s *Server) handleEmailWorkActionPreview(w http.ResponseWriter, r *http.Request) {
	var request EmailWorkActionRequest
	if !decodeExploreJSON(w, r, &request) {
		return
	}
	writer := s.emailWorkActionWriter(w)
	if writer == nil {
		return
	}
	if request.CacheRevision == "" {
		s.writeEmailWorkActionError(w, r, store.ErrEmailWorkActionInvalid)
		return
	}
	messages, publishedSnapshot, err := s.reviewEmailWorkTargets(r.Context(), request, request.CacheRevision)
	if err != nil {
		s.writeEmailWorkActionError(w, r, err)
		return
	}
	nativeSnapshot, err := writer.EmailWorkRecoverySnapshot(r.Context())
	if err != nil {
		s.writeEmailWorkActionError(w, r, err)
		return
	}
	if nativeSnapshot != publishedSnapshot {
		s.writeEmailWorkActionError(w, r, store.ErrEmailWorkActionChanged)
		return
	}
	uid := make([]byte, 16)
	if _, err = rand.Read(uid); err != nil {
		s.writeEmailWorkActionError(w, r, err)
		return
	}
	request.UID = hex.EncodeToString(uid)
	request.Snapshot = nativeSnapshot
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, EmailWorkActionPreview{EmailWorkActionRequest: request, Messages: messages})
}

func (s *Server) handleEmailWorkActionApply(w http.ResponseWriter, r *http.Request) {
	var request EmailWorkActionRequest
	if !decodeExploreJSON(w, r, &request) {
		return
	}
	writer := s.emailWorkActionWriter(w)
	if writer == nil {
		return
	}
	principal := principalFromContext(r.Context())
	existing, lookupErr := writer.EmailWorkAction(r.Context(), request.UID)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		s.writeEmailWorkActionError(w, r, lookupErr)
		return
	}
	expectedCache := request.CacheRevision
	if existing != nil {
		expectedCache = ""
	} // An identical retry changes no selection.
	_, publishedSnapshot, err := s.reviewEmailWorkTargets(r.Context(), request, expectedCache)
	if err != nil {
		s.writeEmailWorkActionError(w, r, err)
		return
	}
	if existing == nil && (request.CacheRevision == "" || request.Snapshot == "" || publishedSnapshot != request.Snapshot) {
		s.writeEmailWorkActionError(w, r, store.ErrEmailWorkActionChanged)
		return
	}
	action, err := writer.CreateEmailWorkAction(r.Context(), store.EmailWorkActionInput{UID: request.UID, Kind: request.Kind, Targets: request.Targets, Snapshot: request.Snapshot, ActorKind: string(principal.Kind), ActorUserID: principal.UserID, ExpectedViewID: request.SavedViewID, ExpectedViewRevision: request.SavedViewRevision})
	if err != nil {
		s.writeEmailWorkActionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, EmailWorkActionResponse{Action: action, CacheState: s.refreshIdentityCacheState(r.Context())})
}

func (s *Server) handleEmailWorkActionUndo(w http.ResponseWriter, r *http.Request) {
	var request EmailWorkUndoRequest
	if !decodeExploreJSON(w, r, &request) {
		return
	}
	writer := s.emailWorkActionWriter(w)
	if writer == nil {
		return
	}
	action, err := writer.EmailWorkAction(r.Context(), request.UID)
	if errors.Is(err, sql.ErrNoRows) {
		s.writeEmailWorkActionError(w, r, store.ErrEmailWorkActionChanged)
		return
	}
	if err != nil {
		s.writeEmailWorkActionError(w, r, err)
		return
	}
	// Revalidate every fixed target under current caller access. Undo never
	// replaces a changing family selection with whatever its alias now resolves to.
	targets := make([]store.EmailWorkActionTarget, 0, len(action.Targets))
	prepared, err := s.prepareResolvedExplorePredicate(r.Context(), request.Predicate)
	if err != nil {
		s.writeEmailWorkActionError(w, r, store.ErrEmailWorkActionInvalid)
		return
	}
	prepared.query.Context.RowUnit = query.RowUnitEmailWork
	prepared.query.Context.WorkAuthorityKey = emailWorkAuthorityKey(r.Context())
	reader, ok := s.queryEngineForContext(r.Context()).(query.EmailWorkExplorer)
	if !ok {
		s.writeEmailWorkActionError(w, r, query.ErrCacheUnavailable)
		return
	}
	expectedCache := request.CacheRevision
	if !action.Active {
		expectedCache = ""
	}
	for _, uid := range action.Targets {
		member, err := reader.EmailWorkMembers(r.Context(), query.EmailWorkMembersRequest{Explore: prepared.query, UID: uid, ExpectedCacheRevision: expectedCache, Page: query.PageSpec{Limit: 1}})
		if err != nil {
			s.writeEmailWorkActionError(w, r, err)
			return
		}
		if member.RequestedAnchor == nil {
			s.writeEmailWorkActionError(w, r, store.ErrEmailWorkActionChanged)
			return
		}
		targets = append(targets, store.EmailWorkActionTarget{UID: uid, SourceID: member.RequestedAnchor.SourceID})
	}
	if len(targets) == 0 {
		s.writeEmailWorkActionError(w, r, store.ErrEmailWorkActionChanged)
		return
	}
	undone, err := writer.UndoEmailWorkAction(r.Context(), request.UID, request.Snapshot, request.ExpectedRevision, request.SavedViewID, request.SavedViewRevision)
	if err != nil {
		s.writeEmailWorkActionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, EmailWorkActionResponse{Action: undone, CacheState: s.refreshIdentityCacheState(r.Context())})
}
