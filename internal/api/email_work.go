package api

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

type EmailWorkMembersHTTPRequest struct {
	UID           string             `json:"uid" minLength:"1" maxLength:"128"`
	Predicate     ExploreHTTPRequest `json:"predicate"`
	CacheRevision string             `json:"cache_revision,omitempty"`
	Limit         int                `json:"limit,omitempty" minimum:"0" maximum:"500"`
	Offset        int                `json:"offset,omitempty" minimum:"0"`
}

func emailWorkAuthorityKey(ctx context.Context) string {
	principal := principalFromContext(ctx)
	sources := slices.Clone(principal.VisibleSourceIDs)
	slices.Sort(sources)
	sources = slices.Compact(sources)
	return hashCanonicalValue(struct {
		Kind    string  `json:"kind"`
		Role    string  `json:"role"`
		UserID  int64   `json:"user_id"`
		Sources []int64 `json:"sources"`
	}{Kind: string(principal.Kind), Role: string(principal.Role), UserID: principal.UserID, Sources: sources}, false)
}

func (s *Server) registerEmailWorkRoutes(api huma.API) {
	registerExploreRoute[EmailWorkMembersHTTPRequest, query.EmailWorkMembersResponse](api, "getEmailWorkMembers", "/explore/email-work/members", "Read every permitted source copy and link evidence in an email work item", s.handleEmailWorkMembers)
}

func (s *Server) handleEmailWorkMembers(w http.ResponseWriter, r *http.Request) {
	var request EmailWorkMembersHTTPRequest
	if !decodeExploreJSON(w, r, &request) {
		return
	}
	if request.UID == "" || len(request.UID) > 128 || request.Limit < 0 || request.Limit > exploreMaxLimit || request.Offset < 0 {
		writeError(w, http.StatusBadRequest, "invalid_work_item_request", "A work item link and valid page are required")
		return
	}
	prepared, err := s.prepareResolvedExplorePredicate(r.Context(), request.Predicate)
	if err != nil {
		s.writeExploreFilterError(w, err, "invalid_filter")
		return
	}
	prepared.query.Context.RowUnit = query.RowUnitEmailWork
	prepared.query.Context.WorkAuthorityKey = emailWorkAuthorityKey(r.Context())
	reader, ok := s.queryEngineForContext(r.Context()).(query.EmailWorkExplorer)
	if !ok {
		s.writeExploreUnavailable(r.Context(), w, query.CacheAbsent)
		return
	}
	response, err := reader.EmailWorkMembers(r.Context(), query.EmailWorkMembersRequest{Explore: prepared.query, UID: request.UID, ExpectedCacheRevision: request.CacheRevision, Page: query.PageSpec{Limit: request.Limit, Offset: request.Offset}})
	if err != nil {
		if errors.Is(err, store.ErrEmailWorkAliasGone) || errors.Is(err, store.ErrEmailWorkAliasMissing) || errors.Is(err, store.ErrEmailWorkAliasOutOfScope) {
			writeError(w, http.StatusNotFound, "work_item_unavailable", "This work item is unavailable in the selected accounts. Its original message may have been removed.")
			return
		}
		s.writeExploreError(r.Context(), w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

// EmailWorkIdentityStore binds a cached member link to its current native UID.
type EmailWorkIdentityStore interface {
	EmailWorkSeedForMessage(ctx context.Context, id int64) (*store.EmailWorkSeed, error)
}

func (s *Server) validateExpectedMessageUID(w http.ResponseWriter, r *http.Request, id int64) bool {
	expected := r.URL.Query().Get("expected_message_uid")
	if expected == "" {
		return true
	}
	if len(expected) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_message_identity", "The expected message identity is invalid")
		return false
	}
	reader, ok := s.store.(EmailWorkIdentityStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "message_identity_unavailable", "The archived message identity cannot be checked yet")
		return false
	}
	seed, err := reader.EmailWorkSeedForMessage(r.Context(), id)
	if err != nil && !errors.Is(err, store.ErrEmailWorkAliasMissing) {
		if s.writeIfContextError(w, err) {
			return false
		}
		writeError(w, http.StatusServiceUnavailable, "message_identity_unavailable", "The archived copy identity cannot be checked right now. Try again.")
		return false
	}
	if err != nil || seed == nil || seed.UID != expected {
		writeError(w, http.StatusConflict, "message_identity_changed", "This archived copy changed. Refresh the work item before opening it.")
		return false
	}
	return true
}
