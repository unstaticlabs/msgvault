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
	require := require.New(t)
	assert := assert.New(t)
	fixture := newGmailDraftTestFixture(t)
	draft := fixture.seedDraft(t)
	labels, err := fixture.store.EnsureLabelsBatch(fixture.source.ID, map[string]store.LabelInfo{
		"DRAFT": {Name: "DRAFT", Type: "system"},
		"TRASH": {Name: "TRASH", Type: "system"},
	})
	require.NoError(err)
	changed, err := fixture.store.ReconcileMessageLabels(draft.CurrentMessageID, []int64{labels["DRAFT"], labels["TRASH"]}, true)
	require.NoError(err)
	require.True(changed)
	fixture.client.getErr = &gmail.NotFoundError{Path: "/drafts/gmail-draft-managed"}

	var events []api.CLIRunEvent
	err = fixture.adapter.runCLIDraftLifecycle(t.Context(), api.CLIRunRequest{
		Args: []string{api.CLIRunDraftGetCommand, draft.DraftID, "--json"},
	}, func(event api.CLIRunEvent) error {
		events = append(events, event)
		return nil
	})
	require.NoError(err)
	require.Len(events, 1)
	var result gmailDraftLifecycleOutput
	require.NoError(json.Unmarshal([]byte(events[0].Data), &result))
	assert.Equal("active", result.Lifecycle)
	assert.Equal(int64(1), result.Revision)
	assert.Equal(draft.CurrentMessageID, result.MessageID)
	require.NotNil(result.ProviderObservation)
	assert.Equal("not_checked", result.ProviderObservation.State)

	router := api.NewServer(fixture.adapter.config, fixture.adapter, nil, fixture.adapter.logger).Router()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", result.MessageID), nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(http.StatusOK, response.Code)
	var archived api.MessageDetail
	require.NoError(json.Unmarshal(response.Body.Bytes(), &archived))
	assert.ElementsMatch([]string{"DRAFT", "TRASH"}, archived.Labels)

	_, err = fixture.lifecycle(t, api.CLIRunDraftRecoverCommand, draft, "")
	require.ErrorContains(err, "not_supported")
	assert.Zero(fixture.client.getCalls)

	_, err = fixture.lifecycle(t, api.CLIRunDraftEditCommand, draft, "replacement")
	require.ErrorContains(err, "provider_absent")
	assert.Equal(1, fixture.client.getCalls)
	assert.Zero(fixture.client.updateCalls)
	assert.Zero(fixture.client.deleteCalls)
}
