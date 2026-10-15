package mcp

import (
	"context"

	"github.com/google/jsonschema-go/jsonschema"
	"go.kenn.io/msgvault/internal/query"
	"go.kenn.io/msgvault/internal/savedview"
)

func getEmailWorkItemDefinition(_ *handlers) toolDefinition {
	definition := readDefinition(ToolGetEmailWorkItem,
		"Read all permitted source copies and handling/link evidence for a related-email work item returned by run_saved_view. Keep the exact Saved View revision and archive cache revision from that result: an old selection must not silently widen to different accounts or family members.",
		closedObject(map[string]*jsonschema.Schema{
			"saved_view_id":       safeIDSchema("Producing Saved View ID"),
			"saved_view_revision": safeIDSchema("Producing Saved View revision"),
			"work_uid":            stringSchema("Opaque email_work.uid returned by the row"),
			"cache_revision":      stringSchema("Producing run_saved_view cache_revision"),
			"limit":               nonNegativeIntegerSchema("Copies per page (default 100, max 500)", 100),
			"offset":              nonNegativeIntegerSchema("Member offset (default zero)", 0),
		}, "saved_view_id", "saved_view_revision", "work_uid", "cache_revision"), outputSchemaFor[query.EmailWorkMembersResponse](), (*handlers).getEmailWorkItem)
	definition.availability = emailWorkAvailable
	return definition
}

func (h *handlers) getEmailWorkItem(ctx context.Context, req toolRequest) (*toolResult, error) {
	args := req.GetArguments()
	view, err := getIDArg(args, "saved_view_id")
	if err != nil {
		return toolErrorResult(err.Error()), nil
	}
	revision, err := getIDArg(args, "saved_view_revision")
	if err != nil {
		return toolErrorResult(err.Error()), nil
	}
	uid, _ := args["work_uid"].(string)
	cache, _ := args["cache_revision"].(string)
	if uid == "" || cache == "" {
		return toolErrorResult("work_uid and cache_revision are required"), nil
	}
	limit := intArg(args, "limit", 100)
	offset := intArg(args, "offset", 0)
	if limit < 0 || limit > 500 || offset < 0 {
		return toolErrorResult("member page must have a non-negative offset and a limit up to 500"), nil
	}
	reader, ok := h.savedViews.(savedview.EmailWorkReader)
	if !ok {
		return toolErrorResult("Related email details are unavailable on this Saved View service"), nil
	}
	response, err := reader.GetEmailWorkItem(ctx, view, revision, uid, cache, limit, offset)
	if err != nil {
		return savedViewHandlerError("read related email work item", err)
	}
	return jsonResult(response)
}
