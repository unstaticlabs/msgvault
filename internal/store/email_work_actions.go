package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const emailWorkCurationRevisionKey = "email_work_curation_revision"

var (
	ErrEmailWorkActionInvalid = errors.New("invalid local email work action")
	ErrEmailWorkActionChanged = errors.New("local email work changed; review it again")
)

// EmailWorkAction is an explicit local assertion, never automatic MIME proof.
// Targets are opaque immutable seeds; no mail content is copied into the journal.
type EmailWorkAction struct {
	UID         string    `json:"uid"`
	Kind        string    `json:"kind"`
	Revision    int64     `json:"revision"`
	Active      bool      `json:"active"`
	ActorKind   string    `json:"actor_kind"`
	ActorUserID int64     `json:"actor_user_id"`
	CreatedAt   time.Time `json:"created_at"`
	Targets     []string  `json:"targets"`
}

type EmailWorkActionTarget struct {
	UID      string `json:"uid"`
	SourceID int64  `json:"source_id"`
}

// EmailWorkActionInput supplies authorized, previewed fixed targets and its actor.
// Source IDs are identity checks, not assertions of permission by a client.
type EmailWorkActionInput struct {
	UID                  string
	Kind                 string
	Targets              []EmailWorkActionTarget
	Snapshot             string
	ActorKind            string
	ActorUserID          int64
	ExpectedViewID       int64
	ExpectedViewRevision int64
}

func validEmailWorkOpaqueID(uid string) bool {
	decoded, err := hex.DecodeString(uid)
	return err == nil && len(decoded) == 16 && uid == strings.ToLower(uid)
}

func emailWorkRevision(q rowQuerier, key string) (int64, error) {
	var value string
	err := q.QueryRow(`SELECT value FROM archive_metadata WHERE key=?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("read local work revision: %w", err)
	}
	return revision, nil
}

// EmailWorkRecoverySnapshotFor stamps an already-pinned native read snapshot.
// The cache publisher and local-action transactions use the same calculation.
func EmailWorkRecoverySnapshotFor(q rowQuerier) (string, error) {
	derived, err := emailWorkRevision(q, derivedDataRevisionKey)
	if err != nil {
		return "", err
	}
	account, err := readAccountIdentityRevision(q)
	if err != nil {
		return "", err
	}
	curation, err := emailWorkRevision(q, emailWorkCurationRevisionKey)
	if err != nil {
		return "", err
	}
	var count, maxID int64
	var watermark sql.NullString
	err = q.QueryRow(`SELECT COUNT(*),COALESCE(MAX(id),0),MAX(content_changed_at) FROM messages`).Scan(&count, &maxID, &watermark)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d:%d:%d:%s", derived, account, curation, count, maxID, watermark.String)))
	return hex.EncodeToString(sum[:]), nil
}

// EmailWorkRecoverySnapshot is an optimistic token, not serialized authority.
// Import, identity, evidence, local action and deletion changes invalidate it.
func (s *Store) EmailWorkRecoverySnapshot(ctx context.Context) (string, error) {
	var token string
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error {
		var err error
		token, err = EmailWorkRecoverySnapshotFor(boundQuerier{ctx: ctx, q: tx})
		return err
	})
	return token, err
}

func readEmailWorkAction(ctx context.Context, tx *loggedTx, uid string) (*EmailWorkAction, error) {
	action := &EmailWorkAction{Targets: []string{}}
	err := tx.QueryRowContext(ctx, `SELECT uid,kind,revision,active,actor_kind,actor_user_id,created_at FROM email_work_actions WHERE uid=?`, uid).Scan(&action.UID, &action.Kind, &action.Revision, &action.Active, &action.ActorKind, &action.ActorUserID, &action.CreatedAt)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT seed_uid FROM email_work_action_targets WHERE action_uid=? ORDER BY seed_uid`, uid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			return nil, err
		}
		action.Targets = append(action.Targets, target)
	}
	return action, rows.Err()
}

func (s *Store) EmailWorkAction(ctx context.Context, uid string) (*EmailWorkAction, error) {
	var action *EmailWorkAction
	err := s.withReadSnapshotContext(ctx, func(tx *loggedTx) error { var err error; action, err = readEmailWorkAction(ctx, tx, uid); return err })
	return action, err
}

func (s *Store) lockEmailWorkCuration(ctx context.Context, tx *loggedTx) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO archive_metadata (key,value) VALUES (?,'0') ON CONFLICT(key) DO NOTHING`, emailWorkCurationRevisionKey); err != nil {
		return err
	}
	var value string
	return tx.QueryRowContext(ctx, `SELECT value FROM archive_metadata WHERE key=?`+s.dialect.SelectForUpdate(), emailWorkCurationRevisionKey).Scan(&value)
}

func (s *Store) advanceEmailWorkCuration(ctx context.Context, tx *loggedTx) error {
	if _, err := tx.ExecContext(ctx, `UPDATE archive_metadata SET value=CAST(CAST(value AS BIGINT)+1 AS TEXT) WHERE key=?`, emailWorkCurationRevisionKey); err != nil {
		return err
	}
	return s.bumpDerivedDataRevision(tx)
}

func (s *Store) CreateEmailWorkAction(ctx context.Context, input EmailWorkActionInput) (*EmailWorkAction, error) {
	if !validEmailWorkOpaqueID(input.UID) || (input.Kind != "link" && input.Kind != "handled") || len(input.Targets) < 1 || len(input.Targets) > 100 || (input.Kind == "link" && len(input.Targets) < 2) || len(input.Snapshot) != 64 || input.ActorKind == "" || len(input.ActorKind) > 32 || input.ActorUserID < 0 {
		return nil, ErrEmailWorkActionInvalid
	}
	targets := slices.Clone(input.Targets)
	slices.SortFunc(targets, func(a, b EmailWorkActionTarget) int { return strings.Compare(a.UID, b.UID) })
	var uids []string
	for i, target := range targets {
		if !validEmailWorkOpaqueID(target.UID) || target.SourceID < 1 || (i > 0 && target.UID == targets[i-1].UID) {
			return nil, ErrEmailWorkActionInvalid
		}
		uids = append(uids, target.UID)
	}
	var result *EmailWorkAction
	err := s.withTxOptionsContext(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *loggedTx) error {
		if err := s.lockEmailWorkCuration(ctx, tx); err != nil {
			return err
		}
		existing, err := readEmailWorkAction(ctx, tx, input.UID)
		if err == nil {
			if existing.Kind != input.Kind || existing.ActorKind != input.ActorKind || existing.ActorUserID != input.ActorUserID || !slices.Equal(existing.Targets, uids) {
				return ErrEmailWorkActionChanged
			}
			result = existing
			return nil // A retry never reactivates an undone action.
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		snapshot, err := EmailWorkRecoverySnapshotFor(boundQuerier{ctx: ctx, q: tx})
		if err != nil {
			return err
		}
		if snapshot != input.Snapshot {
			return ErrEmailWorkActionChanged
		}
		if err := s.verifyEmailWorkSavedView(ctx, tx, input.ExpectedViewID, input.ExpectedViewRevision); err != nil {
			return err
		}
		for _, target := range targets {
			var source int64
			var messageType string
			err := tx.QueryRowContext(ctx, `SELECT m.source_id,COALESCE(NULLIF(m.message_type,''),'email') FROM email_work_seeds seed JOIN messages m ON m.id=seed.live_message_id WHERE seed.uid=?`+s.dialect.SelectForUpdate(), target.UID).Scan(&source, &messageType)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrEmailWorkActionChanged
			}
			if err != nil {
				return err
			}
			if source != target.SourceID || messageType != MessageTypeEmail {
				return ErrEmailWorkActionChanged
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO email_work_actions (uid,kind,actor_kind,actor_user_id) VALUES (?,?,?,?)`, input.UID, input.Kind, input.ActorKind, input.ActorUserID); err != nil {
			return err
		}
		for _, target := range targets {
			if _, err := tx.ExecContext(ctx, `INSERT INTO email_work_action_targets (action_uid,seed_uid) VALUES (?,?)`, input.UID, target.UID); err != nil {
				return err
			}
		}
		if err := s.advanceEmailWorkCuration(ctx, tx); err != nil {
			return err
		}
		result, err = readEmailWorkAction(ctx, tx, input.UID)
		return err
	})
	return result, err
}

// UndoEmailWorkAction changes exactly one reviewed assertion. Other actions
// remain intact. Repeated undo is a no-op; retrying creation cannot resurrect it.
func (s *Store) UndoEmailWorkAction(ctx context.Context, uid, snapshot string, expectedRevision int64, view ...int64) (*EmailWorkAction, error) {
	if !validEmailWorkOpaqueID(uid) || len(snapshot) != 64 || expectedRevision < 1 {
		return nil, ErrEmailWorkActionInvalid
	}
	var result *EmailWorkAction
	err := s.withTxOptionsContext(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *loggedTx) error {
		if err := s.lockEmailWorkCuration(ctx, tx); err != nil {
			return err
		}
		if len(view) != 0 && len(view) != 2 {
			return ErrEmailWorkActionInvalid
		}
		if len(view) == 2 {
			if err := s.verifyEmailWorkSavedView(ctx, tx, view[0], view[1]); err != nil {
				return err
			}
		}
		action, err := readEmailWorkAction(ctx, tx, uid)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEmailWorkActionChanged
		}
		if err != nil {
			return err
		}
		if !action.Active && (action.Revision == expectedRevision+1 || action.Revision == expectedRevision) {
			result = action
			return nil
		}
		if action.Revision != expectedRevision {
			return ErrEmailWorkActionChanged
		}
		current, err := EmailWorkRecoverySnapshotFor(boundQuerier{ctx: ctx, q: tx})
		if err != nil {
			return err
		}
		if current != snapshot {
			return ErrEmailWorkActionChanged
		}
		if _, err := tx.ExecContext(ctx, `UPDATE email_work_actions SET active=FALSE,revision=revision+1 WHERE uid=? AND revision=?`, uid, expectedRevision); err != nil {
			return err
		}
		if err := s.advanceEmailWorkCuration(ctx, tx); err != nil {
			return err
		}
		result, err = readEmailWorkAction(ctx, tx, uid)
		return err
	})
	return result, err
}

func (s *Store) verifyEmailWorkSavedView(ctx context.Context, tx *loggedTx, id, revision int64) error {
	if id == 0 && revision == 0 {
		return nil
	}
	if id < 1 || revision < 1 {
		return ErrSavedViewRevisionConflict
	}
	var current int64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM saved_views WHERE id=?`+s.dialect.SelectForUpdate(), id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSavedViewRevisionConflict
	}
	if err != nil {
		return err
	}
	if current != revision {
		return ErrSavedViewRevisionConflict
	}
	return nil
}
