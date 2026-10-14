package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"go.kenn.io/msgvault/internal/savedview"
)

func emailWorkRecoverySchema() map[string]*jsonschema.Schema {
	return map[string]*jsonschema.Schema{"saved_view_id": safeIDSchema("Producing Saved View ID"), "saved_view_revision": safeIDSchema("Producing Saved View revision"), "cache_revision": stringSchema("Exact archive cache revision from the selected result")}
}

func previewEmailWorkActionDefinition(_ *handlers) toolDefinition {
	properties := emailWorkRecoverySchema()
	properties["kind"] = stringSchema("Explicit local assertion to review", "link", "handled")
	properties["message_uids"] = &jsonschema.Schema{Type: "array", Items: stringSchema("Immutable source-copy message_uid from get_email_work_item"), MinItems: new(1), MaxItems: new(100)}
	definition := writeDefinition(ToolPreviewEmailWorkAction, "Review up to 100 fixed email copies for an undoable LOCAL link or handled confirmation. Use this practical recovery for inline forwards without trusted RFC linkage. The preview preserves copies and mailbox read/Inbox states; it grants no provider authority.", closedObject(properties, "saved_view_id", "saved_view_revision", "cache_revision", "kind", "message_uids"), outputSchemaFor[savedview.EmailWorkRecoveryPlan](), (*handlers).previewEmailWorkAction)
	definition.annotations = toolAnnotations(true)
	definition.availability = emailWorkRecoveryAvailable
	return definition
}

func applyEmailWorkActionDefinition(_ *handlers) toolDefinition {
	definition := writeDefinition(ToolApplyEmailWorkAction, "Apply the exact reviewed local recovery plan. The daemon rechecks current Member authority, every fixed source copy, Saved View revision and archive snapshot. Stale plans must be reviewed again. No provider labels or messages are changed; the returned action can be undone.", closedObject(map[string]*jsonschema.Schema{"plan": outputSchemaFor[savedview.EmailWorkRecoveryPlan]()}, "plan"), outputSchemaFor[savedview.EmailWorkRecoveryResult](), (*handlers).applyEmailWorkAction)
	definition.availability = emailWorkRecoveryAvailable
	return definition
}

func undoEmailWorkActionDefinition(_ *handlers) toolDefinition {
	properties := emailWorkRecoverySchema()
	properties["action_uid"] = stringSchema("Explicit local action UID returned by apply or work item local_actions")
	properties["action_revision"] = safeIDSchema("Current local action revision")
	properties["snapshot"] = stringSchema("Current recovery_snapshot from get_email_work_item; refresh before undo")
	definition := writeDefinition(ToolUndoEmailWorkAction, "Undo exactly one local link or handled assertion using its action UID and revisions. Other assertions and all source copies remain intact. Current source access is rechecked; an old creation retry cannot resurrect an undone assertion.", closedObject(properties, "saved_view_id", "saved_view_revision", "cache_revision", "action_uid", "action_revision", "snapshot"), outputSchemaFor[savedview.EmailWorkRecoveryResult](), (*handlers).undoEmailWorkAction)
	definition.availability = emailWorkRecoveryAvailable
	return definition
}

func emailWorkViewArguments(args map[string]any) (int64, int64, error) {
	view, err := getIDArg(args, "saved_view_id")
	if err != nil {
		return 0, 0, err
	}
	revision, err := getIDArg(args, "saved_view_revision")
	return view, revision, err
}

func (h *handlers) previewEmailWorkAction(ctx context.Context, request toolRequest) (*toolResult, error) {
	args := request.GetArguments()
	view, revision, err := emailWorkViewArguments(args)
	if err != nil {
		return toolErrorResult(err.Error()), nil
	}
	var uids []string
	switch values := args["message_uids"].(type) {
	case []any:
		for _, value := range values {
			uid, ok := value.(string)
			if !ok {
				return toolErrorResult("message_uids must contain immutable message UID strings"), nil
			}
			uids = append(uids, uid)
		}
	case []string:
		uids = values
	default:
		return toolErrorResult("message_uids must be a list of immutable message UID strings"), nil
	}
	kind, _ := args["kind"].(string)
	cache, _ := args["cache_revision"].(string)
	recovery, ok := h.savedViews.(savedview.EmailWorkRecovery)
	if !ok {
		return toolErrorResult("Local email work recovery is unavailable on this service"), nil
	}
	plan, err := recovery.PreviewEmailWorkAction(ctx, view, revision, kind, uids, cache)
	if err != nil {
		return savedViewHandlerError("preview local email work recovery", err)
	}
	return jsonResult(plan)
}

func (h *handlers) applyEmailWorkAction(ctx context.Context, request toolRequest) (*toolResult, error) {
	encoded, err := json.Marshal(request.GetArguments()["plan"])
	if err != nil {
		return toolErrorResult(fmt.Sprintf("A reviewed local recovery plan is required: %v", err)), nil
	}
	var plan savedview.EmailWorkRecoveryPlan
	if err := json.Unmarshal(encoded, &plan); err != nil {
		return toolErrorResult(fmt.Sprintf("The local recovery plan is invalid: %v", err)), nil
	}
	recovery, ok := h.savedViews.(savedview.EmailWorkRecovery)
	if !ok {
		return toolErrorResult("Local email work recovery is unavailable on this service"), nil
	}
	result, err := recovery.ApplyEmailWorkAction(ctx, plan)
	if err != nil {
		return savedViewHandlerError("apply local email work recovery", err)
	}
	return jsonResult(result)
}

func (h *handlers) undoEmailWorkAction(ctx context.Context, request toolRequest) (*toolResult, error) {
	args := request.GetArguments()
	view, revision, err := emailWorkViewArguments(args)
	if err != nil {
		return toolErrorResult(err.Error()), nil
	}
	actionRevision, err := getIDArg(args, "action_revision")
	if err != nil {
		return toolErrorResult(err.Error()), nil
	}
	uid, _ := args["action_uid"].(string)
	snapshot, _ := args["snapshot"].(string)
	cache, _ := args["cache_revision"].(string)
	recovery, ok := h.savedViews.(savedview.EmailWorkRecovery)
	if !ok {
		return toolErrorResult("Local email work recovery is unavailable on this service"), nil
	}
	result, err := recovery.UndoEmailWorkAction(ctx, view, revision, uid, snapshot, cache, actionRevision)
	if err != nil {
		return savedViewHandlerError("undo local email work recovery", err)
	}
	return jsonResult(result)
}
