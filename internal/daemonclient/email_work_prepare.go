package daemonclient

import (
	"context"
	"errors"

	"go.kenn.io/msgvault/internal/store"
	apiclient "go.kenn.io/msgvault/pkg/client"
	"go.kenn.io/msgvault/pkg/client/generated"
)

type EmailWorkPrepareResult struct {
	Progress   *store.EmailLinkageBackfill `json:"progress"`
	CacheState string                      `json:"cache_state"`
}

func (c *Client) PrepareEmailWork(ctx context.Context, sourceID int64, batchSize int) (*EmailWorkPrepareResult, error) {
	request := generated.EmailWorkPrepareRequest{SourceID: sourceID, BatchSize: int64(batchSize)}
	response, err := APIResponse(c, func(client *apiclient.Client) (*generated.PrepareEmailWorkResp, error) {
		return client.PrepareEmailWorkWithResponse(ctx, &generated.PrepareEmailWorkRequestOptions{Body: &request})
	})
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("the daemon returned an empty preparation response")
	}
	result, err := decodeEmailWorkContract[EmailWorkPrepareResult](response.JSON200)
	if err != nil {
		return nil, err
	}
	if result.Progress == nil {
		return nil, errors.New("the daemon returned no preparation progress")
	}
	return result, nil
}
