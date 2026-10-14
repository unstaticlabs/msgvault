package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/daemon"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/authz"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/daemonclient"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
)

func TestPrepareEmailWorkCLIUsesNativeResumableDaemonAndSourceAuthority(t *testing.T) {
	requirements := require.New(t)
	assertions := assert.New(t)
	ctx := context.Background()
	home := t.TempDir()
	dbPath := filepath.Join(home, "msgvault.db")
	cache := filepath.Join(home, "analytics")
	st, err := store.Open(dbPath)
	requirements.NoError(err)
	t.Cleanup(func() { requirements.NoError(st.Close()) })
	requirements.NoError(st.InitSchema())
	source, err := st.GetOrCreateSource("gmail", "qa-legacy@example.test")
	requirements.NoError(err)
	excluded, err := st.GetOrCreateSource("gmail", "qa-excluded@example.test")
	requirements.NoError(err)
	for i := range 3 {
		conversation, err := st.EnsureConversation(source.ID, fmt.Sprintf("legacy-%d", i), "[QA]")
		requirements.NoError(err)
		raw := []byte(fmt.Sprintf("Message-ID: <legacy-%d@example.test>\r\n\r\n[QA]", i))
		if i == 1 {
			raw = []byte("malformed archived headers\r\n\r\n[QA]")
		}
		_, err = st.PersistMessage(&store.MessagePersistData{Message: &store.Message{SourceID: source.ID, ConversationID: conversation, SourceMessageID: fmt.Sprintf("legacy-%d", i), MessageType: "email", SentAt: sql.NullTime{Time: time.Date(2026, 1, 1, 12, i, 0, 0, time.UTC), Valid: true}, Subject: sql.NullString{String: "[QA] Legacy", Valid: true}}, RawMIME: raw})
		requirements.NoError(err)
	}
	// Recreate the pre-feature archive boundary: mail/raw exists, derived facts
	// and opaque work seeds have never been allocated. No viewer writes them.
	_, err = st.DB().Exec("DELETE FROM email_linkage_facts")
	requirements.NoError(err)
	_, err = st.DB().Exec("DELETE FROM email_work_seeds")
	requirements.NoError(err)
	user, err := st.RecordUserLogin(ctx, store.UserLogin{Issuer: "https://synthetic-idp.example.test", Subject: "preparation", Email: "qa-prepare@example.test", Role: string(authz.RoleMember)})
	requirements.NoError(err)
	requirements.NoError(st.SetUserSources(ctx, user.ID, []int64{source.ID}))
	configuration := &config.Config{Server: config.ServerConfig{APIKey: "synthetic-admin"}, Auth: config.AuthConfig{APIKeys: []config.APIKeyConfig{{Name: "member", Key: "synthetic-prepare-member", Role: "member", User: "qa-prepare@example.test"}, {Name: "viewer", Key: "synthetic-prepare-viewer", Role: "viewer", User: "qa-prepare@example.test"}}}}
	native := &emailWorkHTTPNativeStore{storeAPIAdapter: &storeAPIAdapter{store: st, analyticsDir: cache}, dbPath: dbPath, cache: cache}
	server := api.NewServerWithOptions(api.ServerOptions{Config: configuration, Store: native, UserStore: st, Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() { requirements.NoError(server.Shutdown(ctx)) })
	mux := http.NewServeMux()
	mux.Handle("/api/ping", daemon.NewPingHandler(daemon.PingHandlerOptions{Service: daemonService, Version: Version}))
	mux.Handle("/", server.Router())
	endpoint := httptest.NewServer(mux)
	t.Cleanup(endpoint.Close)
	writeStatsHTTPDaemonRuntime(t, home, endpoint)
	savedCfg, savedLocal := cfg, useLocal
	t.Cleanup(func() { cfg = savedCfg; useLocal = savedLocal })
	cfg = &config.Config{HomeDir: home, Data: config.DataConfig{DataDir: home}, Remote: config.RemoteConfig{URL: endpoint.URL, APIKey: "synthetic-prepare-member", AllowInsecure: true}}
	useLocal = false
	run := func(arguments ...string) (*bytes.Buffer, error) {
		output := &bytes.Buffer{}
		root := newTestRootCmd()
		root.SetOut(output)
		root.SetErr(io.Discard)
		root.AddCommand(newPrepareEmailWorkCommand())
		root.SetArgs(append([]string{"prepare-email-work"}, arguments...))
		return output, root.Execute()
	}
	_, err = run(strconv.FormatInt(excluded.ID, 10))
	requirements.Error(err, "member cannot prepare excluded source")
	cfg.Remote.APIKey = "synthetic-prepare-viewer"
	_, err = run(strconv.FormatInt(source.ID, 10))
	requirements.Error(err, "viewer cannot allocate derived identities")
	var count int
	requirements.NoError(st.DB().QueryRow("SELECT COUNT(*) FROM email_work_seeds").Scan(&count))
	assertions.Zero(count)
	cfg.Remote.APIKey = "synthetic-prepare-member"
	output, err := run(strconv.FormatInt(source.ID, 10), "--batch-size", "2", "--all")
	requirements.NoError(err, output.String())
	decoder := json.NewDecoder(output)
	var first, last daemonclient.EmailWorkPrepareResult
	requirements.NoError(decoder.Decode(&first))
	requirements.NoError(decoder.Decode(&last))
	assertions.False(first.Progress.Done)
	assertions.Equal(2, first.Progress.Processed)
	assertions.Equal("stale", first.CacheState)
	assertions.True(last.Progress.Done)
	assertions.Equal(1, last.Progress.Processed)
	assertions.Equal("ready", last.CacheState)
	requirements.NoError(st.DB().QueryRow("SELECT COUNT(*) FROM email_work_seeds").Scan(&count))
	assertions.Equal(3, count)
	requirements.NoError(st.DB().QueryRow("SELECT COUNT(*) FROM email_linkage_facts").Scan(&count))
	assertions.Equal(3, count, "corrupt raw cannot strand subsequent mail")
	revision, err := st.DerivedDataRevision()
	requirements.NoError(err)
	repeated, err := run(strconv.FormatInt(source.ID, 10), "--all")
	requirements.NoError(err)
	var again daemonclient.EmailWorkPrepareResult
	requirements.NoError(json.NewDecoder(repeated).Decode(&again))
	assertions.Zero(again.Progress.Processed)
	assertions.Zero(again.Progress.Changed)
	unchanged, err := st.DerivedDataRevision()
	requirements.NoError(err)
	assertions.Equal(revision, unchanged)
	engine, err := query.NewDuckDBEngine(cache, dbPath, st.DB())
	requirements.NoError(err)
	t.Cleanup(func() { requirements.NoError(engine.Close()) })
	rows, err := engine.Explore(ctx, query.ExploreRequest{Context: query.Context{RowUnit: query.RowUnitEmailWork, WorkAuthorityKey: "synthetic-preparation-read", SourceIDs: []int64{source.ID}}})
	requirements.NoError(err)
	assertions.Equal(int64(3), rows.TotalCount, "prepared legacy rows participate in the native work projection")
}
