package store

import (
	"context"
	"fmt"
	"strings"
)

// ProviderMailboxStateUnknown represents unavailable provider metadata.
const ProviderMailboxStateUnknown = "unknown"

// MailboxStateExpressions projects provider state from stored labels and IMAP
// flags. Local messages.is_read is deliberately separate. Aliases/prefixes and
// driver are repository-owned SQL names, never caller-supplied filter values.
// Missing membership metadata and unsupported sources remain unknown.
func MailboxStateExpressions(alias, prefix, driver string, hasIMAP bool) (string, string) {
	source := fmt.Sprintf("(SELECT LOWER(ms.source_type) FROM %ssources ms WHERE ms.id = %s.source_id)", prefix, alias)
	eligible := fmt.Sprintf("%s.deleted_at IS NULL AND %s.deleted_from_source_at IS NULL AND COALESCE(%s.source_message_id, '') <> '' AND LOWER(COALESCE(%s.message_type, 'email')) IN ('', 'email')", alias, alias, alias, alias)
	role := func(name string) string {
		return fmt.Sprintf("ml.source_id = %s.source_id AND (ml.source_label_id = '%s' OR (COALESCE(ml.source_label_id, '') = '' AND ml.label_type = 'system' AND UPPER(ml.name) = '%s'))", alias, name, name)
	}
	known := func(name string) string {
		return fmt.Sprintf("EXISTS (SELECT 1 FROM %slabels ml WHERE %s)", prefix, role(name))
	}
	carries := func(name string) string {
		return fmt.Sprintf("EXISTS (SELECT 1 FROM %smessage_labels mlink JOIN %slabels ml ON ml.id = mlink.label_id WHERE mlink.message_id = %s.id AND %s)", prefix, prefix, alias, role(name))
	}
	inbox := fmt.Sprintf("CASE WHEN NOT (%s) THEN 'unknown' WHEN %s = 'gmail' AND %s THEN CASE WHEN %s THEN 'inbox' ELSE 'outside' END ELSE 'unknown' END", eligible, source, known("INBOX"), carries("INBOX"))
	read := fmt.Sprintf("CASE WHEN NOT (%s) THEN 'unknown' WHEN %s = 'gmail' AND %s THEN CASE WHEN %s THEN 'unread' ELSE 'read' END ELSE 'unknown' END", eligible, source, known("UNREAD"), carries("UNREAD"))
	if !hasIMAP {
		return inbox, read
	}
	member := fmt.Sprintf("mm.source_id = %s.source_id AND mm.message_id = %s.id", alias, alias)
	exists := func(condition string) string {
		return fmt.Sprintf("EXISTS (SELECT 1 FROM %simap_message_memberships mm WHERE %s AND (%s))", prefix, member, condition)
	}
	var valid, elements, elementType, elementValue, seen string
	switch driver {
	case "pgx":
		elements = "jsonb_array_elements(CASE WHEN jsonb_typeof(mm.flags) = 'array' THEN mm.flags ELSE '[]'::jsonb END) AS mf(value)"
		elementType = "jsonb_typeof(mf.value) = 'string'"
		elementValue = "mf.value #>> '{}'"
		valid = "jsonb_typeof(mm.flags) = 'array'"
	case "duckdb":
		// Scalar list operations avoid repeated correlated json_each readers
		// during a large cache COPY while retaining strict element-type checks.
		items := "json_extract(TRY_CAST(mm.flags AS JSON), '$[*]')"
		valid = "json_type(TRY_CAST(mm.flags AS JSON)) = 'ARRAY' AND list_has_all(['VARCHAR'], list_transform(" + items + ", flag -> COALESCE(json_type(flag), 'NULL')))"
		seen = "list_contains(list_transform(" + items + ", flag -> LOWER(json_extract_string(flag, '$'))), '\\seen')"
	default:
		elements = "json_each(CASE WHEN json_valid(mm.flags) THEN mm.flags ELSE '[]' END) AS mf"
		elementType = "mf.type = 'text'"
		elementValue = "CAST(mf.value AS TEXT)"
		valid = "CASE WHEN json_valid(mm.flags) THEN json_type(mm.flags) = 'array' ELSE FALSE END"
	}
	if driver != "duckdb" {
		valid += " AND NOT EXISTS (SELECT 1 FROM " + elements + " WHERE NOT (" + elementType + "))"
		seen = "EXISTS (SELECT 1 FROM " + elements + " WHERE " + elementType + " AND LOWER(" + elementValue + ") = '\\seen')"
	}
	// Labels are updated immediately after a confirmed provider archive; stale
	// pre-move membership flags must not put that item back into Inbox.
	imapInbox := fmt.Sprintf("EXISTS (SELECT 1 FROM %smessage_labels mlink JOIN %slabels ml ON ml.id = mlink.label_id WHERE mlink.message_id = %s.id AND ml.source_id = %s.source_id AND UPPER(ml.name) = 'INBOX')", prefix, prefix, alias, alias)
	inbox = fmt.Sprintf("CASE WHEN NOT (%s) THEN 'unknown' WHEN %s = 'imap' AND (%s OR %s) THEN CASE WHEN %s THEN 'inbox' ELSE 'outside' END ELSE (%s) END", eligible, source, exists("TRUE"), imapInbox, imapInbox, inbox)
	read = fmt.Sprintf("CASE WHEN NOT (%s) THEN 'unknown' WHEN %s = 'imap' AND %s THEN CASE WHEN %s THEN 'unknown' WHEN %s THEN 'unread' ELSE 'read' END ELSE (%s) END", eligible, source, exists("TRUE"), exists("NOT COALESCE(("+valid+"), FALSE)"), exists("NOT ("+seen+")"), read)
	return inbox, read
}

type providerMailboxState struct {
	inbox string
	read  string
}

func (s *Store) providerMailboxStates(ctx context.Context, ids []int64) (map[int64]providerMailboxState, error) {
	states := make(map[int64]providerMailboxState, len(ids))
	if len(ids) == 0 {
		return states, nil
	}
	driver := "sqlite3"
	if s.IsPostgreSQL() {
		driver = "pgx"
	}
	inbox, read := MailboxStateExpressions("m", "", driver, true)
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, "SELECT m.id, "+inbox+", "+read+" FROM messages m WHERE m.id IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return nil, fmt.Errorf("read provider mailbox states: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var state providerMailboxState
		if err := rows.Scan(&id, &state.inbox, &state.read); err != nil {
			return nil, err
		}
		states[id] = state
	}
	return states, rows.Err()
}
