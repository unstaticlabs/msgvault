package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestSourceMergeAccessPreviewMatchesNativeVisibilityAfterTransfer(t *testing.T) {
	assertions, requirements := assert.New(t), require.New(t)
	st := testutil.NewTestStore(t)
	from, err := st.GetOrCreateSource("beeper", "history-example")
	requirements.NoError(err)
	into, err := st.GetOrCreateSource("beeper", "live-example")
	requirements.NoError(err)
	messages := map[int64]int64{}
	for _, source := range []*store.Source{from, into} {
		conversation, err := st.EnsureConversation(source.ID, "fixture-thread", "Fixture history")
		requirements.NoError(err)
		id, err := st.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conversation, SourceMessageID: "fixture-message", MessageType: "beeper"})
		requirements.NoError(err)
		messages[source.ID] = id
	}
	type userCase struct {
		role                             string
		bindings                         []int64
		disabled                         bool
		user                             *store.User
		beforeHistory, beforeDestination int
	}
	cases := []userCase{
		{role: "viewer", bindings: []int64{from.ID}},
		{role: "member", bindings: []int64{into.ID}},
		{role: "member", bindings: []int64{from.ID, into.ID}},
		{role: "viewer", bindings: []int64{}},
		{role: "viewer", bindings: []int64{from.ID}, disabled: true},
		{role: "member", bindings: []int64{into.ID}, disabled: true},
		{role: "admin", bindings: []int64{}},
		{role: "admin", bindings: []int64{from.ID}},
	}
	cfg := &config.Config{Server: config.ServerConfig{APIKey: "synthetic-owner-key"}}
	for index := range cases {
		c := &cases[index]
		email := fmt.Sprintf("reviewer-%d@example.test", index)
		c.user, err = st.RecordUserLogin(t.Context(), store.UserLogin{Issuer: "https://idp.example.test", Subject: email, Email: email, DisplayName: fmt.Sprintf("Fixture reviewer %d", index), Role: c.role})
		requirements.NoError(err)
		requirements.NoError(st.SetUserSources(t.Context(), c.user.ID, c.bindings))
		if c.disabled {
			requirements.NoError(st.SetUserDisabled(t.Context(), c.user.ID, true))
		}
		cfg.Auth.APIKeys = append(cfg.Auth.APIKeys, config.APIKeyConfig{Name: email, Key: email, Role: c.role, User: email})
	}
	server := api.NewServerWithOptions(api.ServerOptions{Config: cfg, Store: st, UserStore: st, Engine: query.NewEngine(st.DB(), st.IsPostgreSQL()), Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() { requirements.NoError(server.Shutdown(context.Background())) })
	clientAddresses := map[string]string{}
	for index, c := range cases {
		clientAddresses[c.user.Email] = fmt.Sprintf("192.0.2.%d:40000", index+1)
	}
	get := func(email string, messageID int64) int {
		request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", messageID), nil)
		request.Header.Set("Authorization", "Bearer "+email)
		request.RemoteAddr = clientAddresses[email]
		response := httptest.NewRecorder()
		server.Router().ServeHTTP(response, request)
		return response.Code
	}
	for index := range cases {
		c := &cases[index]
		c.beforeHistory = get(c.user.Email, messages[from.ID])
		c.beforeDestination = get(c.user.Email, messages[into.ID])
		if c.role == "admin" {
			assertions.Equal(http.StatusOK, c.beforeHistory)
			assertions.Equal(http.StatusOK, c.beforeDestination)
		}
		if c.disabled {
			assertions.Equal(http.StatusUnauthorized, c.beforeHistory)
			assertions.Equal(http.StatusUnauthorized, c.beforeDestination)
		}
	}
	// The richer preview uses the existing Admin route; it is not a new
	// permission or a second approval step.
	for _, index := range []int{0, 1, 6} {
		c := cases[index]
		request := httptest.NewRequest(http.MethodPost, "/api/v1/cli/account/merge", strings.NewReader(fmt.Sprintf(`{"from_source_id":%d,"into_source_id":%d,"dry_run":true}`, from.ID, into.ID)))
		request.Header.Set("Authorization", "Bearer "+c.user.Email)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Router().ServeHTTP(response, request)
		if c.role != "admin" {
			assertions.Equal(http.StatusForbidden, response.Code, "the native merge route stays Administrator-only")
			assertions.NotContains(response.Body.String(), "reviewer-0@example.test", "non-admin callers do not receive the user access report")
		} else {
			requirements.Equal(http.StatusOK, response.Code, response.Body.String())
			var report store.SourceMergeResult
			requirements.NoError(json.Unmarshal(response.Body.Bytes(), &report))
			requirements.NotNil(report.AccessImpact)
			requirements.Len(report.AccessImpact.GainingUsers, 1)
			assertions.Equal(cases[1].user.ID, report.AccessImpact.GainingUsers[0].UserID)
		}
	}
	request := store.MergeSourcesRequest{FromSourceID: from.ID, IntoSourceID: into.ID, DryRun: true}
	preview, err := st.MergeSourcesContext(t.Context(), request)
	requirements.NoError(err)
	requirements.NotNil(preview.AccessImpact)
	assertions.Equal(int64(1), preview.MessagesMoved)
	assertions.False(preview.AccessImpact.AssessedAt.IsZero())
	assertions.True(preview.AccessImpact.ExistingDestinationAccessUnchanged)
	assertions.False(preview.AccessImpact.BindingsCopied)
	assertions.True(preview.AccessImpact.AdministratorsKeepAccess)
	assertions.True(preview.AccessImpact.DisabledUsersRemainDisabled)
	assertions.Contains(preview.AccessImpact.Basis, "each credential's configured role")
	for _, c := range cases {
		assertions.Equal(c.beforeHistory, get(c.user.Email, messages[from.ID]), "dry run changes no visibility")
	}
	request.DryRun = false
	applied, err := st.MergeSourcesContext(t.Context(), request)
	requirements.NoError(err, "differing source grants do not block the native merge")
	requirements.NotNil(applied.AccessImpact)
	actualGains, actualLosses := []store.SourceMergeAccessUser{}, []store.SourceMergeAccessUser{}
	for _, c := range cases {
		afterHistory := get(c.user.Email, messages[from.ID])
		assertions.Equal(c.beforeDestination, get(c.user.Email, messages[into.ID]), "existing destination access is unchanged for %s", c.user.Email)
		person := store.SourceMergeAccessUser{UserID: c.user.ID, Email: c.user.Email, DisplayName: c.user.DisplayName, Role: c.role}
		switch {
		case c.beforeHistory != http.StatusOK && afterHistory == http.StatusOK:
			actualGains = append(actualGains, person)
		case c.beforeHistory == http.StatusOK && afterHistory != http.StatusOK:
			actualLosses = append(actualLosses, person)
		}
		bindings, err := st.ListUserSourceIDs(t.Context(), c.user.ID)
		requirements.NoError(err)
		assertions.ElementsMatch(c.bindings, bindings, "merge does not copy or rewrite user source assignments")
		user, err := st.GetUser(t.Context(), c.user.ID)
		requirements.NoError(err)
		assertions.Equal(c.disabled, user.Disabled)
	}
	requirements.Len(actualGains, 1)
	requirements.Len(actualLosses, 1)
	assertions.Equal(actualGains, preview.AccessImpact.GainingUsers)
	assertions.Equal(actualLosses, preview.AccessImpact.LosingUsers)
	assertions.Equal(actualGains, applied.AccessImpact.GainingUsers)
	assertions.Equal(actualLosses, applied.AccessImpact.LosingUsers)
	var movedSource int64
	requirements.NoError(st.DB().QueryRowContext(t.Context(), st.Rebind(`SELECT source_id FROM messages WHERE id=?`), messages[from.ID]).Scan(&movedSource))
	assertions.Equal(into.ID, movedSource, "the native transfer really changed ownership")
	requirements.NoError(st.SetUserSources(t.Context(), cases[1].user.ID, []int64{from.ID}))
	replay, err := st.MergeSourcesContext(t.Context(), request)
	requirements.NoError(err)
	assertions.True(replay.AlreadyMerged)
	assertions.Equal(applied.AccessImpact, replay.AccessImpact, "journal retries retain the assessment made at the actual merge")
}

func TestSourceMergeAccessEmptyHistoryDoesNotClaimGainsOrLosses(t *testing.T) {
	assertions, requirements := assert.New(t), require.New(t)
	st := testutil.NewTestStore(t)
	from, err := st.GetOrCreateSource("beeper", "empty-history")
	requirements.NoError(err)
	into, err := st.GetOrCreateSource("beeper", "live-history")
	requirements.NoError(err)
	user, err := st.RecordUserLogin(t.Context(), store.UserLogin{Issuer: "https://idp.example.test", Subject: "viewer", Email: "viewer@example.test", Role: "viewer"})
	requirements.NoError(err)
	requirements.NoError(st.SetUserSources(t.Context(), user.ID, []int64{into.ID}))
	result, err := st.MergeSourcesContext(t.Context(), store.MergeSourcesRequest{FromSourceID: from.ID, IntoSourceID: into.ID, DryRun: true})
	requirements.NoError(err)
	requirements.NotNil(result.AccessImpact)
	assertions.Empty(result.AccessImpact.GainingUsers)
	assertions.Empty(result.AccessImpact.LosingUsers)
}
