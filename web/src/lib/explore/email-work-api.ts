import type { APIClient } from "../api/client";
import type { APIResponse } from "../api/runtime";
import {
  getEmailWorkMembers,
  previewEmailWorkAction,
  exportEmailWorkItem,
} from "../api/generated/exploration/exploration";
import {
  applyEmailWorkAction,
  undoEmailWorkAction,
  getMessage,
} from "../api/generated/api/api";
import type {
  EmailWorkActionRequest,
  EmailWorkMembersHTTPRequest,
  EmailWorkUndoRequest,
  EmailWorkExportRequest,
} from "../api/generated/models";
function requireData<T>(result: APIResponse<T>): T {
  if (!result.response.ok || !result.data)
    throw new Error(
      result.error?.message ??
        "Could not complete this request. Refresh and try again.",
    );
  return result.data;
}
export async function emailWorkMembers(
  client: APIClient,
  body: EmailWorkMembersHTTPRequest,
  signal?: AbortSignal,
) {
  return requireData(await getEmailWorkMembers(body, { ...client, signal }));
}
export async function emailWorkPreview(
  client: APIClient,
  body: EmailWorkActionRequest,
  signal?: AbortSignal,
) {
  return requireData(await previewEmailWorkAction(body, { ...client, signal }));
}
export async function emailWorkApply(
  client: APIClient,
  body: EmailWorkActionRequest,
  signal?: AbortSignal,
) {
  return requireData(await applyEmailWorkAction(body, { ...client, signal }));
}
export async function emailWorkUndo(
  client: APIClient,
  body: EmailWorkUndoRequest,
  signal?: AbortSignal,
) {
  return requireData(await undoEmailWorkAction(body, { ...client, signal }));
}
export async function emailWorkExport(
  client: APIClient,
  body: EmailWorkExportRequest,
  signal?: AbortSignal,
) {
  const result = await exportEmailWorkItem(body, { ...client, signal });
  return {
    blob: requireData(result),
    state: result.response.headers.get("X-Email-Work-Export-State"),
  };
}
export async function emailWorkBody(
  client: APIClient,
  id: number,
  uid: string,
  signal?: AbortSignal,
) {
  return requireData(
    await getMessage(
      { id },
      { expected_message_uid: uid },
      { ...client, signal },
    ),
  );
}
