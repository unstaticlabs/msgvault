package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"go.kenn.io/msgvault/internal/mime"
)

func (s *Store) extractEmailLinkageTx(ctx context.Context, tx *loggedTx, messageID int64, raw []byte, format string) error {
	var messageType string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(message_type, '') FROM messages WHERE id = ?`, messageID).Scan(&messageType); err != nil {
		return err
	}
	if format != "mime" || (messageType != MessageTypeEmail && messageType != "") {
		result, err := tx.ExecContext(ctx, `DELETE FROM email_linkage_facts WHERE message_id = ?`, messageID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed == 0 {
			return err
		}
		return s.bumpDerivedDataRevision(tx)
	}
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	var version int
	var oldFingerprint string
	var hasSeed bool
	err := tx.QueryRowContext(ctx, `SELECT extraction_version, raw_sha256, EXISTS (SELECT 1 FROM email_work_seeds WHERE live_message_id = ?) FROM email_linkage_facts WHERE message_id = ?`, messageID, messageID).Scan(&version, &oldFingerprint, &hasSeed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && version == mime.EmailLinkageVersion && oldFingerprint == fingerprint && hasSeed {
		return nil
	}
	_, err = s.storeEmailLinkageTx(ctx, tx, messageID, mime.ExtractEmailLinkage(raw), fingerprint)
	return err
}

func (s *Store) storeEmailLinkageTx(ctx context.Context, tx *loggedTx, messageID int64, evidence mime.EmailLinkage, fingerprint string) (bool, error) {
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return false, err
	}
	var oldFacts string
	err = tx.QueryRowContext(ctx, `SELECT facts FROM email_linkage_facts WHERE message_id = ?`, messageID).Scan(&oldFacts)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	factsChanged := errors.Is(err, sql.ErrNoRows) || oldFacts != string(encoded)
	allocated, err := ensureEmailWorkSeed(boundQuerier{ctx: ctx, q: tx}, messageID)
	if err != nil {
		return false, err
	}
	changed := allocated || factsChanged
	if _, err := tx.ExecContext(ctx, `INSERT INTO email_linkage_facts (message_id, extraction_version, outcome, facts, raw_sha256)
 VALUES (?, ?, ?, ?, ?) ON CONFLICT(message_id) DO UPDATE SET extraction_version = excluded.extraction_version,
 outcome = excluded.outcome, facts = excluded.facts, raw_sha256 = excluded.raw_sha256`, messageID, evidence.Version, evidence.Outcome, string(encoded), fingerprint); err != nil {
		return false, err
	}
	if changed {
		return true, s.bumpDerivedDataRevision(tx)
	}
	return false, nil
}

// EmailLinkageEvidence returns saved explicit facts; a missing extraction stays
// distinguishable from a complete extraction with no identifiers.
func (s *Store) EmailLinkageEvidence(ctx context.Context, messageID int64) (mime.EmailLinkage, bool, error) {
	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT facts FROM email_linkage_facts WHERE message_id = ?`, messageID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return mime.EmailLinkage{}, false, nil
	}
	if err != nil {
		return mime.EmailLinkage{}, false, err
	}
	var evidence mime.EmailLinkage
	if err := json.Unmarshal([]byte(encoded), &evidence); err != nil {
		return mime.EmailLinkage{}, false, fmt.Errorf("decode email linkage: %w", err)
	}
	return evidence, true, nil
}

// EmailLinkageBackfill reports one resumable, source-pinned extraction batch.
type EmailLinkageBackfill struct {
	Processed     int   `json:"processed"`
	Changed       int   `json:"changed"`
	LastMessageID int64 `json:"last_message_id"`
	Done          bool  `json:"done"`
}

// BackfillEmailLinkage extracts archived MIME without provider access. Facts,
// actual-change revision and checkpoint commit together. Corrupt/oversized raw
// messages get an incomplete outcome and cannot strand later messages.
func (s *Store) BackfillEmailLinkage(ctx context.Context, sourceID int64, limit int) (*EmailLinkageBackfill, error) {
	if sourceID < 1 || limit < 1 || limit > 1000 {
		return nil, errors.New("email linkage backfill requires a positive source and batch size from 1 to 1000")
	}
	result := &EmailLinkageBackfill{}
	err := s.withTxContext(ctx, func(tx *loggedTx) error {
		// Reserve SQLite's writer slot before checkpoint/prior-fact reads.
		if !s.IsPostgreSQL() {
			if _, err := tx.ExecContext(ctx, `UPDATE embedding_change_clock SET sequence = sequence WHERE singleton = 1`); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO email_linkage_checkpoints (source_id, extraction_version, last_message_id) VALUES (?, ?, 0) ON CONFLICT(source_id, extraction_version) DO NOTHING`, sourceID, mime.EmailLinkageVersion); err != nil {
			return err
		}
		checkpointSQL := `SELECT last_message_id FROM email_linkage_checkpoints WHERE source_id = ? AND extraction_version = ?`
		checkpointSQL += s.dialect.SelectForUpdate()
		if err := tx.QueryRowContext(ctx, checkpointSQL, sourceID, mime.EmailLinkageVersion).Scan(&result.LastMessageID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT m.id FROM messages m WHERE m.source_id = ? AND m.message_type IN ('email', '') AND (m.id > ? OR NOT EXISTS (SELECT 1 FROM email_linkage_facts f WHERE f.message_id = m.id AND f.extraction_version = ?) OR NOT EXISTS (SELECT 1 FROM email_work_seeds seed WHERE seed.live_message_id = m.id)) ORDER BY m.id LIMIT ?`, sourceID, result.LastMessageID, mime.EmailLinkageVersion, limit+1)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		scanErr := rows.Err()
		_ = rows.Close()
		if scanErr != nil {
			return scanErr
		}
		result.Done = len(ids) <= limit
		ids = ids[:min(len(ids), limit)]
		for _, id := range ids {
			rawSQL := `SELECT CASE WHEN length(raw_data) <= ? THEN raw_data ELSE NULL END, compression, length(raw_data), raw_format FROM message_raw WHERE message_id = ?`
			rawSQL += s.dialect.SelectForUpdate()
			var compressed []byte
			var compression sql.NullString
			var storedSize int64
			var format sql.NullString
			rawErr := tx.QueryRowContext(ctx, rawSQL, mime.EmailLinkageMaxBytes, id).Scan(&compressed, &compression, &storedSize, &format)
			if rawErr != nil && !errors.Is(rawErr, sql.ErrNoRows) {
				return rawErr
			}
			evidence := mime.EmailLinkage{Version: mime.EmailLinkageVersion, Outcome: "size_limit"}
			fingerprint := ""
			switch {
			case errors.Is(rawErr, sql.ErrNoRows):
				evidence.Outcome = "missing_raw"
			case !format.Valid || format.String != "mime":
				evidence.Outcome = "unsupported_raw"
			case storedSize <= mime.EmailLinkageMaxBytes:
				limitErr := errors.New("email linkage raw size limit")
				raw, _, decodeErr := decodeMessageRawBounded(compressed, compression, mime.EmailLinkageMaxBytes, limitErr)
				if errors.Is(decodeErr, limitErr) {
					evidence.Outcome = "size_limit"
				} else if decodeErr != nil {
					evidence.Outcome = "invalid_raw"
				} else {
					evidence = mime.ExtractEmailLinkage(raw)
					sum := sha256.Sum256(raw)
					fingerprint = hex.EncodeToString(sum[:])
				}
			}
			changed, err := s.storeEmailLinkageTx(ctx, tx, id, evidence, fingerprint)
			if err != nil {
				return err
			}
			result.Processed++
			if changed {
				result.Changed++
			}
			result.LastMessageID = max(result.LastMessageID, id)
		}
		_, err = tx.ExecContext(ctx, `UPDATE email_linkage_checkpoints SET last_message_id = ? WHERE source_id = ? AND extraction_version = ?`, result.LastMessageID, sourceID, mime.EmailLinkageVersion)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
