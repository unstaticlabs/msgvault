package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/savedview"
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
	predicate, state, err := savedViewPredicate(*view, limit)
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
	if resp == nil {
		return nil, errors.New("the daemon returned an empty related email response")
	}
	return decodeEmailWorkContract[query.EmailWorkMembersResponse](resp.JSON200)
}

func (c *Client) emailWorkSavedPredicate(ctx context.Context, viewID, revision int64) (generated.ExploreHTTPRequest, error) {
	view, err := c.GetSavedView(ctx, viewID)
	if err != nil {
		return generated.ExploreHTTPRequest{}, err
	}
	if view.Revision != revision {
		return generated.ExploreHTTPRequest{}, store.ErrSavedViewRevisionConflict
	}
	predicate, state, err := savedViewPredicate(*view, 1)
	if err != nil {
		return generated.ExploreHTTPRequest{}, err
	}
	for _, filter := range state.Filters {
		if filter.Field == "row_unit" && len(filter.Values) == 1 && filter.Values[0] == string(query.RowUnitEmailWork) {
			return predicate, nil
		}
	}
	return generated.ExploreHTTPRequest{}, fmt.Errorf("%w: choose a Saved View that groups related email", store.ErrSavedViewInvalidState)
}

func decodeEmailWorkContract[T any](wire any) (*T, error) {
	if wire == nil || (reflect.ValueOf(wire).Kind() == reflect.Pointer && reflect.ValueOf(wire).IsNil()) {
		return nil, errors.New("the daemon returned an empty email work response")
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode email work response: %w", err)
	}
	var result T
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, fmt.Errorf("decode email work response: %w", err)
	}
	return &result, nil
}

func (c *Client) PreviewEmailWorkAction(ctx context.Context, viewID, viewRevision int64, kind string, uids []string, cacheRevision string) (*savedview.EmailWorkRecoveryPlan, error) {
	if len(uids) < 1 || len(uids) > 100 || cacheRevision == "" {
		return nil, fmt.Errorf("%w: choose up to 100 fixed message identities and an archive revision", store.ErrSavedViewInvalidState)
	}
	predicate, err := c.emailWorkSavedPredicate(ctx, viewID, viewRevision)
	if err != nil {
		return nil, err
	}
	var targets []store.EmailWorkActionTarget
	for _, uid := range uids {
		details, err := c.GetEmailWorkItem(ctx, viewID, viewRevision, uid, cacheRevision, 1, 0)
		if err != nil {
			return nil, err
		}
		if details.RequestedAnchor == nil || details.RequestedAnchor.MessageUID != uid {
			return nil, query.ErrEmailWorkRevisionChanged
		}
		targets = append(targets, store.EmailWorkActionTarget{UID: uid, SourceID: details.RequestedAnchor.SourceID})
	}
	wire, err := decodeEmailWorkContract[generated.EmailWorkActionRequest](savedview.EmailWorkRecoveryPlan{Kind: kind, Targets: targets, CacheRevision: cacheRevision, SavedViewID: viewID, SavedViewRevision: viewRevision})
	if err != nil {
		return nil, err
	}
	wire.Predicate = predicate
	response, err := APIResponse(c, func(client *apiclient.Client) (*generated.PreviewEmailWorkActionResp, error) {
		return client.PreviewEmailWorkActionWithResponse(ctx, &generated.PreviewEmailWorkActionRequestOptions{Body: wire})
	})
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("the daemon returned an empty email work preview")
	}
	return decodeEmailWorkContract[savedview.EmailWorkRecoveryPlan](response.JSON200)
}

func (c *Client) ApplyEmailWorkAction(ctx context.Context, plan savedview.EmailWorkRecoveryPlan) (*savedview.EmailWorkRecoveryResult, error) {
	predicate, err := c.emailWorkSavedPredicate(ctx, plan.SavedViewID, plan.SavedViewRevision)
	if err != nil {
		return nil, err
	}
	wire, err := decodeEmailWorkContract[generated.EmailWorkActionRequest](plan)
	if err != nil {
		return nil, err
	}
	wire.Predicate = predicate
	response, err := APIResponse(c, func(client *apiclient.Client) (*generated.ApplyEmailWorkActionResp, error) {
		return client.ApplyEmailWorkActionWithResponse(ctx, &generated.ApplyEmailWorkActionRequestOptions{Body: wire})
	})
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("the daemon returned an empty local recovery response")
	}
	return decodeEmailWorkContract[savedview.EmailWorkRecoveryResult](response.JSON200)
}

func (c *Client) UndoEmailWorkAction(ctx context.Context, viewID, viewRevision int64, uid, snapshot, cacheRevision string, actionRevision int64) (*savedview.EmailWorkRecoveryResult, error) {
	predicate, err := c.emailWorkSavedPredicate(ctx, viewID, viewRevision)
	if err != nil {
		return nil, err
	}
	wire := generated.EmailWorkUndoRequest{UID: uid, Snapshot: snapshot, CacheRevision: cacheRevision, ExpectedRevision: actionRevision, Predicate: predicate, SavedViewID: new(viewID), SavedViewRevision: new(viewRevision)}
	response, err := APIResponse(c, func(client *apiclient.Client) (*generated.UndoEmailWorkActionResp, error) {
		return client.UndoEmailWorkActionWithResponse(ctx, &generated.UndoEmailWorkActionRequestOptions{Body: &wire})
	})
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("the daemon returned an empty undo response")
	}
	return decodeEmailWorkContract[savedview.EmailWorkRecoveryResult](response.JSON200)
}
