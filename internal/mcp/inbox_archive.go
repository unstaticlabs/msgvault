package mcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"go.kenn.io/msgvault/internal/deletion"
)

// maxArchiveFromInboxResults bounds how many messages one selection may resolve
// to. It is far below stage_deletion's cap because this tool acts immediately:
// a call has to finish inside one MCP request without holding the daemon's
// operation gate long enough to stall everything else. Larger selections are
// archived over several calls, which is safe because a message that has left
// the inbox drops out of the next selection.
const maxArchiveFromInboxResults = 1000

// inboxArchiveSampleSize is how many messages a plan shows by name.
const inboxArchiveSampleSize = 10

// ErrMailboxWritesDisabled reports that the daemon has not opted in to mailbox
// writes. It is deliberately distinct from an authorization failure: nothing is
// wrong with the request, the operator simply has not enabled the capability.
var ErrMailboxWritesDisabled = errors.New("mailbox writes are disabled on this daemon")

// ErrInboxArchiveTokenInvalid reports a confirmation token that does not match
// the messages being confirmed, or that has already been spent.
var ErrInboxArchiveTokenInvalid = errors.New("inbox archive confirmation token is not valid for this selection")

// ErrInboxArchiveUnsupported reports a source whose provider cannot archive.
var ErrInboxArchiveUnsupported = errors.New("source does not support inbox archiving")

// ErrInboxArchiveScopeRequired reports an account added read-only, whose grant
// does not permit changing the mailbox.
var ErrInboxArchiveScopeRequired = errors.New("account's grant does not permit mailbox changes")

// ErrInboxArchiveBusy reports that the daemon is already running a mutating
// operation and declined to queue behind it.
var ErrInboxArchiveBusy = errors.New("daemon is busy with another operation")

// InboxArchiveAuthorizeRequest asks the daemon to authorize archiving an exact
// set of messages.
type InboxArchiveAuthorizeRequest struct {
	Account          string   `json:"account"`
	SourceID         int64    `json:"source_id"`
	Description      string   `json:"description"`
	SourceMessageIDs []string `json:"source_message_ids"`
}

// InboxArchiveExecuteRequest redeems a token minted by AuthorizeInboxArchive.
//
// SourceMessageIDs is optional. The daemon recorded the exact set when it
// minted the token, so a caller that has the token does not have to hold the
// list or rebuild the query that produced it. When ids are supplied they must
// match the ones the token was minted for, which is what lets a caller assert
// it is confirming the plan it thinks it is.
type InboxArchiveExecuteRequest struct {
	ConfirmationToken string   `json:"confirmation_token"`
	SourceMessageIDs  []string `json:"source_message_ids,omitempty"`
}

// InboxArchiveResult reports what the daemon did.
type InboxArchiveResult struct {
	BatchID   string   `json:"batch_id"`
	Account   string   `json:"account"`
	Archived  int      `json:"archived"`
	Failed    int      `json:"failed"`
	Remaining int      `json:"remaining"`
	FailedIDs []string `json:"failed_ids,omitempty"`
	Yielded   bool     `json:"yielded,omitempty"`
	// PartialFailure explains why a run stopped after archiving some of its
	// messages. Those stay archived; the rest were not attempted.
	PartialFailure string `json:"partial_failure,omitempty"`
}

// InboxArchiver removes messages from a mail account's inbox through the
// selected daemon. The MCP process holds no provider credentials of its own and
// never contacts a mail provider directly; it resolves which messages the user
// meant and asks the daemon to act.
//
// The split into authorize and execute exists so the token that permits an
// archive is minted by the daemon, bound to one exact set of messages, and
// spent once. It is an integrity check on the selection, not a human approval
// gate: a plan shown to a user cannot later be redeemed against different
// messages, and a single-call archive mints its own token over the set it just
// resolved. Whether a model may archive at all is the operator's decision,
// taken by enabling both --allow-mailbox-writes and the daemon's own opt-in.
type InboxArchiver interface {
	// AuthorizeInboxArchive mints a one-shot confirmation token for this set of
	// messages, or returns ErrMailboxWritesDisabled when the daemon has not
	// opted in to mailbox writes.
	AuthorizeInboxArchive(ctx context.Context, req InboxArchiveAuthorizeRequest) (string, error)

	// ExecuteInboxArchive archives the messages a token was minted for.
	ExecuteInboxArchive(ctx context.Context, req InboxArchiveExecuteRequest) (InboxArchiveResult, error)
}

type inboxArchiveSample struct {
	From    string `json:"from,omitempty"`
	Subject string `json:"subject,omitempty"`
	SentAt  string `json:"sent_at,omitempty"`
}

type inboxArchivePlanResponse struct {
	Status  string `json:"status"`
	Account string `json:"account"`
	// Selection describes what was matched, for a person to read. It is not a
	// query to send back: the confirmation_token identifies the plan.
	Selection    string `json:"selection"`
	MessageCount int    `json:"message_count"`
	// TotalMatching is how many messages the selection matches in total, which
	// can exceed message_count when the selection is larger than one call
	// archives. Omitted when the backend could not count them.
	TotalMatching *int                 `json:"total_matching,omitempty"`
	HasMore       bool                 `json:"has_more"`
	Truncated     bool                 `json:"truncated"`
	Stale         bool                 `json:"stale,omitempty"`
	Sample        []inboxArchiveSample `json:"sample,omitempty"`
	// ConfirmationToken identifies this plan and authorizes archiving exactly
	// the messages it covers. Sending it back with confirm=true is the whole
	// confirmation; no other argument is needed.
	ConfirmationToken string `json:"confirmation_token"`
	NextStep          string `json:"next_step"`
}

type inboxArchiveExecuteResponse struct {
	Status    string   `json:"status"`
	Account   string   `json:"account"`
	BatchID   string   `json:"batch_id"`
	Archived  int      `json:"archived"`
	Failed    int      `json:"failed"`
	Remaining int      `json:"remaining"`
	FailedIDs []string `json:"failed_ids,omitempty"`
	// TotalMatching and HasMore describe the selection this call came from, so
	// a caller working through a backlog knows whether to run again. They are
	// absent on a token-only confirmation, which names a plan rather than a
	// selection.
	TotalMatching *int   `json:"total_matching,omitempty"`
	HasMore       bool   `json:"has_more"`
	NextStep      string `json:"next_step"`
}

func inboxArchiveOutputSchema() *jsonschema.Schema {
	plan := outputSchemaFor[inboxArchivePlanResponse]()
	plan.Schema = ""
	executed := outputSchemaFor[inboxArchiveExecuteResponse]()
	executed.Schema = ""
	return &jsonschema.Schema{
		Schema: schema202012,
		Type:   "object",
		OneOf:  []*jsonschema.Schema{plan, executed},
	}
}

func archiveFromInboxDefinition(_ *handlers) toolDefinition {
	return mailboxWriteDefinition(
		ToolArchiveFromInbox,
		"Remove messages from the account's inbox at the mail provider. This CHANGES THE USER'S LIVE MAILBOX. "+
			"It does not delete anything: archived messages keep every other label, stay searchable at the provider, "+
			"and stay in the local archive; the user can move them back from their mail client. "+
			"Call with confirm=true to archive the selection in one call. Call without 'confirm' to get a plan "+
			"first -- the count, a sample, and a confirmation_token -- which changes nothing; do that when the "+
			"selection is broad or you are not confident it matches what the user asked for. To archive a plan, "+
			"send its confirmation_token back with confirm=true and nothing else: the token names the exact "+
			"messages the plan covered, so the query does not have to be repeated. "+
			"Use EITHER 'query' (Gmail-style search) OR structured filters, not both; add label:INBOX to the "+
			"selection so the count reflects messages that are actually in the inbox. "+
			"At most "+strconv.Itoa(maxArchiveFromInboxResults)+" messages move per call; the response reports "+
			"total_matching and has_more, so repeat the call until has_more is false.",
		closedObject(map[string]*jsonschema.Schema{
			toolArgAccount: accountProperty(),
			toolArgQuery: stringSchema(
				"Gmail-style search query (e.g. 'label:INBOX from:newsletter before:2024-01-01'). " +
					"Cannot be combined with structured filters."),
			toolArgFrom:      stringSchema("Filter by sender email address"),
			"domain":         stringSchema("Filter by sender domain (e.g. 'linkedin.com')"),
			"label":          stringSchema("Filter by label (use 'INBOX' to select only messages still in the inbox)"),
			toolArgAfter:     afterProperty(),
			toolArgBefore:    beforeProperty(),
			"has_attachment": booleanSchema("Only messages with attachments"),
			"confirm": booleanSchema(
				"Archive the selection. Without it the call returns a plan and changes nothing."),
			"confirmation_token": stringSchema(
				"The confirmation_token from a preceding plan call. Sent with confirm=true it archives " +
					"exactly the messages that plan covered, and no selection argument is needed or read. " +
					"Omit it to archive the selection this call describes."),
		}),
		inboxArchiveOutputSchema(),
		(*handlers).archiveFromInbox,
	)
}

func (h *handlers) archiveFromInbox(ctx context.Context, req toolRequest) (*toolResult, error) {
	if h.inboxArchiver == nil {
		return toolErrorResult(
			"mailbox_writes_unavailable: this server cannot archive messages at the mail provider"), nil
	}

	args := req.GetArguments()
	confirm, _ := args["confirm"].(bool)
	token, _ := args["confirmation_token"].(string)
	token = strings.TrimSpace(token)

	// A token names a plan the daemon still holds, including the exact
	// messages it covered. Redeeming it needs nothing else -- re-resolving the
	// selection here could only produce a different set, which is the drift
	// the token exists to prevent.
	if token != "" {
		if !confirm {
			return toolErrorResult(
				"confirmation_token was given without confirm=true; add confirm=true to archive the " +
					"plan it names, or omit the token to get a fresh plan. Nothing has been changed."), nil
		}
		return h.redeemInboxArchivePlan(ctx, token)
	}

	selection, result, err := h.resolveMutationTargets(ctx, args, mutationResolveOptions{
		limit:       maxArchiveFromInboxResults,
		operation:   "inbox archive",
		preferFresh: true,
	})
	if result != nil || err != nil {
		return result, err
	}
	if len(selection.targets) == 0 {
		return toolErrorResult("no messages match the specified criteria"), nil
	}

	source, sourceErr := deletion.SourceReferenceForTargets(selection.targets)
	switch {
	case errors.Is(sourceErr, deletion.ErrMultipleDeletionSources):
		return toolErrorResult(
			"selected messages span multiple sources; set account or archive each source separately"), nil
	case errors.Is(sourceErr, deletion.ErrIncompleteDeletionSource):
		return toolErrorResult("selected message has incomplete source metadata"), nil
	case sourceErr != nil:
		return toolErrorResult(sourceErr.Error()), nil
	}

	sourceMessageIDs := deletion.SourceMessageIDs(selection.targets)

	if !confirm {
		return h.planInboxArchive(ctx, selection, source, sourceMessageIDs)
	}

	// confirm without a token archives in one call. The token binds an execute
	// to one exact set of messages; it is an integrity check, not a human
	// approval gate. Minting it here closes the same gap for a single-call
	// archive, because the set it binds is the set this call just resolved.
	// Whether a model may do this at all was decided by the operator when they
	// enabled both --allow-mailbox-writes and the daemon's own opt-in.
	minted, mintErr := h.inboxArchiver.AuthorizeInboxArchive(ctx, InboxArchiveAuthorizeRequest{
		Account:          source.Identifier,
		SourceID:         source.ID,
		Description:      selection.description,
		SourceMessageIDs: sourceMessageIDs,
	})
	if translated := translateInboxArchiveErr(mintErr); translated != nil {
		return translated, nil
	}
	if mintErr != nil {
		return nil, newInternalError("authorize inbox archive", mintErr)
	}

	return h.executeInboxArchive(ctx, source.Identifier, sourceMessageIDs, minted, selection)
}

// redeemInboxArchivePlan archives the messages a plan covered, naming the plan
// by its token alone.
func (h *handlers) redeemInboxArchivePlan(ctx context.Context, token string) (*toolResult, error) {
	return h.executeInboxArchive(ctx, "", nil, token, nil)
}

func (h *handlers) executeInboxArchive(
	ctx context.Context,
	account string,
	sourceMessageIDs []string,
	token string,
	selection *mutationSelection,
) (*toolResult, error) {
	archiveResult, err := h.inboxArchiver.ExecuteInboxArchive(ctx, InboxArchiveExecuteRequest{
		ConfirmationToken: token,
		SourceMessageIDs:  sourceMessageIDs,
	})
	if translated := translateInboxArchiveErr(err); translated != nil {
		return translated, nil
	}
	if err != nil {
		return nil, newInternalError("archive messages from inbox", err)
	}
	if archiveResult.Account != "" {
		account = archiveResult.Account
	}

	response := inboxArchiveExecuteResponse{
		Status:    "archived",
		Account:   account,
		BatchID:   archiveResult.BatchID,
		Archived:  archiveResult.Archived,
		Failed:    archiveResult.Failed,
		Remaining: archiveResult.Remaining,
		FailedIDs: archiveResult.FailedIDs,
		NextStep:  inboxArchiveNextStep(archiveResult, selection),
	}
	if selection != nil {
		response.TotalMatching = matchTotal(selection)
		response.HasMore = selection.hasMore
	}
	return jsonResult(response)
}

// matchTotal renders a resolved total, or nothing when the backend could not
// report one. A caller must be able to tell "no more matches" from "unknown",
// which a plain zero would hide.
func matchTotal(selection *mutationSelection) *int {
	if selection == nil || selection.totalMatching == unknownMatchTotal {
		return nil
	}
	total := selection.totalMatching
	return &total
}

func (h *handlers) planInboxArchive(
	ctx context.Context,
	selection *mutationSelection,
	source deletion.SourceReference,
	sourceMessageIDs []string,
) (*toolResult, error) {
	token, err := h.inboxArchiver.AuthorizeInboxArchive(ctx, InboxArchiveAuthorizeRequest{
		Account:          source.Identifier,
		SourceID:         source.ID,
		Description:      selection.description,
		SourceMessageIDs: sourceMessageIDs,
	})
	if translated := translateInboxArchiveErr(err); translated != nil {
		return translated, nil
	}
	if err != nil {
		return nil, newInternalError("authorize inbox archive", err)
	}

	nextStep := fmt.Sprintf(
		"Nothing has changed yet. To archive exactly these %d messages, call %s again with "+
			"confirm=true and confirmation_token=%q -- no selection argument is needed, and the "+
			"token covers this set only.",
		len(sourceMessageIDs), ToolArchiveFromInbox, token)
	if selection.hasMore {
		nextStep += " " + inboxArchiveMorePhrase(selection) +
			" Repeat plan-then-confirm until a call reports has_more false."
	}
	if !selection.freshlyResolved {
		nextStep += " " + inboxArchiveStaleWarning
	}

	return jsonResult(inboxArchivePlanResponse{
		Status:            "plan",
		Account:           source.Identifier,
		Selection:         selection.description,
		MessageCount:      len(sourceMessageIDs),
		TotalMatching:     matchTotal(selection),
		HasMore:           selection.hasMore,
		Truncated:         selection.hasMore,
		Stale:             !selection.freshlyResolved,
		Sample:            h.inboxArchiveSample(ctx, selection),
		ConfirmationToken: token,
		NextStep:          nextStep,
	})
}

// inboxArchiveMorePhrase says how much of the selection this call leaves.
func inboxArchiveMorePhrase(selection *mutationSelection) string {
	if selection.totalMatching == unknownMatchTotal {
		return "The selection holds more than this call covers."
	}
	return fmt.Sprintf("The selection matches %d messages in total.", selection.totalMatching)
}

// inboxArchiveStaleWarning explains a selection resolved from the analytics
// cache rather than the archive of record. The cache is rebuilt when messages
// arrive or are deleted, not when their labels change, so it can still offer
// messages a previous call already archived.
const inboxArchiveStaleWarning = "This selection came from the analytics cache, which does not " +
	"track label changes, so it may still list messages an earlier call archived; archiving them " +
	"again is harmless but the counts will not fall. Scope the call to one account with 'account' " +
	"to have it resolved against the archive of record instead."

// inboxArchiveSample hydrates a handful of the selected messages so the plan
// names real mail rather than only a count. A sample the user recognises is
// what makes a wrong selection visible before it is confirmed, so a failure to
// build one is not worth failing the plan over.
func (h *handlers) inboxArchiveSample(
	ctx context.Context, selection *mutationSelection,
) []inboxArchiveSample {
	ids := make([]int64, 0, inboxArchiveSampleSize)
	for _, target := range selection.targets {
		if len(ids) == inboxArchiveSampleSize {
			break
		}
		if target.MessageID > 0 {
			ids = append(ids, target.MessageID)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	summaries, err := h.engine.GetMessageSummariesByIDs(ctx, ids)
	if err != nil {
		return nil
	}

	samples := make([]inboxArchiveSample, 0, len(summaries))
	for _, summary := range summaries {
		sample := inboxArchiveSample{
			From:    summary.FromEmail,
			Subject: summary.Subject,
		}
		if !summary.SentAt.IsZero() {
			sample.SentAt = summary.SentAt.Format("2006-01-02")
		}
		samples = append(samples, sample)
	}
	return samples
}

// inboxArchiveNextStep says what, if anything, is left to do. selection is nil
// when a plan was redeemed by token, in which case this call knows nothing
// about the wider selection that produced it.
func inboxArchiveNextStep(result InboxArchiveResult, selection *mutationSelection) string {
	switch {
	case result.PartialFailure != "":
		return fmt.Sprintf(
			"Stopped after archiving %d messages: %s. Those stay archived; the rest "+
				"were not attempted. Report this to the user before trying again.",
			result.Archived, result.PartialFailure)
	case result.Remaining > 0 && result.Yielded:
		return fmt.Sprintf(
			"Paused after %d messages to let another request through; %d of this batch remain. "+
				"Call again with the same selection to continue.",
			result.Archived, result.Remaining)
	case result.Remaining > 0:
		return fmt.Sprintf(
			"%d messages of this batch remain. Call again with the same selection to continue.",
			result.Remaining)
	case selection != nil && selection.hasMore:
		return inboxArchiveMorePhrase(selection) +
			" Call again with the same selection to continue, until has_more is false."
	case result.Failed > 0:
		return "Some messages could not be archived and kept their inbox label; they are listed in failed_ids."
	default:
		return "The selection is archived. The messages are out of the inbox at the provider and " +
			"in the local archive; searching the archive may keep listing them as INBOX until its " +
			"analytics cache is next rebuilt, so trust this result over a search."
	}
}

// translateInboxArchiveErr maps the seam's known conditions to tool errors the
// model can act on, rather than surfacing them as internal failures.
func translateInboxArchiveErr(err error) *toolResult {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrMailboxWritesDisabled):
		return toolErrorResult(
			"mailbox_writes_disabled: the daemon has not enabled mailbox writes. " +
				"Ask the user to set '[inbox_archive] remote_enabled = true' in the daemon's config.toml " +
				"and restart it. Nothing has been changed.")
	case errors.Is(err, ErrInboxArchiveTokenInvalid):
		return toolErrorResult(
			"confirmation_token_invalid: the token does not match this set of messages, or has already " +
				"been used. Call again without 'confirm' to get a fresh plan, show it to the user, and " +
				"confirm that one. Nothing has been changed.")
	case errors.Is(err, ErrInboxArchiveUnsupported):
		return toolErrorResult(
			"unsupported_source: this account's provider cannot archive messages from the inbox. " +
				"Only Gmail and IMAP accounts support it.")
	case errors.Is(err, ErrInboxArchiveScopeRequired):
		return toolErrorResult(
			"scope_escalation_required: this account was added read-only, so its authorization " +
				"does not permit changing the mailbox. Ask the user to re-authorize it by running " +
				"'msgvault add-account <email>'. Nothing has been changed.")
	case errors.Is(err, ErrInboxArchiveBusy):
		return toolErrorResult(
			"busy: the daemon is running another operation. Nothing has been changed; try again shortly.")
	default:
		return nil
	}
}
