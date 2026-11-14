package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/deletion"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/query/querytest"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

// memberWriteStore adds per-ID manifests and source lookup to the deletion
// fake, so manifest routes can be checked against two users' sources.
type memberWriteStore struct {
	deletionMockStore

	byID    map[string]*deletion.Manifest
	sources map[int64]*store.Source
}

func (s *memberWriteStore) GetDeletionManifest(_ context.Context, id string) (*deletion.Manifest, deletion.Status, error) {
	manifest, ok := s.byID[id]
	if !ok {
		return nil, "", deletion.ErrManifestNotFound
	}
	return manifest, manifest.Status, nil
}

func (s *memberWriteStore) GetSourceByIDContext(_ context.Context, id int64) (*store.Source, error) {
	if src, ok := s.sources[id]; ok {
		return src, nil
	}
	return nil, store.ErrSourceNotFound
}

// memberWriteUsers holds two sources: alice is a member bound to "own", and
// "other" belongs to someone else.
type memberWriteUsers struct {
	users *store.Store
	own   *store.Source
	other *store.Source
}

func newMemberWriteUsers(t *testing.T) memberWriteUsers {
	t.Helper()
	require := require.New(t)
	users := testutil.NewTestStore(t)
	own, err := users.GetOrCreateSource("gmail", "alice@example.com")
	require.NoError(err)
	other, err := users.GetOrCreateSource("gmail", "bob@example.com")
	require.NoError(err)
	alice, err := users.RecordUserLogin(t.Context(), store.UserLogin{
		Issuer: "https://idp.example", Subject: "alice", Email: "alice@example.com", DisplayName: "Alice", Role: "member",
	})
	require.NoError(err)
	require.NoError(users.SetUserSources(t.Context(), alice.ID, []int64{own.ID}))
	return memberWriteUsers{users: users, own: own, other: other}
}

func memberWriteConfig(enabled bool) *config.Config {
	return &config.Config{
		Server:       config.ServerConfig{APIKey: "admin-secret-value"},
		InboxArchive: config.InboxArchiveConfig{RemoteEnabled: true},
		Auth: config.AuthConfig{
			MemberSourceWrites: enabled,
			APIKeys: []config.APIKeyConfig{
				{Name: "assistant", Key: "assistant-secret-value", Role: "member", User: "alice@example.com"},
				{Name: "reader", Key: "reader-secret-value", Role: "viewer", User: "alice@example.com"},
			},
		},
	}
}

func (u memberWriteUsers) target(messageID int64, src *store.Source, gmailID string) query.DeletionTarget {
	return query.DeletionTarget{
		MessageID: messageID, SourceID: src.ID, SourceType: src.SourceType,
		SourceIdentifier: src.Identifier, SourceMessageID: gmailID,
	}
}

func (u memberWriteUsers) deletionServer(t *testing.T, enabled bool, st *memberWriteStore) *Server {
	t.Helper()
	targets := map[int64]query.DeletionTarget{
		11: u.target(11, u.own, "gm-own"),
		22: u.target(22, u.other, "gm-other"),
	}
	// The daemon's engines resolve message IDs through the text-capable
	// scoped wrapper; the text double gives the mock that shape.
	engine := &participantFilterTextEngine{MockEngine: &querytest.MockEngine{
		GetDeletionTargetsByMessageIDsFunc: func(_ context.Context, ids []int64) ([]query.DeletionTarget, error) {
			var out []query.DeletionTarget
			for _, id := range ids {
				if target, ok := targets[id]; ok {
					out = append(out, target)
				}
			}
			return out, nil
		},
	}}
	if st.sources == nil {
		st.sources = map[int64]*store.Source{u.own.ID: u.own, u.other.ID: u.other}
	}
	srv := NewServerWithOptions(ServerOptions{
		Config: memberWriteConfig(enabled), Store: st, Engine: engine, UserStore: u.users, Logger: testLogger(),
	})
	t.Cleanup(func() { require.NoError(t, srv.Shutdown(context.Background())) })
	return srv
}

func TestMemberSourceWritesAreAdministratorOnlyByDefault(t *testing.T) {
	assert := assert.New(t)
	u := newMemberWriteUsers(t)
	st := &memberWriteStore{}
	srv := u.deletionServer(t, false, st)

	w := performSessionRequest(t, srv, http.MethodPost, "/api/v1/deletions",
		[]byte(`{"message_ids":[11]}`), withKey("assistant-secret-value"), false)
	assert.Equal(http.StatusForbidden, w.Code, w.Body.String())
	assert.Empty(st.saved)
}

func TestMemberSourceWritesStageOnlyOwnSources(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	u := newMemberWriteUsers(t)
	st := &memberWriteStore{}
	srv := u.deletionServer(t, true, st)
	stage := func(key, body string) int {
		return performSessionRequest(t, srv, http.MethodPost, "/api/v1/deletions", []byte(body), withKey(key), false).Code
	}

	assert.Equal(http.StatusCreated, stage("assistant-secret-value", `{"message_ids":[11]}`))
	require.Len(st.saved, 1)
	require.NotNil(st.saved[0].Source)
	assert.Equal(u.own.ID, st.saved[0].Source.ID)
	assert.Equal([]string{"gm-own"}, st.saved[0].GmailIDs)

	assert.NotEqual(http.StatusCreated, stage("assistant-secret-value", `{"message_ids":[22]}`),
		"a member cannot stage another user's message")
	assert.Equal(http.StatusCreated, stage("assistant-secret-value", `{"message_ids":[11,22]}`))
	require.Len(st.saved, 2)
	assert.Equal([]string{"gm-own"}, st.saved[1].GmailIDs,
		"another user's message is not found, so a mixed request stages only the member's own")
	assert.Equal(http.StatusForbidden, stage("reader-secret-value", `{"message_ids":[11]}`),
		"viewers still cannot stage deletions")

	assert.Equal(http.StatusCreated, stage("admin-secret-value", `{"message_ids":[22]}`), "administrators keep every source")
	require.Len(st.saved, 3)
	assert.Equal(u.other.ID, st.saved[2].Source.ID)
}

func TestMemberSourceWritesConfineClientBuiltManifests(t *testing.T) {
	u := newMemberWriteUsers(t)
	manifest := func(src *deletion.SourceReference, account string) []byte {
		version := 2
		if src == nil {
			version = 1 // version 2 requires a source; a legacy manifest has none
		}
		body, err := json.Marshal(deletion.Manifest{
			Version: version, ID: "20260101-000000-test", GmailIDs: []string{"gm-1"},
			Filters: deletion.Filters{Account: account}, Source: src,
		})
		require.NoError(t, err)
		return body
	}
	own := &deletion.SourceReference{ID: u.own.ID, Type: "gmail", Identifier: u.own.Identifier}
	other := &deletion.SourceReference{ID: u.other.ID, Type: "gmail", Identifier: u.other.Identifier}
	forged := &deletion.SourceReference{ID: u.own.ID, Type: "gmail", Identifier: u.other.Identifier}

	tests := []struct {
		name string
		key  string
		body []byte
		want int
	}{
		{"own source", "assistant-secret-value", manifest(own, ""), http.StatusOK},
		{"own source with its account", "assistant-secret-value", manifest(own, u.own.Identifier), http.StatusOK},
		{"another user's source", "assistant-secret-value", manifest(other, ""), http.StatusNotFound},
		{"own source ID with another identifier", "assistant-secret-value", manifest(forged, ""), http.StatusBadRequest},
		{"own source with another account", "assistant-secret-value", manifest(own, u.other.Identifier), http.StatusBadRequest},
		{"no source", "assistant-secret-value", manifest(nil, u.own.Identifier), http.StatusBadRequest},
		{"administrator without a source", "admin-secret-value", manifest(nil, u.other.Identifier), http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			st := &memberWriteStore{}
			srv := u.deletionServer(t, true, st)
			w := performSessionRequest(t, srv, http.MethodPost, "/api/v1/cli/deletion-manifests", tc.body, withKey(tc.key), false)
			require.Equal(tc.want, w.Code, w.Body.String())
			if tc.want != http.StatusOK {
				assert.Empty(st.saved)
				return
			}
			require.Len(st.saved, 1)
			if tc.key == "assistant-secret-value" {
				assert.Equal(u.own.Identifier, st.saved[0].Filters.Account, "execution targets the member's own account")
			}
		})
	}
}

func TestMemberSourceWritesConfineManifestReadsAndCancels(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	u := newMemberWriteUsers(t)
	ownManifest := &deletion.Manifest{ID: "own-batch", Status: deletion.StatusPending,
		Source: &deletion.SourceReference{ID: u.own.ID, Type: "gmail", Identifier: u.own.Identifier}}
	otherManifest := &deletion.Manifest{ID: "other-batch", Status: deletion.StatusPending,
		Source: &deletion.SourceReference{ID: u.other.ID, Type: "gmail", Identifier: u.other.Identifier}}
	legacyManifest := &deletion.Manifest{ID: "legacy-batch", Status: deletion.StatusPending}
	st := &memberWriteStore{
		byID: map[string]*deletion.Manifest{"own-batch": ownManifest, "other-batch": otherManifest, "legacy-batch": legacyManifest},
	}
	st.manifests = map[deletion.Status][]*deletion.Manifest{deletion.StatusPending: {ownManifest, otherManifest, legacyManifest}}
	srv := u.deletionServer(t, true, st)
	member := withKey("assistant-secret-value")

	w := performSessionRequest(t, srv, http.MethodGet, "/api/v1/deletions", nil, member, false)
	require.Equal(http.StatusOK, w.Code, w.Body.String())
	var list ListDeletionsResponse
	require.NoError(json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(list.Manifests, 1)
	assert.Equal("own-batch", list.Manifests[0].ID)

	for _, id := range []string{"other-batch", "legacy-batch"} {
		assert.Equal(http.StatusNotFound, performSessionRequest(t, srv, http.MethodGet, "/api/v1/deletions/"+id, nil, member, false).Code, id)
		assert.Equal(http.StatusNotFound, performSessionRequest(t, srv, http.MethodDelete, "/api/v1/deletions/"+id, nil, member, false).Code, id)
	}
	assert.Empty(st.cancelled, "a member cannot cancel another user's batch")

	assert.Equal(http.StatusOK, performSessionRequest(t, srv, http.MethodGet, "/api/v1/deletions/own-batch", nil, member, false).Code)
	assert.Equal(http.StatusOK, performSessionRequest(t, srv, http.MethodDelete, "/api/v1/deletions/own-batch", nil, member, false).Code)
	assert.Equal([]string{"own-batch"}, st.cancelled)

	admin := withKey("admin-secret-value")
	assert.Equal(http.StatusOK, performSessionRequest(t, srv, http.MethodDelete, "/api/v1/deletions/other-batch", nil, admin, false).Code)
	assert.Equal([]string{"own-batch", "other-batch"}, st.cancelled)
}

func TestMemberSourceWritesConfineInboxArchive(t *testing.T) {
	u := newMemberWriteUsers(t)
	newServer := func(t *testing.T, enabled bool, runner InboxArchiveRunner) *Server {
		t.Helper()
		srv := NewServerWithOptions(ServerOptions{
			Config:        memberWriteConfig(enabled),
			Store:         &operationArchiveStore{mockStore: &mockStore{}, uid: operationTestArchiveUID},
			OperationGate: NewSerialOperationGate(),
			InboxArchive:  runner,
			UserStore:     u.users,
			Logger:        testLogger(),
		})
		t.Cleanup(func() { require.NoError(t, srv.Shutdown(context.Background())) })
		return srv
	}
	post := func(t *testing.T, srv *Server, key, path string, body any) (int, []byte) {
		t.Helper()
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		w := performSessionRequest(t, srv, http.MethodPost, path, payload, withKey(key), false)
		return w.Code, w.Body.Bytes()
	}
	authorize := func(t *testing.T, srv *Server, key string, sourceID int64) (int, string) {
		t.Helper()
		code, body := post(t, srv, key, "/api/v1/inbox-archive/authorize", InboxArchiveAuthorizeRequest{
			SourceID: sourceID, SourceMessageIDs: []string{"m-1"},
		})
		var resp InboxArchiveAuthorizeResponse
		if code == http.StatusOK {
			require.NoError(t, json.Unmarshal(body, &resp))
		}
		return code, resp.ConfirmationToken
	}
	execute := func(t *testing.T, srv *Server, key, token string) int {
		t.Helper()
		code, _ := post(t, srv, key, "/api/v1/inbox-archive/execute", InboxArchiveExecuteRequest{ConfirmationToken: token})
		return code
	}

	t.Run("off by default", func(t *testing.T) {
		runner := &stubInboxArchiveRunner{}
		code, _ := authorize(t, newServer(t, false, runner), "assistant-secret-value", u.own.ID)
		assert.Equal(t, http.StatusForbidden, code)
	})

	t.Run("own source", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		runner := &stubInboxArchiveRunner{result: InboxArchiveRunResult{Archived: 1}}
		srv := newServer(t, true, runner)
		code, token := authorize(t, srv, "assistant-secret-value", u.own.ID)
		require.Equal(http.StatusOK, code)
		require.Equal(http.StatusOK, execute(t, srv, "assistant-secret-value", token))
		require.Len(runner.calls, 1)
		assert.Equal(u.own.ID, runner.calls[0].SourceID)
	})

	t.Run("another user's source", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		runner := &stubInboxArchiveRunner{}
		srv := newServer(t, true, runner)
		code, _ := authorize(t, srv, "assistant-secret-value", u.other.ID)
		assert.Equal(http.StatusNotFound, code)

		code, token := authorize(t, srv, "admin-secret-value", u.other.ID)
		require.Equal(http.StatusOK, code)
		assert.Equal(http.StatusNotFound, execute(t, srv, "assistant-secret-value", token),
			"a member cannot spend a token minted for another user's source")
		assert.Empty(runner.calls)
	})

	t.Run("viewer", func(t *testing.T) {
		code, _ := authorize(t, newServer(t, true, &stubInboxArchiveRunner{}), "reader-secret-value", u.own.ID)
		assert.Equal(t, http.StatusForbidden, code)
	})
}
