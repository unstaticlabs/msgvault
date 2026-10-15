package query

import (
	"context"
	"database/sql"
	"fmt"
	"go.kenn.io/msgvault/internal/store"
	"strings"
)

// fetchProviderStateForMessageList hydrates a bounded result page, never the
// entire archive. It reads the same authority as message detail and labels.
func fetchProviderStateForMessageList(ctx context.Context, db *sql.DB, rebind rebindFunc, prefix, driver string, messages []MessageSummary) error {
	if len(messages) == 0 {
		return nil
	}
	probe := "SELECT COUNT(*) FROM pragma_table_info('imap_message_memberships') WHERE name = 'flags'"
	if driver == "pgx" {
		probe = "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'imap_message_memberships' AND column_name = 'flags'"
	}
	if driver == "duckdb" {
		probe = "SELECT COUNT(*) FROM duckdb_columns() WHERE database_name = 'sqlite_db' AND table_name = 'imap_message_memberships' AND column_name = 'flags'"
	}
	var available int
	if err := db.QueryRowContext(ctx, probe).Scan(&available); err != nil {
		return fmt.Errorf("inspect provider membership metadata: %w", err)
	}
	inbox, read := store.MailboxStateExpressions("m", prefix, driver, available > 0)
	ids := make([]any, len(messages))
	placeholders := make([]string, len(messages))
	positions := make(map[int64]int, len(messages))
	for i, m := range messages {
		ids[i] = m.ID
		placeholders[i] = "?"
		positions[m.ID] = i
		messages[i].InboxState = "unknown"
		messages[i].ReadState = "unknown"
	}
	rows, err := db.QueryContext(ctx, rebind("SELECT m.id, "+inbox+", "+read+" FROM "+prefix+"messages m WHERE m.id IN ("+strings.Join(placeholders, ",")+")"), ids...)
	if err != nil {
		return fmt.Errorf("read provider mailbox state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var inboxState, readState string
		if err := rows.Scan(&id, &inboxState, &readState); err != nil {
			return err
		}
		if i, ok := positions[id]; ok {
			messages[i].InboxState = inboxState
			messages[i].ReadState = readState
		}
	}
	return rows.Err()
}

func (e *SQLiteEngine) fetchProviderState(ctx context.Context, messages []MessageSummary) error {
	driver := "sqlite3"
	if e.dialect.messageBodyContextBackend() == messageBodyContextPostgreSQL {
		driver = "pgx"
	}
	return fetchProviderStateForMessageList(ctx, e.db, e.dialect.Rebind, "", driver, messages)
}
