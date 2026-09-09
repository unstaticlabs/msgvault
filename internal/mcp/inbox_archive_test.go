package mcp

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/query/querytest"
	"go.kenn.io/msgvault/internal/search"
)

// fakeInboxArchiver records what the tool asked the daemon to do.
type fakeInboxArchiver struct {
	token         string
	authorizeErr  error
	executeErr    error
	result        InboxArchiveResult
	authorizeReqs []InboxArchiveAuthorizeRequest
	executeReqs   []InboxArchiveExecuteRequest
}

func (f *fakeInboxArchiver) AuthorizeInboxArchive(
	_ context.Context, req InboxArchiveAuthorizeRequest,
) (string, error) {
	f.authorizeReqs = append(f.authorizeReqs, req)
	if f.authorizeErr != nil {
		return "", f.authorizeErr
	}
	if f.token == "" {
		return "token-abc", nil
	}
	return f.token, nil
}

func (f *fakeInboxArchiver) ExecuteInboxArchive(
	_ context.Context, req InboxArchiveExecuteRequest,
) (InboxArchiveResult, error) {
	f.executeReqs = append(f.executeReqs, req)
	if f.executeErr != nil {
		return InboxArchiveResult{}, f.executeErr
	}
	return f.result, nil
}

func inboxArchiveEngine() *querytest.MockEngine {
	return &querytest.MockEngine{
		Accounts: []query.AccountInfo{
			{ID: 1, SourceType: "gmail", Identifier: "alice@example.com"},
		},
		GmailIDs: []string{"gmail-001", "gmail-002"},
		SearchFastResults: []query.MessageSummary{
			{ID: 1, SourceID: 1, Subject: "Newsletter", FromEmail: "news@example.com", SourceMessageID: "gmail-001"},
			{ID: 2, SourceID: 1, Subject: "Promo", FromEmail: "promo@example.com", SourceMessageID: "gmail-002"},
		},
	}
}

// TestArchiveFromInboxNeedsBothOptIns is the gate test. stdio passes
// allowWrites=true unconditionally, so allowWrites alone must not be enough to
// expose a tool that reaches the user's live mailbox.
func TestArchiveFromInboxNeedsBothOptIns(t *testing.T) {
	archiver := &fakeInboxArchiver{}

	tests := []struct {
		name    string
		opts    ServeOptions
		exposed bool
	}{
		{
			name: "neither opt-in",
			opts: ServeOptions{Engine: &querytest.MockEngine{}},
		},
		{
			name: "flag without a daemon seam",
			opts: ServeOptions{Engine: &querytest.MockEngine{}, AllowMailboxWrites: true},
		},
		{
			name: "daemon seam without the flag",
			opts: ServeOptions{Engine: &querytest.MockEngine{}, InboxArchiver: archiver},
		},
		{
			name:    "both",
			opts:    ServeOptions{Engine: &querytest.MockEngine{}, AllowMailboxWrites: true, InboxArchiver: archiver},
			exposed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// allowWrites=true is what stdio hardcodes.
			byName := toolsByName(t, rawListTools(t, tc.opts, true))
			_, present := byName[ToolArchiveFromInbox]
			assert.Equal(t, tc.exposed, present)

			// A read-only server never exposes it, whatever else is set.
			readOnly := toolsByName(t, rawListTools(t, tc.opts, false))
			assert.NotContains(t, readOnly, ToolArchiveFromInbox)
		})
	}
}

// TestArchiveFromInboxIsTheOnlyOpenWorldTool guards the annotation change.
// Local tools may be marked destructive -- deleting a saved view is -- but this
// is the only one that reaches a system outside the archive, and openWorldHint
// is what says so. Adding a shared annotation helper is exactly the kind of
// change that could silently flip the rest of the catalog, so every other tool
// is checked too.
func TestArchiveFromInboxIsTheOnlyOpenWorldTool(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	opts := ServeOptions{
		Engine:             &querytest.MockEngine{},
		AllowMailboxWrites: true,
		InboxArchiver:      &fakeInboxArchiver{},
	}
	byName := toolsByName(t, rawListTools(t, opts, true))

	archive, ok := byName[ToolArchiveFromInbox]
	require.True(ok, "archive tool must be registered for this check")
	annotations, ok := archive["annotations"].(map[string]any)
	require.True(ok, "annotations: %#v", archive)

	assert.Equal(true, annotations["destructiveHint"])
	assert.Equal(true, annotations["openWorldHint"])
	assert.Equal(true, annotations["idempotentHint"])
	assert.Equal(false, annotations["readOnlyHint"])

	for name, tool := range byName {
		if name == ToolArchiveFromInbox {
			continue
		}
		other, ok := tool["annotations"].(map[string]any)
		require.True(ok, "%s annotations: %#v", name, tool)
		assert.Equal(false, other["openWorldHint"], "%s must stay closed-world", name)
	}
}

func TestArchiveFromInboxPlanChangesNothing(t *testing.T) {
	assert := assert.New(t)

	archiver := &fakeInboxArchiver{token: "token-xyz"}
	h := &handlers{engine: inboxArchiveEngine(), inboxArchiver: archiver}

	resp := runTool[inboxArchivePlanResponse](
		t, ToolArchiveFromInbox, h.archiveFromInbox,
		map[string]any{"query": "label:INBOX from:news"},
	)

	assert.Equal("plan", resp.Status)
	assert.Equal("alice@example.com", resp.Account)
	assert.Equal(2, resp.MessageCount)
	assert.False(resp.Truncated)
	assert.Equal("token-xyz", resp.ConfirmationToken)
	assert.Contains(resp.NextStep, "confirmation_token=\"token-xyz\"")
	assert.Contains(resp.NextStep, "no selection argument is needed")
	assert.Empty(archiver.executeReqs, "a plan must not archive anything")

	require.Len(t, archiver.authorizeReqs, 1)
	assert.Equal([]string{"gmail-001", "gmail-002"},
		archiver.authorizeReqs[0].SourceMessageIDs)
}

// TestArchiveFromInboxPlanNamesRealMessages: a count alone cannot show the user
// that the selection is wrong. The plan carries a sample.
func TestArchiveFromInboxPlanNamesRealMessages(t *testing.T) {
	assert := assert.New(t)

	engine := inboxArchiveEngine()
	engine.GetMessageSummariesByIDsFunc = func(
		_ context.Context, ids []int64,
	) ([]query.MessageSummary, error) {
		assert.Equal([]int64{1, 2}, ids)
		return engine.SearchFastResults, nil
	}
	h := &handlers{engine: engine, inboxArchiver: &fakeInboxArchiver{}}

	resp := runTool[inboxArchivePlanResponse](
		t, ToolArchiveFromInbox, h.archiveFromInbox,
		map[string]any{"query": "label:INBOX from:news"},
	)

	assert.Len(resp.Sample, 2)
	assert.Equal("Newsletter", resp.Sample[0].Subject)
	assert.Equal("news@example.com", resp.Sample[0].From)
}

// TestArchiveFromInboxConfirmWithoutTokenArchivesDirectly: confirm alone is
// enough. The token binds an execute to one exact selection -- it is an
// integrity check, not a human approval gate -- so a single-call archive mints
// its own over the set it just resolved. Whether a model may archive at all was
// decided by the operator when they enabled both opt-ins.
func TestArchiveFromInboxConfirmWithoutTokenArchivesDirectly(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	archiver := &fakeInboxArchiver{
		token:  "minted-inline",
		result: InboxArchiveResult{BatchID: "batch-1", Archived: 2},
	}
	h := &handlers{engine: inboxArchiveEngine(), inboxArchiver: archiver}

	resp := runTool[inboxArchiveExecuteResponse](
		t, ToolArchiveFromInbox, h.archiveFromInbox,
		map[string]any{"query": "from:news", "confirm": true},
	)

	assert.Equal("archived", resp.Status)
	assert.Equal(2, resp.Archived)

	// The token it spends must cover exactly the messages it resolved, so a
	// selection cannot drift between minting and archiving.
	require.Len(archiver.authorizeReqs, 1)
	require.Len(archiver.executeReqs, 1)
	assert.Equal([]string{"gmail-001", "gmail-002"}, archiver.authorizeReqs[0].SourceMessageIDs)
	assert.Equal([]string{"gmail-001", "gmail-002"}, archiver.executeReqs[0].SourceMessageIDs)
	assert.Equal("minted-inline", archiver.executeReqs[0].ConfirmationToken)
}

// TestArchiveFromInboxWithoutConfirmStillPlans: the plan remains available for
// a broad or uncertain selection, and still changes nothing.
func TestArchiveFromInboxWithoutConfirmStillPlans(t *testing.T) {
	assert := assert.New(t)

	archiver := &fakeInboxArchiver{}
	h := &handlers{engine: inboxArchiveEngine(), inboxArchiver: archiver}

	resp := runTool[inboxArchivePlanResponse](
		t, ToolArchiveFromInbox, h.archiveFromInbox,
		map[string]any{"query": "from:news"},
	)

	assert.Equal("plan", resp.Status)
	assert.Empty(archiver.executeReqs, "a plan must still archive nothing")
}

// TestArchiveFromInboxRedeemsAPlanByTokenAlone: a confirmation token names the
// exact messages the daemon recorded when it minted the token, so redeeming it
// must not depend on the caller reproducing the selection. Re-resolving here
// could only produce a different set -- which is the drift the token exists to
// prevent -- and it is what made a plan unconfirmable when its query could not
// be rebuilt.
func TestArchiveFromInboxRedeemsAPlanByTokenAlone(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	archiver := &fakeInboxArchiver{
		result: InboxArchiveResult{
			BatchID: "batch-1", Account: "alice@example.com", Archived: 2,
		},
	}
	engine := inboxArchiveEngine()
	engine.SearchFastFunc = func(
		context.Context, *search.Query, query.MessageFilter, int, int,
	) ([]query.MessageSummary, error) {
		assert.Fail("redeeming a token must not re-resolve the selection")
		return nil, nil
	}
	h := &handlers{engine: engine, inboxArchiver: archiver}

	resp := runTool[inboxArchiveExecuteResponse](
		t, ToolArchiveFromInbox, h.archiveFromInbox,
		map[string]any{"confirm": true, "confirmation_token": "token-xyz"},
	)

	assert.Equal("archived", resp.Status)
	assert.Equal("batch-1", resp.BatchID)
	assert.Equal("alice@example.com", resp.Account)
	assert.Equal(2, resp.Archived)
	assert.NotContains(resp.NextStep, "incremental sync")
	assert.Contains(resp.NextStep, "this plan covered",
		"a redeemed plan may be part of a larger selection and must not claim it is finished")

	require.Len(archiver.executeReqs, 1)
	assert.Equal("token-xyz", archiver.executeReqs[0].ConfirmationToken)
	assert.Empty(archiver.executeReqs[0].SourceMessageIDs,
		"the daemon holds the set the token covers")
}

// TestArchiveFromInboxTokenWithoutConfirmChangesNothing: a token alone is not a
// request to archive. Treating it as one would turn a caller that meant to
// inspect the plan into one that executed it.
func TestArchiveFromInboxTokenWithoutConfirmChangesNothing(t *testing.T) {
	archiver := &fakeInboxArchiver{}
	h := &handlers{engine: inboxArchiveEngine(), inboxArchiver: archiver}

	result := runToolExpectError(t, ToolArchiveFromInbox, h.archiveFromInbox,
		map[string]any{"confirmation_token": "token-xyz"})

	assert.Contains(t, result.text, "confirm=true")
	assert.Empty(t, archiver.executeReqs)
}

func TestArchiveFromInboxReportsRemainingWork(t *testing.T) {
	assert := assert.New(t)

	h := &handlers{
		engine: inboxArchiveEngine(),
		inboxArchiver: &fakeInboxArchiver{
			result: InboxArchiveResult{BatchID: "b", Archived: 1, Remaining: 40, Yielded: true},
		},
	}

	resp := runTool[inboxArchiveExecuteResponse](
		t, ToolArchiveFromInbox, h.archiveFromInbox,
		map[string]any{"query": "from:news", "confirm": true, "confirmation_token": "t"},
	)

	assert.Equal(40, resp.Remaining)
	assert.Contains(resp.NextStep, "Paused")
}

// TestArchiveFromInboxTranslatesDaemonRefusals: each of these is a condition
// the model can act on, so none of them may surface as an internal error.
// TestArchiveFromInboxReportsPartialFailure: a run that stopped part-way must
// tell the user what was archived and why it stopped, not read as a clean
// success or a total failure.
func TestArchiveFromInboxReportsPartialFailure(t *testing.T) {
	assert := assert.New(t)

	h := &handlers{
		engine: inboxArchiveEngine(),
		inboxArchiver: &fakeInboxArchiver{
			result: InboxArchiveResult{
				BatchID: "b", Archived: 3, Failed: 1, Remaining: 6,
				PartialFailure: "provider refused the batch",
			},
		},
	}

	resp := runTool[inboxArchiveExecuteResponse](
		t, ToolArchiveFromInbox, h.archiveFromInbox,
		map[string]any{"query": "from:news", "confirm": true, "confirmation_token": "t"},
	)

	assert.Equal(3, resp.Archived)
	assert.Contains(resp.NextStep, "Stopped after archiving 3 messages")
	assert.Contains(resp.NextStep, "provider refused the batch")
	assert.Contains(resp.NextStep, "stay archived")
}

func TestArchiveFromInboxTranslatesDaemonRefusals(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantSub string
	}{
		{"writes disabled", ErrMailboxWritesDisabled, "mailbox_writes_disabled"},
		{"stale token", ErrInboxArchiveTokenInvalid, "confirmation_token_invalid"},
		{"unsupported provider", ErrInboxArchiveUnsupported, "unsupported_source"},
		{"read-only grant", ErrInboxArchiveScopeRequired, "scope_escalation_required"},
		{"daemon busy", ErrInboxArchiveBusy, "busy"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &handlers{
				engine:        inboxArchiveEngine(),
				inboxArchiver: &fakeInboxArchiver{executeErr: tc.err, authorizeErr: tc.err},
			}

			executed := runToolExpectError(t, ToolArchiveFromInbox, h.archiveFromInbox,
				map[string]any{"query": "from:news", "confirm": true, "confirmation_token": "t"})
			assert.Contains(t, resultText(t, executed), tc.wantSub)

			// The same condition must also stop the plan, so the user is never
			// asked to confirm something that cannot happen.
			planned := runToolExpectError(t, ToolArchiveFromInbox, h.archiveFromInbox,
				map[string]any{"query": "from:news"})
			assert.Contains(t, resultText(t, planned), tc.wantSub)
		})
	}
}

func TestArchiveFromInboxWithoutSeamIsUnavailable(t *testing.T) {
	h := &handlers{engine: inboxArchiveEngine()}

	result := runToolExpectError(t, ToolArchiveFromInbox, h.archiveFromInbox,
		map[string]any{"query": "from:news"})

	assert.Contains(t, resultText(t, result), "mailbox_writes_unavailable")
}

// TestArchiveFromInboxResolvesAgainstTheArchiveOfRecord: the analytics cache is
// rebuilt when messages arrive or are deleted, never when their labels change.
// A tool that archives by removing a label therefore cannot read its own work
// back out of the cache: the batch it just archived still looks unarchived, the
// same messages are selected again, and the run never converges. When the call
// names one Gmail account the selection is resolved against the archive of
// record instead.
func TestArchiveFromInboxResolvesAgainstTheArchiveOfRecord(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	engine := inboxArchiveEngine()
	var resolvedFilter query.MessageFilter
	engine.GetDeletionTargetsBySearchFunc = func(
		_ context.Context, _ *search.Query, filter query.MessageFilter, mode query.DeletionSearchMode,
	) ([]query.DeletionTarget, error) {
		resolvedFilter = filter
		assert.Equal(query.DeletionSearchFast, mode)
		return []query.DeletionTarget{{
			MessageID: 7, SourceID: 1, SourceType: "gmail",
			SourceIdentifier: "alice@example.com", SourceMessageID: "gmail-007",
		}}, nil
	}
	engine.SearchFastFunc = func(
		context.Context, *search.Query, query.MessageFilter, int, int,
	) ([]query.MessageSummary, error) {
		assert.Fail("a Gmail account must not be resolved from the analytics cache")
		return nil, nil
	}
	archiver := &fakeInboxArchiver{token: "token-fresh"}
	h := &handlers{engine: engine, inboxArchiver: archiver}

	resp := runTool[inboxArchivePlanResponse](
		t, ToolArchiveFromInbox, h.archiveFromInbox,
		map[string]any{"account": "alice@example.com", "query": "label:INBOX from:news"},
	)

	assert.False(resp.Stale, "a fresh resolution must not be reported as stale")
	assert.Equal(1, resp.MessageCount)
	require.NotNil(resp.TotalMatching)
	assert.Equal(1, *resp.TotalMatching)
	assert.False(resp.HasMore)
	require.NotNil(resolvedFilter.SourceID)
	assert.Equal(int64(1), *resolvedFilter.SourceID, "the resolution must stay scoped to the account")

	require.Len(archiver.authorizeReqs, 1)
	assert.Equal([]string{"gmail-007"}, archiver.authorizeReqs[0].SourceMessageIDs)
}

// TestArchiveFromInboxPlanKeepsTheWholeSelectionText: the plan's selection is
// what a person reads to recognise a wrong batch, and a caller that mistakes it
// for a query to replay must not be handed a truncated one. It used to be cut
// at 50 characters, which silently dropped the tail of a date bound.
func TestArchiveFromInboxPlanKeepsTheWholeSelectionText(t *testing.T) {
	longQuery := "label:INBOX from:notifications@some-quite-long-sender.example.com before:2026-02-01"
	h := &handlers{engine: inboxArchiveEngine(), inboxArchiver: &fakeInboxArchiver{token: "t"}}

	resp := runTool[inboxArchivePlanResponse](
		t, ToolArchiveFromInbox, h.archiveFromInbox,
		map[string]any{"query": longQuery},
	)

	assert.Equal(t, "query: "+longQuery, resp.Selection)
}

// TestArchiveFromInboxReportsWhatItDidNotCover: a selection larger than one
// call must say so, or a caller cannot tell "exactly this many matched" from
// "this is the first page". Without it a backlog is worked through by guessing.
func TestArchiveFromInboxReportsWhatItDidNotCover(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	engine := inboxArchiveEngine()
	engine.GetDeletionTargetsBySearchFunc = func(
		context.Context, *search.Query, query.MessageFilter, query.DeletionSearchMode,
	) ([]query.DeletionTarget, error) {
		targets := make([]query.DeletionTarget, maxArchiveFromInboxResults+25)
		for i := range targets {
			targets[i] = query.DeletionTarget{
				MessageID: int64(i + 1), SourceID: 1, SourceType: "gmail",
				SourceIdentifier: "alice@example.com",
				SourceMessageID:  "gmail-" + strconv.Itoa(i),
			}
		}
		return targets, nil
	}
	archiver := &fakeInboxArchiver{token: "token-big"}
	h := &handlers{engine: engine, inboxArchiver: archiver}

	resp := runTool[inboxArchivePlanResponse](
		t, ToolArchiveFromInbox, h.archiveFromInbox,
		map[string]any{"account": "alice@example.com", "query": "label:INBOX"},
	)

	assert.Equal(maxArchiveFromInboxResults, resp.MessageCount)
	assert.True(resp.HasMore)
	require.NotNil(resp.TotalMatching)
	assert.Equal(maxArchiveFromInboxResults+25, *resp.TotalMatching)
	assert.Contains(resp.NextStep, strconv.Itoa(maxArchiveFromInboxResults+25))

	require.Len(archiver.authorizeReqs, 1)
	assert.Len(archiver.authorizeReqs[0].SourceMessageIDs, maxArchiveFromInboxResults)
}

// TestArchiveFromInboxPagesPastTheDaemonPageCap: the daemon clamps one search
// page to 500 whatever the caller asks for, so a single request for the tool's
// full selection silently returned 500 and reported nothing about the rest.
func TestArchiveFromInboxPagesPastTheDaemonPageCap(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const available = 1200
	all := make([]query.MessageSummary, available)
	for i := range all {
		all[i] = query.MessageSummary{
			ID: int64(i + 1), SourceID: 1, SourceMessageID: "gmail-" + strconv.Itoa(i),
		}
	}
	engine := inboxArchiveEngine()
	var pages []int
	engine.SearchFastFunc = func(
		_ context.Context, _ *search.Query, _ query.MessageFilter, limit, offset int,
	) ([]query.MessageSummary, error) {
		// The daemon's own ceiling, reproduced: asking for more returns 500.
		if limit > 500 {
			limit = 500
		}
		pages = append(pages, limit)
		if offset >= len(all) {
			return nil, nil
		}
		return all[offset:min(offset+limit, len(all))], nil
	}
	engine.SearchFastCountFunc = func(
		context.Context, *search.Query, query.MessageFilter,
	) (int64, error) {
		return available, nil
	}
	archiver := &fakeInboxArchiver{token: "token-paged"}
	h := &handlers{engine: engine, inboxArchiver: archiver}

	resp := runTool[inboxArchivePlanResponse](
		t, ToolArchiveFromInbox, h.archiveFromInbox,
		map[string]any{"query": "label:INBOX"},
	)

	assert.Equal(maxArchiveFromInboxResults, resp.MessageCount)
	assert.True(resp.HasMore)
	require.NotNil(resp.TotalMatching)
	assert.Equal(available, *resp.TotalMatching)
	assert.Equal([]int{500, 500}, pages, "the selection must be paged in units the daemon honours")
	assert.True(resp.Stale, "a cache-resolved selection must say so")
}

// TestArchiveFromInboxDoesNotSelectAMessageTwice: results come back newest
// first, so mail arriving while the pages are walked shifts every later result
// back by one and a message can land on two pages. Selecting it twice would
// double-count the batch.
func TestArchiveFromInboxDoesNotSelectAMessageTwice(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	page := func(first int64, n int) []query.MessageSummary {
		out := make([]query.MessageSummary, n)
		for i := range out {
			id := first + int64(i)
			out[i] = query.MessageSummary{
				ID: id, SourceID: 1,
				SourceMessageID: "gmail-" + strconv.FormatInt(id, 10),
			}
		}
		return out
	}
	engine := inboxArchiveEngine()
	calls := 0
	engine.SearchFastFunc = func(
		context.Context, *search.Query, query.MessageFilter, int, int,
	) ([]query.MessageSummary, error) {
		calls++
		switch calls {
		case 1:
			return page(1, 500), nil
		case 2:
			// One message arrived, so this page repeats the last of page one.
			return page(500, 200), nil
		default:
			return nil, nil
		}
	}
	archiver := &fakeInboxArchiver{token: "token-dedup"}
	h := &handlers{engine: engine, inboxArchiver: archiver}

	resp := runTool[inboxArchivePlanResponse](
		t, ToolArchiveFromInbox, h.archiveFromInbox,
		map[string]any{"query": "label:INBOX"},
	)

	assert.Equal(699, resp.MessageCount, "the repeated message must be selected once")
	require.Len(archiver.authorizeReqs, 1)
	ids := archiver.authorizeReqs[0].SourceMessageIDs
	assert.Len(ids, 699)
	unique := make(map[string]bool, len(ids))
	for _, id := range ids {
		require.Falsef(unique[id], "duplicate id %s", id)
		unique[id] = true
	}
}
