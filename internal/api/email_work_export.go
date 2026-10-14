package api

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

type emailWorkExportSnapshotReader interface {
	EmailWorkRecoverySnapshot(ctx context.Context) (string, error)
}

type EmailWorkExportRequest struct {
	UID           string             `json:"uid" minLength:"1" maxLength:"128"`
	Predicate     ExploreHTTPRequest `json:"predicate"`
	CacheRevision string             `json:"cache_revision" minLength:"1"`
}

type EmailWorkExportCopy struct {
	Member   query.EmailWorkMemberFact `json:"member"`
	Filename string                    `json:"filename,omitempty"`
	State    string                    `json:"state"`
}

type EmailWorkExportManifest struct {
	UID           string                `json:"uid"`
	RequestedUID  string                `json:"requested_uid"`
	CacheRevision string                `json:"cache_revision"`
	State         string                `json:"state"`
	Copies        []EmailWorkExportCopy `json:"copies"`
}

func (s *Server) registerEmailWorkExportRoute(api huma.API) {
	op := rawAPIV1Operation("exportEmailWorkItem", http.MethodPost, "/explore/email-work/export", "Export every permitted archived copy and its provenance")
	op.Tags = []string{"Exploration"}
	op.RequestBody = jsonRequestBodyFor[EmailWorkExportRequest](api)
	op.Responses = binaryResponsesFor(api, "application/zip", http.StatusConflict, http.StatusServiceUnavailable)
	registerRawHumaRoute(api, op, s.handleEmailWorkExport)
}

// Build the complete, revision-pinned archive before publishing any bytes.
// A missing raw copy remains in the manifest; export never fetches a provider.
func (s *Server) handleEmailWorkExport(w http.ResponseWriter, r *http.Request) {
	var request EmailWorkExportRequest
	if !decodeExploreJSON(w, r, &request) {
		return
	}
	if request.UID == "" || len(request.UID) > 128 || request.CacheRevision == "" {
		writeError(w, http.StatusBadRequest, "invalid_work_item_request", "A work item link and current cache revision are required")
		return
	}
	prepared, err := s.prepareResolvedExplorePredicate(r.Context(), request.Predicate)
	if err != nil {
		s.writeExploreFilterError(w, err, "invalid_filter")
		return
	}
	prepared.query.Context.RowUnit = query.RowUnitEmailWork
	prepared.query.Context.WorkAuthorityKey = emailWorkAuthorityKey(r.Context())
	engine := s.queryEngineForContext(r.Context())
	reader, ok := engine.(query.EmailWorkExplorer)
	if !ok {
		s.writeExploreUnavailable(r.Context(), w, query.CacheAbsent)
		return
	}
	identity, ok := s.store.(EmailWorkIdentityStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "message_identity_unavailable", "Archived copy identities cannot be checked yet")
		return
	}
	snapshotReader, ok := s.store.(emailWorkExportSnapshotReader)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "message_identity_unavailable", "The native archive revision cannot be checked yet")
		return
	}
	var expectedSnapshot string
	file, err := os.CreateTemp("", "msgvault-work-export-*.zip")
	if err != nil {
		s.writeEmailWorkExportError(w, r, err)
		return
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	writer := zip.NewWriter(file)
	defer func() { _ = writer.Close() }()
	manifest := EmailWorkExportManifest{RequestedUID: request.UID, CacheRevision: request.CacheRevision, State: "complete", Copies: []EmailWorkExportCopy{}}
	seen := map[string]bool{}
	offset := 0
	for {
		page, readErr := reader.EmailWorkMembers(r.Context(), query.EmailWorkMembersRequest{Explore: prepared.query, UID: request.UID, ExpectedCacheRevision: request.CacheRevision, Page: query.PageSpec{Limit: 500, Offset: offset}})
		if readErr != nil {
			s.writeEmailWorkExportError(w, r, readErr)
			return
		}
		manifest.UID = page.UID
		if page.RecoverySnapshot == "" || (expectedSnapshot != "" && expectedSnapshot != page.RecoverySnapshot) {
			s.writeEmailWorkExportError(w, r, query.ErrEmailWorkRevisionChanged)
			return
		}
		expectedSnapshot = page.RecoverySnapshot
		for _, member := range page.Members {
			if member.MessageUID == "" || seen[member.MessageUID] {
				s.writeEmailWorkExportError(w, r, query.ErrEmailWorkRevisionChanged)
				return
			}
			seen[member.MessageUID] = true
			verify := func() error {
				seed, seedErr := identity.EmailWorkSeedForMessage(r.Context(), member.MessageID)
				if seedErr != nil {
					return seedErr
				}
				if seed == nil || seed.UID != member.MessageUID {
					return query.ErrEmailWorkRevisionChanged
				}
				return nil
			}
			if err = verify(); err != nil {
				s.writeEmailWorkExportError(w, r, err)
				return
			}
			var raw []byte
			if archived, available := s.store.(archivedMessageRawReader); available {
				raw, err = archived.GetArchivedMessageRaw(r.Context(), member.MessageID)
			} else {
				raw, err = engine.GetMessageRaw(r.Context(), member.MessageID)
			}
			rawErr := err
			// A physical ID reused while reading must never publish replacement bytes.
			if err = verify(); err != nil {
				s.writeEmailWorkExportError(w, r, err)
				return
			}
			copyEntry := EmailWorkExportCopy{Member: member, State: "raw_unavailable"}
			if rawErr == nil && len(raw) > 0 {
				copyEntry.State = "available"
				copyEntry.Filename = fmt.Sprintf("messages/%s.eml", member.MessageUID)
				entry, createErr := writer.Create(copyEntry.Filename)
				if createErr != nil {
					s.writeEmailWorkExportError(w, r, createErr)
					return
				}
				if _, err = entry.Write(raw); err != nil {
					s.writeEmailWorkExportError(w, r, err)
					return
				}
			} else {
				if errors.Is(rawErr, r.Context().Err()) && r.Context().Err() != nil {
					s.writeEmailWorkExportError(w, r, rawErr)
					return
				}
				manifest.State = "partial"
			}
			manifest.Copies = append(manifest.Copies, copyEntry)
		}
		offset += len(page.Members)
		if offset >= page.TotalCount {
			break
		}
		if len(page.Members) == 0 {
			s.writeEmailWorkExportError(w, r, query.ErrEmailWorkRevisionChanged)
			return
		}
	}
	// Re-probe both the native archive and immutable publication after the last raw read.
	nativeSnapshot, snapshotErr := snapshotReader.EmailWorkRecoverySnapshot(r.Context())
	if snapshotErr != nil {
		s.writeEmailWorkExportError(w, r, snapshotErr)
		return
	}
	if nativeSnapshot != expectedSnapshot {
		s.writeEmailWorkExportError(w, r, query.ErrEmailWorkRevisionChanged)
		return
	}
	if _, err = reader.EmailWorkMembers(r.Context(), query.EmailWorkMembersRequest{Explore: prepared.query, UID: request.UID, ExpectedCacheRevision: request.CacheRevision, Page: query.PageSpec{Limit: 1}}); err != nil {
		s.writeEmailWorkExportError(w, r, err)
		return
	}
	entry, err := writer.Create("manifest.json")
	if err != nil {
		s.writeEmailWorkExportError(w, r, err)
		return
	}
	if err = json.NewEncoder(entry).Encode(manifest); err != nil {
		s.writeEmailWorkExportError(w, r, err)
		return
	}
	if err = writer.Close(); err != nil {
		s.writeEmailWorkExportError(w, r, err)
		return
	}
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		s.writeEmailWorkExportError(w, r, err)
		return
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		s.writeEmailWorkExportError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"email-work-%s.zip\"", manifest.UID))
	w.Header().Set("X-Email-Work-Export-State", manifest.State)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	if _, err = io.Copy(w, file); err != nil {
		s.logger.Warn("email work export interrupted", "error", err)
	}
}

func (s *Server) writeEmailWorkExportError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrEmailWorkAliasGone), errors.Is(err, store.ErrEmailWorkAliasMissing), errors.Is(err, store.ErrEmailWorkAliasOutOfScope), errors.Is(err, query.ErrEmailWorkRevisionChanged), errors.Is(err, query.ErrCacheUnavailable):
		s.writeEmailWorkActionError(w, r, err)
	default:
		if s.writeIfContextError(w, err) {
			return
		}
		s.logger.Error("email work export failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "export_unavailable", "The export could not be completed. Your archived copies were preserved. Refresh and try again.")
	}
}
