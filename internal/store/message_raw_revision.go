package store

import (
	"context"
	"fmt"
)

const messageRawRevisionKey = "message_raw_revision"

// MessageRawRevision pins native raw-byte writes independently of analytical
// linkage. Existing archives begin at zero; their first changed raw write seeds
// the counter in the same transaction, without rewriting legacy messages.
func (s *Store) MessageRawRevision(ctx context.Context) (int64, error) {
	return emailWorkRevision(boundQuerier{ctx: ctx, q: s.db}, messageRawRevisionKey)
}

func bumpMessageRawRevisionWith(q querier) error {
	if _, err := q.Exec(`INSERT INTO archive_metadata (key,value) VALUES (?, '0') ON CONFLICT(key) DO NOTHING`, messageRawRevisionKey); err != nil {
		return fmt.Errorf("seed raw-content revision: %w", err)
	}
	if _, err := q.Exec(`UPDATE archive_metadata SET value=CAST(CAST(value AS BIGINT)+1 AS TEXT) WHERE key=?`, messageRawRevisionKey); err != nil {
		return fmt.Errorf("advance raw-content revision: %w", err)
	}
	return nil
}
