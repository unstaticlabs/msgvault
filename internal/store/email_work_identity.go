package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
)

var (
	ErrEmailWorkAliasMissing    = errors.New("email work item link is unavailable")
	ErrEmailWorkAliasGone       = errors.New("email work item anchor was deleted")
	ErrEmailWorkAliasOutOfScope = errors.New("email work item anchor is outside the selected population")
)

// EmailWorkSeed retains only opaque allocation and anchor identity metadata.
// AnchorMessageID never changes. A NULL LiveMessageID is a tombstone, even if
// a later insert reuses the same physical message ID.
type EmailWorkSeed struct {
	Ordinal         int64
	UID             string
	AnchorMessageID int64
	LiveMessageID   sql.NullInt64
}

func ensureEmailWorkSeed(q querier, messageID int64) (bool, error) {
	var exists int
	err := q.QueryRow(`SELECT 1 FROM email_work_seeds WHERE live_message_id = ?`, messageID).Scan(&exists)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return false, fmt.Errorf("allocate email work identity: %w", err)
	}
	result, err := q.Exec(`INSERT INTO email_work_seeds (uid,anchor_message_id,live_message_id) VALUES (?,?,?) ON CONFLICT(live_message_id) DO NOTHING`, hex.EncodeToString(random), messageID, messageID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

// EmailWorkSeedForMessage reads an allocated live seed. It never allocates on
// behalf of a reader; import/persistence and offline publication repair do so.
func (s *Store) EmailWorkSeedForMessage(ctx context.Context, messageID int64) (*EmailWorkSeed, error) {
	var seed EmailWorkSeed
	err := s.db.QueryRowContext(ctx, `SELECT ordinal, uid, anchor_message_id, live_message_id FROM email_work_seeds WHERE live_message_id = ?`, messageID).Scan(&seed.Ordinal, &seed.UID, &seed.AnchorMessageID, &seed.LiveMessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEmailWorkAliasMissing
	}
	if err != nil {
		return nil, err
	}
	return &seed, nil
}

func (s *Store) EmailWorkSeedForAlias(ctx context.Context, uid string) (*EmailWorkSeed, error) {
	var seed EmailWorkSeed
	err := s.db.QueryRowContext(ctx, `SELECT ordinal, uid, anchor_message_id, live_message_id FROM email_work_seeds WHERE uid = ?`, uid).Scan(&seed.Ordinal, &seed.UID, &seed.AnchorMessageID, &seed.LiveMessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEmailWorkAliasMissing
	}
	if err != nil {
		return nil, err
	}
	return &seed, nil
}

// ResolveEmailWorkAlias follows the alias's own live anchor in the current
// authorized projection. Union/split redirects are recomputed, not irreversible
// parent pointers. An excluded/deleted anchor cannot relocate to another item.
func ResolveEmailWorkAlias(alias EmailWorkSeed, components [][]int64, seeds []EmailWorkSeed) (string, error) {
	if !alias.LiveMessageID.Valid {
		return "", ErrEmailWorkAliasGone
	}
	for _, members := range components {
		if !slices.Contains(members, alias.LiveMessageID.Int64) {
			continue
		}
		var oldest *EmailWorkSeed
		for i := range seeds {
			seed := &seeds[i]
			if seed.LiveMessageID.Valid && slices.Contains(members, seed.LiveMessageID.Int64) && (oldest == nil || seed.Ordinal < oldest.Ordinal) {
				oldest = seed
			}
		}
		if oldest == nil {
			return "", ErrEmailWorkAliasMissing
		}
		return oldest.UID, nil
	}
	return "", ErrEmailWorkAliasOutOfScope
}
