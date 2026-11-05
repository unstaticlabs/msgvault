package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestSourceMaintenanceCLIThroughDaemon(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	st := testutil.NewTestStore(t)
	from, err := st.GetOrCreateSource("beeper", "history-example")
	require.NoError(err)
	into, err := st.GetOrCreateSource("beeper", "live-example")
	require.NoError(err)
	cfg := testConfigValue()
	srv := api.NewServer(cfg, &storeAPIAdapter{store: st, config: cfg, logger: testLoggerValue()}, nil, testLoggerValue())
	httpServer := httptest.NewServer(srv.Router())
	t.Cleanup(httpServer.Close)
	ctx := configureRemoteDaemonForTest(t, httpServer.URL)
	var output bytes.Buffer
	update := newUpdateAccountCmd()
	update.SetContext(ctx)
	update.SetOut(&output)
	update.SetArgs([]string{from.Identifier, "--alias", "historical-alias", "--history-only=true"})
	require.NoError(update.Execute())
	settings, err := st.GetSourceSettingsContext(t.Context(), from.ID)
	require.NoError(err)
	assert.Equal("historical-alias", settings.Alias)
	assert.True(settings.HistoryOnly)
	resume := newUpdateAccountCmd()
	resume.SetContext(ctx)
	resume.SetOut(&output)
	resume.SetArgs([]string{from.Identifier, "--history-only=false", "--accept-reanchor=false"})
	require.NoError(resume.Execute(), "explicit false history-only is an intentional update")
	settings, err = st.GetSourceSettingsContext(t.Context(), from.ID)
	require.NoError(err)
	assert.False(settings.HistoryOnly)
	assert.Equal("historical-alias", settings.Alias)
	merge := newMergeAccountCmd()
	merge.SetContext(ctx)
	merge.SetOut(&output)
	output.Reset()
	merge.SetArgs([]string{"--from", "historical-alias", "--into", into.Identifier, "--dry-run", "--json"})
	require.NoError(merge.Execute())
	var preview store.SourceMergeResult
	require.NoError(json.Unmarshal(output.Bytes(), &preview))
	assert.Equal(from.ID, preview.FromSourceID)
	assert.Equal(into.ID, preview.IntoSourceID)
	assert.True(preview.DryRun)
	settings, err = st.GetSourceSettingsContext(t.Context(), from.ID)
	require.NoError(err)
	assert.Zero(settings.MergedIntoSourceID)
}

func TestSourceMaintenanceCLIRejectsInvalidUpdatesBeforeDaemon(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  string
	}{
		{"false acceptance", []string{"--accept-reanchor=false"}, "nothing to update"},
		{"empty alias", []string{"--alias", ""}, "alias must be nonempty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			st := testutil.NewTestStore(t)
			source, err := st.GetOrCreateSource("beeper", "history-example")
			require.NoError(err)
			cfg := testConfigValue()
			srv := api.NewServer(cfg, &storeAPIAdapter{store: st, config: cfg, logger: testLoggerValue()}, nil, testLoggerValue())
			router := srv.Router()
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				router.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			ctx := configureRemoteDaemonForTest(t, server.URL)
			savedDisplayName, savedSourceID := updateDisplayName, updateAccountSourceID
			t.Cleanup(func() { updateDisplayName, updateAccountSourceID = savedDisplayName, savedSourceID })
			cmd := newUpdateAccountCmd()
			cmd.SetContext(ctx)
			cmd.SilenceUsage = true
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetArgs(append([]string{source.Identifier}, tc.flags...))
			require.ErrorContains(cmd.Execute(), tc.want)
			assert.False(cmd.SilenceUsage, "invalid flags must return a local usage error")
			assert.Zero(requests.Load(), "invalid flags must not contact the daemon")
		})
	}
}

func TestSourceMergeCLIReportsAccessChangesThroughNativeDaemon(t *testing.T) {
	assertions, requirements := assert.New(t), require.New(t)
	st := testutil.NewTestStore(t)
	from, err := st.GetOrCreateSource("beeper", "historical-fixture")
	requirements.NoError(err)
	into, err := st.GetOrCreateSource("beeper", "destination-fixture")
	requirements.NoError(err)
	conversation, err := st.EnsureConversation(from.ID, "fixture-thread", "Fixture history")
	requirements.NoError(err)
	messageID, err := st.UpsertMessage(&store.Message{SourceID: from.ID, ConversationID: conversation, SourceMessageID: "fixture-message", MessageType: "beeper"})
	requirements.NoError(err)
	users := []*store.User{}
	for index, sourceID := range []int64{from.ID, into.ID} {
		email := fmt.Sprintf("merge-reviewer-%d@example.test", index)
		user, err := st.RecordUserLogin(t.Context(), store.UserLogin{Issuer: "https://idp.example.test", Subject: email, Email: email, DisplayName: fmt.Sprintf("Fixture reviewer %d", index), Role: "member"})
		requirements.NoError(err)
		requirements.NoError(st.SetUserSources(t.Context(), user.ID, []int64{sourceID}))
		users = append(users, user)
	}
	cfg := testConfigValue()
	server := api.NewServer(cfg, &storeAPIAdapter{store: st, config: cfg, logger: testLoggerValue()}, nil, testLoggerValue())
	httpServer := httptest.NewServer(server.Router())
	t.Cleanup(httpServer.Close)
	t.Cleanup(func() { requirements.NoError(server.Shutdown(context.Background())) })
	ctx := configureRemoteDaemonForTest(t, httpServer.URL)
	run := func(flags ...string) string {
		var output bytes.Buffer
		command := newMergeAccountCmd()
		command.SetContext(ctx)
		command.SetOut(&output)
		command.SetArgs(append([]string{"--from", from.Identifier, "--into", into.Identifier}, flags...))
		requirements.NoError(command.Execute())
		return output.String()
	}
	var preview store.SourceMergeResult
	requirements.NoError(json.Unmarshal([]byte(run("--dry-run", "--json")), &preview))
	requirements.NotNil(preview.AccessImpact)
	requirements.Len(preview.AccessImpact.GainingUsers, 1)
	requirements.Len(preview.AccessImpact.LosingUsers, 1)
	assertions.Equal(users[1].ID, preview.AccessImpact.GainingUsers[0].UserID)
	assertions.Equal(users[1].DisplayName, preview.AccessImpact.GainingUsers[0].DisplayName, "the daemon client preserves complete preview context")
	assertions.Equal(users[0].ID, preview.AccessImpact.LosingUsers[0].UserID)
	human := run("--dry-run")
	assertions.Contains(human, "Gains source access to moved history: "+users[1].Email)
	assertions.Contains(human, "Loses source access to moved history: "+users[0].Email)
	assertions.Contains(human, "Access to existing destination history is unchanged")
	assertions.Contains(human, "Source assignments are not copied")
	assertions.Contains(human, "each credential's configured role still apply")
	var owner int64
	requirements.NoError(st.DB().QueryRowContext(t.Context(), st.Rebind(`SELECT source_id FROM messages WHERE id=?`), messageID).Scan(&owner))
	assertions.Equal(from.ID, owner, "previews leave history in place")
	var applied store.SourceMergeResult
	requirements.NoError(json.Unmarshal([]byte(run("--yes", "--json")), &applied))
	assertions.False(applied.DryRun)
	requirements.NotNil(applied.AccessImpact)
	assertions.Equal(preview.AccessImpact.GainingUsers, applied.AccessImpact.GainingUsers)
	assertions.Equal(preview.AccessImpact.LosingUsers, applied.AccessImpact.LosingUsers)
	requirements.NoError(st.DB().QueryRowContext(t.Context(), st.Rebind(`SELECT source_id FROM messages WHERE id=?`), messageID).Scan(&owner))
	assertions.Equal(into.ID, owner, "different user grants do not prevent native Admin execution")
	replay := run("--yes")
	assertions.Contains(replay, "Recorded source access impact")
	assertions.Contains(replay, "current assignments may have changed")
}
