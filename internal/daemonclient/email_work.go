package daemonclient

import (
	"context"
	"encoding/json"
	"fmt"

	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/store"
	apiclient "go.kenn.io/msgvault/pkg/client"
	"go.kenn.io/msgvault/pkg/client/generated"
)

func (c *Client) GetEmailWorkItem(ctx context.Context, viewID, viewRevision int64, uid, cacheRevision string, limit, offset int) (*query.EmailWorkMembersResponse, error) {
	if cacheRevision == "" {
		return nil, fmt.Errorf("%w: archive revision is required", store.ErrSavedViewInvalidState)
	}
	view, err := c.GetSavedView(ctx, viewID)
	if err != nil {
		return nil, err
	}
	if view.Revision != viewRevision {
		return nil, store.ErrSavedViewRevisionConflict
	}
	predicate, state, err := savedViewPredicate(*view, limit, "")
	if err != nil {
		return nil, err
	}
	work := false
	for _, filter := range state.Filters {
		if filter.Field == "row_unit" && len(filter.Values) == 1 && filter.Values[0] == string(query.RowUnitEmailWork) {
			work = true
		}
	}
	if !work {
		return nil, fmt.Errorf("%w: this Saved View displays individual messages", store.ErrSavedViewInvalidState)
	}
	request := generated.EmailWorkMembersHTTPRequest{UID: uid, Predicate: predicate, CacheRevision: &cacheRevision, Limit: new(int64(limit)), Offset: new(int64(offset))}
	resp, err := APIResponse(c, func(client *apiclient.Client) (*generated.GetEmailWorkMembersResp, error) {
		return client.GetEmailWorkMembersWithResponse(ctx, &generated.GetEmailWorkMembersRequestOptions{Body: &request})
	})
	if err != nil {
		return nil, err
	}
	// These are the same wire contract. Preserve every typed provenance field,
	// including future additive fields, instead of re-projecting a partial DTO.
	encoded, err := json.Marshal(resp.JSON200)
	if err != nil {
		return nil, err
	}
	var result query.EmailWorkMembersResponse
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
