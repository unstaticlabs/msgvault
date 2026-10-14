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
