package daemonclient

import (
	"encoding/json"
	"go.kenn.io/msgvault/pkg/client/generated"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIDeleteDedupedExecuteBodyKeepsEmptyExpectedBatches(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	expectedTotal := int64(0)
	expectedBatchCount := int64(0)
	body := cliDeleteDedupedExecuteBodyFromRequest(CLIDeleteDedupedRequest{
		BatchIDs:           []string{"batch-a"},
		ExpectedTotal:      &expectedTotal,
		ExpectedBatchCount: &expectedBatchCount,
		ExpectedBatches:    []CLIDeleteDedupedBatch{},
	})

	data, err := json.Marshal(body)
	require.NoError(err, "marshal execute body")

	var decoded map[string]any
	require.NoError(json.Unmarshal(data, &decoded), "decode execute body")
	expectedBatches, ok := decoded["expected_batches"].([]any)
	require.True(ok, "expected_batches should be present as an array: %s", string(data))
	assert.Empty(expectedBatches, "expected_batches")
}

func TestCLIMessageNilAndMissingProviderStateStayUnknown(t *testing.T) {
	assert.Nil(t, cliMessageDetailFromGenerated(nil))
	message := cliMessageDetailFromGenerated(&generated.GetCLIMessageResponse{ID: 1})
	require.NotNil(t, message)
	assert.Equal(t, "unknown", message.InboxState)
	assert.Equal(t, "unknown", message.ReadState)
}
