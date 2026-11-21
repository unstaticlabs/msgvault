package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/gmail"
	"go.kenn.io/msgvault/internal/store"
)

func TestGmailDraftGetAndArchivedTrashLabels(t *testing.T) {
	fixture := newGmailDraftTestFixture(t)
	draft := fixture.seedDraft(t)
	labels, err := fixture.store.EnsureLabelsBatch(fixture.source.ID, map[string]store.LabelInfo{
		"DRAFT": {Name: "DRAFT", Type: "system"},
		"TRASH": {Name: "TRASH", Type: "system"},
	})
	require.NoError(t, err)
	changed, err := fixture.store.ReconcileMessageLabels(draft.CurrentMessageID, []int64{labels["DRAFT"], labels["TRASH"]}, true)
	require.NoError(t, err)
	require.True(t, changed)
	fixture.client.getErr = &gmail.NotFoundError{Path: "/drafts/gmail-draft-managed"}

	var events []api.CLIRunEvent
	err = fixture.adapter.runCLIDraftLifecycle(t.Context(), api.CLIRunRequest{
		Args: []string{api.CLIRunDraftGetCommand, draft.DraftID, "--json"},
	}, func(event api.CLIRunEvent) error {
		events = append(events, event)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	var result gmailDraftLifecycleOutput
	require.NoError(t, json.Unmarshal([]byte(events[0].Data), &result))
	assert.Equal(t, "active", result.Lifecycle)
	assert.Equal(t, int64(1), result.Revision)
	assert.Equal(t, draft.CurrentMessageID, result.MessageID)
	require.NotNil(t, result.ProviderObservation)
	assert.Equal(t, "not_checked", result.ProviderObservation.State)

	router := api.NewServer(fixture.adapter.config, fixture.adapter, nil, fixture.adapter.logger).Router()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", result.MessageID), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	var archived api.MessageDetail
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &archived))
	assert.ElementsMatch(t, []string{"DRAFT", "TRASH"}, archived.Labels)

	_, err = fixture.lifecycle(t, api.CLIRunDraftRecoverCommand, draft, "")
	require.ErrorContains(t, err, "not_supported")
	assert.Zero(t, fixture.client.getCalls)

	_, err = fixture.lifecycle(t, api.CLIRunDraftEditCommand, draft, "replacement")
	require.ErrorContains(t, err, "provider_absent")
	assert.Equal(t, 1, fixture.client.getCalls)
	assert.Zero(t, fixture.client.updateCalls)
	assert.Zero(t, fixture.client.deleteCalls)
}
