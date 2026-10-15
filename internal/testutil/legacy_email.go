package testutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
)

// NewLegacyNullableEmailStore models an existing archive's historical nullable
// message type. PostgreSQL keeps its normal isolated test schema; SQLite
// reopens the actual file after rewriting only this legacy column constraint.
func NewLegacyNullableEmailStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	r := require.New(t)
	if os.Getenv("MSGVAULT_TEST_DB") != "" {
		st := NewTestStore(t)
		_, err := st.DB().Exec(`ALTER TABLE messages ALTER COLUMN message_type DROP NOT NULL`)
		r.NoError(err)
		return st, ""
	}
	return NewLegacyNullableSQLiteEmailStore(t)
}

// NewLegacyNullableSQLiteEmailStore explicitly models the supported SQLite cache input.
func NewLegacyNullableSQLiteEmailStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	r := require.New(t)
	path := filepath.Join(t.TempDir(), "legacy-email.db")
	st, err := store.OpenForTest(path)
	r.NoError(err)
	r.NoError(st.InitSchema())
	conn, err := st.DB().Conn(context.Background())
	r.NoError(err)
	var schema string
	r.NoError(conn.QueryRowContext(t.Context(), `SELECT sql FROM sqlite_master WHERE type='table' AND name='messages'`).Scan(&schema))
	rewritten := strings.Replace(schema, "message_type TEXT NOT NULL", "message_type TEXT", 1)
	r.NotEqual(schema, rewritten)
	_, err = conn.ExecContext(t.Context(), `PRAGMA writable_schema=ON`)
	r.NoError(err)
	_, err = conn.ExecContext(t.Context(), `UPDATE sqlite_master SET sql=? WHERE type='table' AND name='messages'`, rewritten)
	r.NoError(err)
	_, err = conn.ExecContext(t.Context(), `PRAGMA writable_schema=OFF`)
	r.NoError(err)
	r.NoError(conn.Close())
	r.NoError(st.Close())
	st, err = store.OpenForTest(path)
	r.NoError(err)
	r.NoError(st.InitSchema())
	t.Cleanup(func() { r.NoError(st.Close()) })
	return st, path
}
