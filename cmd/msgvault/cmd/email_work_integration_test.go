package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

// Native deletion and allocation reuse the physical ID, but an old member
// link must never expose the replacement message through either detail path.
func TestEmailWorkMemberBodyRejectsReusedNativeID(t *testing.T) {
	for _, withEngine := range []bool{false, true} {
		t.Run(strconv.FormatBool(withEngine), func(t *testing.T) {
			requirements := require.New(t)
			assertions := assert.New(t)
			st, err := store.Open(filepath.Join(t.TempDir(), "synthetic.db"))
			requirements.NoError(err)
			t.Cleanup(func() { requirements.NoError(st.Close()) })
			requirements.NoError(st.InitSchema())
			source, err := st.GetOrCreateSource("gmail", "qa-copy@example.test")
			requirements.NoError(err)
			conversation, err := st.EnsureConversation(source.ID, "qa-thread", "[QA]")
			requirements.NoError(err)
			message := &store.Message{SourceID: source.ID, ConversationID: conversation, SourceMessageID: "original", MessageType: "email", Subject: sql.NullString{String: "[QA] Original", Valid: true}}
			id, err := st.UpsertMessage(message)
			requirements.NoError(err)
			seed, err := st.EmailWorkSeedForMessage(context.Background(), id)
			requirements.NoError(err)
			var engine query.Engine
			if withEngine {
				engine = query.NewEngine(st.DB(), st.IsPostgreSQL())
			}
			daemon := api.NewServerWithOptions(api.ServerOptions{Config: &config.Config{Server: config.ServerConfig{APIKey: "synthetic-work-key"}}, Store: &storeAPIAdapter{store: st}, Engine: engine, Logger: slog.New(slog.DiscardHandler)})
			t.Cleanup(func() { requirements.NoError(daemon.Shutdown(context.Background())) })
			fetch := func(uid string) *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d?expected_message_uid=%s", id, uid), nil)
				request.Header.Set("X-Api-Key", "synthetic-work-key")
				response := httptest.NewRecorder()
				daemon.Router().ServeHTTP(response, request)
				return response
			}
			first := fetch(seed.UID)
			requirements.Equal(http.StatusOK, first.Code, first.Body.String())
			assertions.Contains(first.Body.String(), "[QA] Original")
			_, err = st.DB().Exec("DELETE FROM messages WHERE id=?", id)
			requirements.NoError(err)
			message.SourceMessageID = "replacement"
			message.Subject.String = "[QA] Unrelated replacement"
			replacement, err := st.UpsertMessage(message)
			requirements.NoError(err)
			requirements.Equal(id, replacement, "real SQLite maximum-ID reuse")
			current, err := st.EmailWorkSeedForMessage(context.Background(), id)
			requirements.NoError(err)
			requirements.NotEqual(seed.UID, current.UID)
			old := fetch(seed.UID)
			assertions.Equal(http.StatusConflict, old.Code, old.Body.String())
			assertions.NotContains(old.Body.String(), message.Subject.String)
			fresh := fetch(current.UID)
			assertions.Equal(http.StatusOK, fresh.Code, fresh.Body.String())
			assertions.Contains(fresh.Body.String(), message.Subject.String)
		})
	}
}
