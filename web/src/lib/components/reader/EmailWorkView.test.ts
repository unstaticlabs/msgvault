import { fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { describe, expect, it, vi } from "vitest";
import { createAPIClient } from "../../api/client";
import EmailWorkView from "./EmailWorkView.svelte";
const inboundUID = "11111111111111111111111111111111";
const sentUID = "22222222222222222222222222222222";
const inbound = {
  message_id: 1,
  message_uid: inboundUID,
  source_id: 1,
  source_type: "gmail",
  source_identifier: "inbound@example.com",
  source_message_id: "qa-inbound",
  conversation_id: 10,
  occurred_at: "2026-10-14T10:00:00Z",
  title: "[QA] Delivery question",
  preview: "Synthetic question",
  inbox_state: "inbox",
  read_state: "unread",
  local_read_state: "read",
  linkage_outcome: "complete",
  labels: ["INBOX", "UNREAD"],
  native_sent: false,
  native_draft: false,
  incoming: true,
  attribution_complete: true,
  confirmed_source_sender: false,
  rfc_message_id: "<qa-root@example.com>",
};
const sent = {
  ...inbound,
  message_id: 2,
  message_uid: sentUID,
  source_id: 2,
  source_identifier: "sender@example.com",
  source_message_id: "qa-reply",
  title: "[QA] Delivery reply",
  inbox_state: "outside",
  read_state: "read",
  local_read_state: "unknown",
  labels: ["SENT"],
  native_sent: true,
  incoming: false,
  confirmed_source_sender: true,
  rfc_message_id: "<qa-reply@example.com>",
  reply_ids: ["<qa-root@example.com>"],
};
const detail = {
  uid: inboundUID,
  requested_uid: inboundUID,
  cache_revision: "qa-work-1",
  members: [inbound, sent],
  total_count: 2,
  summary: {
    uid: inboundUID,
    state: "handled",
    source_ids: [1, 2],
    outstanding_count: 0,
    evidence_count: 1,
    reasons: [],
  },
  outstanding_message_ids: [],
  evidence_message_ids: [2],
  local_actions: [],
  recovery_snapshot: "a".repeat(64),
  requested_anchor: inbound,
};
const predicate = {
  filters: [
    { dimension: "row_unit" as const, values: ["email_work_item"] },
    { dimension: "source" as const, values: ["1", "2"] },
    { dimension: "read_state" as const, values: ["unread"] },
  ],
};
function fixture(extra?: (request: Request) => Promise<Response | undefined>) {
  const fetch = vi.fn<typeof globalThis.fetch>(async (input, init) => {
    const request =
      input instanceof Request
        ? input
        : new Request(new URL(String(input), "http://localhost"), init);
    const response = await extra?.(request);
    return response ?? Response.json(detail);
  });
  return { fetch, client: createAPIClient(fetch) };
}
describe("native related email reader", () => {
  it("explains missing evidence without claiming a verified reply", async () => {
    const {client} = fixture(async () => Response.json({...detail, summary: {...detail.summary, state:'unknown', evidence_count:0, reasons:['linkage_unavailable']}, members:[{...inbound, linkage_outcome:'missing_raw', unavailable_reasons:['provider_evidence_unavailable']}], total_count:1}));
    render(EmailWorkView,{client,uid:inboundUID,predicate,cacheRevision:'qa-work-1'});
    await screen.findByText('Handling unknown in these accounts');
    expect(screen.getByText('Some archived copies lack reliable message-link evidence.')).toBeTruthy();
    expect(screen.getByText('Raw email was not archived')).toBeTruthy();
    expect(screen.getByText('Saved provider metadata is incomplete for this copy.')).toBeTruthy();
  });

  it("retains conflicting mailbox states and full provenance for a handled item", async () => {
    render(EmailWorkView, {
      client: fixture().client,
      uid: inboundUID,
      predicate,
      cacheRevision: "qa-work-1",
    });
    await screen.findByText("Handled in these accounts");
    expect(screen.getAllByText("Archive conversation ID")).toHaveLength(2);
    expect(
      screen.getByText("Unread · In inbox · Local read: read · Received"),
    ).toBeTruthy();
    expect(
      screen.getByText("Read · Outside inbox · Local read: unknown · Sent"),
    ).toBeTruthy();
    expect(screen.getByText("Completed outbound reply evidence")).toBeTruthy();
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Review handled confirmation" }),
    ).toBeNull();
  });
  it("pins body retrieval to the exact copy UID and shows a stale error without unrelated content", async () => {
    let bodyURL = "";
    const { client } = fixture(async (request) => {
      if (request.url.includes("/messages/")) {
        bodyURL = request.url;
        return Response.json(
          {
            error: "work_item_revision_changed",
            message: "This copy changed. Refresh and try again.",
          },
          { status: 409 },
        );
      }
    });
    render(EmailWorkView, {
      client,
      uid: inboundUID,
      predicate,
      cacheRevision: "qa-work-1",
    });
    await screen.findByText("Handled in these accounts");
    await fireEvent.click(
      screen.getAllByRole("button", { name: "Read copy" })[0],
    );
    await screen.findByRole("alert");
    expect(bodyURL).toContain(`expected_message_uid=${inboundUID}`);
    expect(screen.getByRole("alert").textContent).toContain(
      "This copy changed",
    );
    expect(screen.queryByRole("article", { name: "Message 1" })).toBeNull();
  });
  it("reviews fixed copies before a Member can save a local confirmation", async () => {
    const calls: Array<{ path: string; body: Record<string, unknown> }> = [];
    const { client } = fixture(async (request) => {
      const body = (await request.json()) as Record<string, unknown>;
      calls.push({ path: new URL(request.url).pathname, body });
      if (request.url.endsWith("/preview"))
        return Response.json({
          ...body,
          uid: "b".repeat(32),
          snapshot: detail.recovery_snapshot,
          messages: [inbound],
        });
      if (request.url.endsWith("/actions"))
        return Response.json({
          action: { uid: "b".repeat(32), kind: "handled" },
          cache_state: "ready",
        });
    });
    render(EmailWorkView, {
      client,
      uid: inboundUID,
      predicate,
      cacheRevision: "qa-work-1",
      canCurate: true,
      savedViewID: 7,
      savedViewRevision: 3,
    });
    await screen.findByText("Handled in these accounts");
    await fireEvent.click(
      screen.getByRole("checkbox", {
        name: "Select copy 1 from inbound@example.com",
      }),
    );
    await fireEvent.click(
      screen.getByRole("button", { name: "Review handled confirmation" }),
    );
    await screen.findByText("Confirm these exact copies were handled?");
    expect(calls.filter((call) => call.path.endsWith("/actions"))).toHaveLength(
      0,
    );
    const plan = calls.find((call) => call.path.endsWith("/preview"))!.body;
    expect(plan.targets).toEqual([{ uid: inboundUID, source_id: 1 }]);
    expect(plan.saved_view_revision).toBe(3);
    expect((plan.predicate as typeof predicate).filters).toEqual(
      predicate.filters.slice(0, 2),
    );
    await fireEvent.click(
      screen.getByRole("button", { name: "Mark these copies handled" }),
    );
    await waitFor(() =>
      expect(calls.some((call) => call.path.endsWith("/actions"))).toBe(true),
    );
    const applied = calls.find((call) => call.path.endsWith("/actions"))!.body;
    expect(applied.messages).toBeUndefined();
    expect(applied.targets).toEqual(plan.targets);
    expect(applied.snapshot).toBe(detail.recovery_snapshot);
    expect(applied.uid).toBe("b".repeat(32));
  });
  it("keeps pagination pinned and offers retry when later copies become stale", async () => {
    const { client } = fixture(async (request) => {
      const body = await request.json();
      if (body.offset > 0)
        return Response.json(
          { message: "Related email changed. Refresh before continuing." },
          { status: 409 },
        );
      return Response.json({ ...detail, members: [inbound] });
    });
    render(EmailWorkView, {
      client,
      uid: inboundUID,
      predicate,
      cacheRevision: "qa-work-1",
    });
    await screen.findByText("Handled in these accounts");
    await fireEvent.click(
      screen.getByRole("button", { name: "Load more copies" }),
    );
    await screen.findByRole("button", { name: "Retry more copies" });
    expect(
      screen.getByRole("article", {
        name: "Archived copy from inbound@example.com",
      }),
    ).toBeTruthy();
  });
  it('browses permitted candidates without inventing an empty lexical query', async () => {
    let searchBody: Record<string, unknown> | undefined;
    const {client} = fixture(async (request) => {
      if (new URL(request.url).pathname === '/api/v1/explore') {
        searchBody = await request.json();
        return Response.json({rows: [], cache_revision: 'qa-work-1', search_provenance: {}});
      }
    });
    render(EmailWorkView, {client, uid: inboundUID, predicate, cacheRevision: 'qa-work-1', canCurate: true});
    await screen.findByText('Handled in these accounts');
    await fireEvent.click(screen.getByRole('button', {name: 'Find another related item'}));
    await fireEvent.click(screen.getByRole('button', {name: 'Search permitted accounts'}));
    await screen.findByText('No other related items matched in these accounts. Try fewer words or clear the search to browse.');
    expect(searchBody?.query).toBeUndefined();
    expect(searchBody?.search_mode).toBeUndefined();
    expect(searchBody?.filters).toEqual(predicate.filters.slice(0, 2));
  });
  it('retains undone provenance without offering a second undo', async () => {
    const {client} = fixture(async () => Response.json({...detail, local_actions: [{uid: 'b'.repeat(32), kind: 'link', revision: 2, active: false, actor_kind: 'api_key', actor_user_id: 7, created_at: '2026-10-14T10:00:00Z', targets: [inboundUID, sentUID]}]}));
    render(EmailWorkView, {client, uid: inboundUID, predicate, cacheRevision: 'qa-work-1', canCurate: true});
    await screen.findByText('Handled in these accounts');
    expect(screen.getByText('· Undone')).toBeTruthy();
    expect(screen.getByText(/API user #7/)).toBeTruthy();
    expect(screen.queryByText(/api_key/)).toBeNull();
    expect(screen.queryByRole('button', {name: 'Undo local link'})).toBeNull();
  });

});
