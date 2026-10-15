import { describe, expect, it } from "vitest";
import {
  emailWorkFilterProblem,
  isEmailWork,
  withEmailWork,
  withHandlingState,
} from "./email-work";
import { filterCaption } from "./mailbox-state";
import type { ExploreFilter } from "./models";

describe("related email context", () => {
  const scope: ExploreFilter[] = [
    { dimension: "source", values: ["1", "2", "3"] },
    { dimension: "inbox_state", values: ["inbox"] },
    { dimension: "read_state", values: ["unread"] },
  ];
  it("enables grouping without changing source, Inbox or read witnesses", () => {
    const grouped = withEmailWork(scope, true);
    expect(grouped).toEqual([
      ...scope,
      { dimension: "row_unit", values: ["email_work_item"] },
    ]);
    expect(isEmailWork(grouped)).toBe(true);
    expect(withEmailWork(grouped, true)).toEqual(grouped);
    expect(withHandlingState(grouped, "handled")).toEqual([
      ...grouped,
      { dimension: "handling_state", values: ["handled"] },
    ]);
  });
  it("removes dependent handling status when returning to individual messages", () => {
    expect(
      withEmailWork(withHandlingState(scope, "actionable"), false),
    ).toEqual(scope);
  });
  it("rejects unsupported units and unpaired handling without changing definitions", () => {
    expect(
      emailWorkFilterProblem([
        { dimension: "handling_state", values: ["handled"] },
      ]),
    ).toContain("requires");
    expect(
      emailWorkFilterProblem([{ dimension: "row_unit", values: ["message"] }]),
    ).toContain("Choose");
    expect(
      emailWorkFilterProblem([
        ...withEmailWork(scope, true),
        { dimension: "handling_state", values: ["read"] },
      ]),
    ).toContain("recognized");
    expect(
      emailWorkFilterProblem([
        ...withEmailWork(scope, true),
        { dimension: "handling_state", values: ["actionable", "unknown"] },
      ]),
    ).toBeUndefined();
    expect(
      filterCaption({
        dimension: "handling_state",
        values: ["actionable", "unknown"],
      }),
    ).toBe("Actionable or Handling unknown");
  });
});
