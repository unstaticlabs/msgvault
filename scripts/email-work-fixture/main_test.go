package main

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/store"
)

func TestSeedCreatesOnlyNewSyntheticNativeArchive(t *testing.T) {
	requirements := require.New(t)
	destination := filepath.Join(t.TempDir(), "qa-email-work")
	requirements.NoError(seed(destination))
	original, err := os.ReadFile(filepath.Join(destination, "msgvault.db"))
	requirements.NoError(err)
	requirements.Error(seed(destination), "existing archives must never be opened or overwritten")
	unchanged, err := os.ReadFile(filepath.Join(destination, "msgvault.db"))
	requirements.NoError(err)
	requirements.Equal(sha256.Sum256(original), sha256.Sum256(unchanged))
	cfg, err := config.Load("", destination)
	requirements.NoError(err)
	requirements.True(cfg.Server.IsLoopback())
	requirements.Len(cfg.Auth.APIKeys, 2)
	for _, key := range cfg.Auth.APIKeys {
		requirements.NotEmpty(key.User, "named keys use actual source grants")
	}
	st, err := store.Open(filepath.Join(destination, "msgvault.db"))
	requirements.NoError(err)
	t.Cleanup(func() { requirements.NoError(st.Close()) })
	for table, count := range map[string]int{"sources": 3, "messages": 10, "message_raw": 9, "email_work_seeds": 10, "messages_fts": 10} {
		var actual int
		requirements.NoError(st.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&actual))
		requirements.Equal(count, actual, table)
	}
	var searchable int
	requirements.NoError(st.DB().QueryRow(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH '"Arrange site access"'`).Scan(&searchable))
	requirements.Equal(3, searchable, "candidate picker must see all three real subject matches")
	body, err := os.ReadFile(filepath.Join(destination, "qa-work-items.json"))
	requirements.NoError(err)
	var aliases map[string]string
	requirements.NoError(json.Unmarshal(body, &aliases))
	requirements.Len(aliases, 10)
	requirements.NotEqual(aliases["request"], aliases["copy"], "distinct source copies retain immutable provenance")
	for _, uid := range aliases {
		requirements.Len(uid, 32)
	}
}
