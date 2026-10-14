import type { ExploreFilter } from "./models";
export const HANDLING_LABELS: Record<string, string> = {
  actionable: "Actionable",
  handled: "Handled",
  unknown: "Handling unknown",
};
export function isEmailWork(filters: ExploreFilter[]): boolean {
  return filters.some(
    (filter) =>
      filter.dimension === "row_unit" &&
      filter.values.length === 1 &&
      filter.values[0] === "email_work_item",
  );
}
export function withEmailWork(
  filters: ExploreFilter[],
  enabled: boolean,
): ExploreFilter[] {
  const remaining = filters.filter(
    (filter) =>
      filter.dimension !== "row_unit" &&
      (enabled || filter.dimension !== "handling_state"),
  );
  return enabled
    ? [...remaining, { dimension: "row_unit", values: ["email_work_item"] }]
    : remaining;
}
export function withHandlingState(
  filters: ExploreFilter[],
  value: string,
): ExploreFilter[] {
  const remaining = filters.filter(
    (filter) => filter.dimension !== "handling_state",
  );
  return value
    ? [
        ...withEmailWork(remaining, true),
        { dimension: "handling_state", values: [value] },
      ]
    : remaining;
}
export function emailWorkFilterProblem(
  filters: Array<{ dimension: string; values: string[] }>,
): string | undefined {
  const units = filters.filter((filter) => filter.dimension === "row_unit");
  if (
    units.length &&
    (units.length !== 1 ||
      units[0].values.length !== 1 ||
      units[0].values[0] !== "email_work_item")
  )
    return "Choose Group related email again before saving this view.";
  const handling = filters.filter(
    (filter) => filter.dimension === "handling_state",
  );
  if (
    handling.some(
      (filter) =>
        !filter.values.length ||
        filter.values.some((value) => !(value in HANDLING_LABELS)),
    )
  )
    return "Choose a recognized handling status before saving this view.";
  if (handling.length && units.length !== 1)
    return "Handling status requires Group related email. Your saved definition is unchanged.";
}

const EVIDENCE_LABELS: Record<string, string> = {
  identity_unavailable: "Account ownership or message direction cannot be confirmed from the archive.",
  linkage_unavailable: "Some archived copies lack reliable message-link evidence.",
  provider_evidence_unavailable: "Saved provider metadata is incomplete for this copy.",
  chronology_unavailable: "Saved message dates do not establish reply order.",
  no_received_item: "No confirmed received request is present in these accounts.",
  local_confirmation: "Handling includes an explicit, undoable local confirmation.",
};
export function emailWorkReasonLabel(reason: string): string {
  return EVIDENCE_LABELS[reason] ?? "Some archived evidence is unavailable; inspect the retained copies before deciding.";
}
export function emailWorkExtractionLabel(outcome: string): string {
  if (outcome === "complete") return "Archived headers prepared";
  if (outcome === "missing_raw") return "Raw email was not archived";
  if (outcome === "pending_extraction") return "Archived headers have not been prepared yet";
  if (outcome === "unsupported_raw") return "The archived format does not provide email headers";
  if (outcome.endsWith("_limit")) return "This email exceeds the evidence processing limit; its copy remains available";
  return "Archived message-link evidence could not be read";
}
export function emailWorkActorLabel(kind: string, userID?: number): string {
  const labels: Record<string, string> = {api_key: "API user", user: "User", browser: "Signed-in user", session: "Signed-in user", service: "Service user"};
  if (kind === "api_key" && !userID) return "API key";
  const label = labels[kind] ?? "Recorded actor";
  return userID ? `${label} #${userID}` : label;
}
