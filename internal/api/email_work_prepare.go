package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/msgvault/internal/store"
)

type EmailWorkPreparationStore interface {
	BackfillEmailLinkage(ctx context.Context, sourceID int64, limit int) (*store.EmailLinkageBackfill, error)
	GetSourceByIDContext(ctx context.Context, id int64) (*store.Source, error)
}

type EmailWorkPrepareRequest struct {
	SourceID  int64 `json:"source_id" minimum:"1"`
	BatchSize int   `json:"batch_size" minimum:"1" maximum:"1000"`
}

type EmailWorkPrepareResponse struct {
	Progress   *store.EmailLinkageBackfill `json:"progress"`
	CacheState string                      `json:"cache_state" enum:"ready,stale"`
}

func (s *Server) registerEmailWorkPreparationRoute(api huma.API) {
	registerAPIV1RawHumaJSONRouteWithRequest[EmailWorkPrepareRequest, EmailWorkPrepareResponse](api, "prepareEmailWork", http.MethodPost, "/explore/email-work/prepare", "Prepare one resumable batch of archived email evidence", s.handleEmailWorkPrepare)
}

func (s *Server) handleEmailWorkPrepare(w http.ResponseWriter, r *http.Request) {
	var request EmailWorkPrepareRequest
	if !decodeExploreJSON(w, r, &request) {
		return
	}
	if request.SourceID < 1 || request.BatchSize < 1 || request.BatchSize > 1000 {
		writeError(w, http.StatusBadRequest, "invalid_work_preparation", "Choose a positive account ID and a batch size from 1 to 1000")
		return
	}
	if !principalFromContext(r.Context()).Sees(request.SourceID) {
		writeError(w, http.StatusNotFound, "source_not_found", "Account unavailable")
		return
	}
	writer, ok := s.store.(EmailWorkPreparationStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "work_preparation_unavailable", "Archived evidence preparation is unavailable")
		return
	}
	if _, err := writer.GetSourceByIDContext(r.Context(), request.SourceID); err != nil {
		if errors.Is(err, store.ErrSourceNotFound) {
			writeError(w, http.StatusNotFound, "source_not_found", "Account unavailable")
		} else {
			s.writeEmailWorkActionError(w, r, err)
		}
		return
	}
	progress, err := writer.BackfillEmailLinkage(r.Context(), request.SourceID, request.BatchSize)
	if err != nil {
		s.writeEmailWorkActionError(w, r, err)
		return
	}
	// The extraction transaction commits before publication. An interrupted
	// refresh is reported honestly and the durable checkpoint remains resumable.
	state := identityCacheStateStale
	if progress.Done {
		state = s.refreshIdentityCacheState(r.Context())
	}
	writeJSON(w, http.StatusOK, EmailWorkPrepareResponse{Progress: progress, CacheState: state})
}
