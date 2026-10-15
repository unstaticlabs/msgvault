import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import type { ExploreURLState } from '../../explore/models';
import SavedViewsWorkspace from './SavedViewsWorkspace.svelte';

const currentState: ExploreURLState = {
  schemaVersion: 2,
  workspace: 'everything',
  directoryQuery: '', directoryContactState: '', directoryCategory: '', directoryOrganization: '',
  directoryPrimaryChannel: '', directoryLastContactAfter: '', directoryLastContactBefore: '', directorySort: 'name', directoryPersonID: null,
  reviewKind: 'identity', identityState: 'candidate', relationshipReviewState: 'pending',
  query: 'invoice',
  searchMode: 'full_text',
  filters: [{ dimension: 'source', values: ['1'] }],
  groupingChain: ['domain'],
  presentation: 'table',
  sort: [{ field: 'occurred_at', direction: 'desc' }],
  fileFilenameQuery: '', fileMIMEFamilies: [], personFilePresentation: 'files',
  personFileDirections: ['from_person'], columns: ['kind', 'title'], columnWidths: {},
  activeRow: null, selectedRow: null, inspectorPinned: true, inspectorWidth: 380,
  conversationAnchor: null, scrollAnchor: null,
  relationshipFacet: 'people', relationshipTarget: null,
  relationshipShowAll: false, relationshipFiles: false,
  operationLane: '', operationKind: '', operationState: '',
  operationStartedFrom: '', operationStartedBefore: '', operationRunID: null, operationStatus: '',
  settingsAuthority: ''
};

function savedView(overrides: Record<string, unknown> = {}) {
  return {
    id: 7, name: 'Invoices', description: 'Quarterly review',
    canonical_state: {
      query: 'invoice', search_mode: 'full_text',
      filters: [{ field: 'source', operator: 'in', values: ['1'] }],
      grouping: ['domain'], presentation: 'table',
      sort: [{ field: 'occurred_at', direction: 'desc' }],
      columns: ['kind', 'title'], inspector_pinned: true
    },
    schema_version: 1, revision: 3,
    created_at: '2026-07-19T10:00:00Z', updated_at: '2026-07-19T11:00:00Z',
    ...overrides
  };
}

describe('SavedViewsWorkspace', () => {
  it('creates from canonical analytical state without persisting selection tokens', async () => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      requests.push(request);
      if (request.method === 'GET') return Response.json({ saved_views: [] });
      return Response.json(savedView(), { status: 201 });
    });
    render(SavedViewsWorkspace, {
      client: createAPIClient(fetchFn), currentState,
      selection: { mode: 'all_matching', operationToken: 'session-secret' }
    });

    await screen.findByText('No Saved Views yet');
    await fireEvent.input(screen.getByLabelText('Name'), { target: { value: 'Invoices' } });
    await fireEvent.input(screen.getByLabelText('Description'), { target: { value: 'Quarterly review' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await screen.findByRole('heading', { name: 'Invoices' });
    const body = await requests[1]!.clone().json();
    expect(body).toEqual({
      name: 'Invoices', description: 'Quarterly review', schema_version: 1,
      canonical_state: {
        query: 'invoice', search_mode: 'full_text',
        filters: [{ field: 'source', operator: 'in', values: ['1'] }],
        grouping: ['domain'], presentation: 'table',
        sort: [{ field: 'occurred_at', direction: 'desc' }],
        columns: ['kind', 'title']
      }
    });
    expect(JSON.stringify(body)).not.toContain('session-secret');
    expect(JSON.stringify(body)).not.toContain('selection');
    expect(JSON.stringify(body)).not.toContain('inspector_pinned');
  });

  it('opens by replacing the exact analytical URL state and clears transient focus', async () => {
    const onOpen = vi.fn();
    render(SavedViewsWorkspace, {
      client: createAPIClient(vi.fn<typeof fetch>(async () => Response.json({ saved_views: [savedView()] }))),
      currentState: { ...currentState, activeRow: 'message:9', selectedRow: 'message:9' }, onOpen
    });

    await fireEvent.click(await screen.findByRole('button', { name: 'Open Invoices' }));

    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({
      workspace: 'everything', query: 'invoice', searchMode: 'full_text',
      filters: [{ dimension: 'source', values: ['1'] }], groupingChain: ['domain'],
      activeRow: null, selectedRow: null, scrollAnchor: null
    }));
    expect(onOpen.mock.calls[0]![0]).not.toHaveProperty('selection');
    expect(onOpen.mock.calls[0]![0]).not.toHaveProperty('inspectorPinned');
  });

  it('translates persisted v1 source identifiers and equality operators into current filters', async () => {
    const onOpen = vi.fn();
    const legacy = savedView({
      canonical_state: {
        query: 'invoice', search_mode: 'full_text',
        filters: [{ field: 'source_id', operator: 'eq', values: ['1'] }],
        grouping: [], presentation: 'table',
        sort: [{ field: 'occurred_at', direction: 'desc' }],
        columns: ['kind', 'title'], inspector_pinned: false
      }
    });
    render(SavedViewsWorkspace, {
      client: createAPIClient(vi.fn<typeof fetch>(async () => Response.json({ saved_views: [legacy] }))),
      currentState, onOpen
    });

    await fireEvent.click(await screen.findByRole('button', { name: 'Open Invoices' }));

    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({
      filters: [{ dimension: 'source', values: ['1'] }]
    }));
  });

  it.each(['mailing_list', 'identity'])('opens views filtered by %s', async (dimension) => {
    const onOpen = vi.fn();
    const view = savedView({
      canonical_state: {
        query: 'invoice', search_mode: 'full_text',
        filters: dimension === 'identity' ? [{ field: 'source', operator: 'eq', values: ['1'] }, { field: 'identity', operator: 'in', values: ['1', 'person@example.com', 'sender'] }] : [{ field: dimension, operator: 'in', values: ['list-7'] }],
        grouping: [], presentation: 'table',
        sort: [{ field: 'occurred_at', direction: 'desc' }],
        columns: ['kind', 'title'], inspector_pinned: false
      }
    });
    render(SavedViewsWorkspace, {
      client: createAPIClient(vi.fn<typeof fetch>(async () => Response.json({ saved_views: [view] }))),
      currentState, onOpen
    });

    const openButton = (await screen.findByRole('button', { name: 'Open Invoices' })) as HTMLButtonElement;
    expect(openButton.disabled).toBe(false);
    expect(screen.queryByRole('alert')).toBeNull();

    await fireEvent.click(openButton);

    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({
      filters: dimension === 'identity' ? [{ dimension: 'source', values: ['1'] }, { dimension: 'identity', values: ['1', 'person@example.com', 'sender'] }] : [{ dimension, values: ['list-7'] }]
    }));
  });

  it('keeps compatible-schema views with unsupported fields or operators visibly blocked', async () => {
    render(SavedViewsWorkspace, {
      client: createAPIClient(vi.fn<typeof fetch>(async () => Response.json({ saved_views: [savedView({
        canonical_state: {
          filters: [{ field: 'subject', operator: 'contains', values: ['invoice'] }],
          presentation: 'table'
        }
      })] }))),
      currentState
    });

    expect((await screen.findByRole('alert')).textContent).toContain('unsupported v1 filter');
    expect((screen.getByRole('button', { name: 'Open Invoices' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('updates with optimistic revision truth and explicitly confirms delete', async () => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      requests.push(request);
      if (request.method === 'GET') return Response.json({ saved_views: [savedView()] });
      if (request.method === 'PATCH') return Response.json(savedView({ name: 'Invoices 2026', revision: 4 }));
      return new Response(null, { status: 204 });
    });
    render(SavedViewsWorkspace, { client: createAPIClient(fetchFn), currentState });

    await fireEvent.click(await screen.findByRole('button', { name: 'Edit Invoices' }));
    await fireEvent.input(screen.getByLabelText('Edit name'), { target: { value: 'Invoices 2026' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await screen.findByRole('heading', { name: 'Invoices 2026' });
    expect(requests[1]!.headers.get('If-Match')).toBe('"saved-view-7-r3"');

    await fireEvent.click(screen.getByRole('button', { name: 'Delete Invoices 2026' }));
    expect(screen.getByRole('dialog', { name: 'Delete Saved View?' })).toBeDefined();
    expect(requests).toHaveLength(2);
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm delete' }));
    await waitFor(() => expect(requests).toHaveLength(3));
    expect(requests[2]!.headers.get('If-Match')).toBe('"saved-view-7-r4"');
    expect(await screen.findByText('No Saved Views yet')).toBeDefined();
  });

  it('keeps incompatible schema records visible and offers confirmed removal, not migration', async () => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      requests.push(request);
      if (request.method === 'DELETE') return new Response(null, { status: 204 });
      return Response.json({ saved_views: [savedView({ schema_version: 99 })] });
    });
    render(SavedViewsWorkspace, {
      client: createAPIClient(fetchFn),
      currentState
    });

    expect((await screen.findByRole('alert')).textContent).toContain('schema version 99');
    expect(screen.getByRole('alert').textContent).toContain('Automatic migration is not supported');
    expect((screen.getByRole('button', { name: 'Open Invoices' }) as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.click(screen.getByRole('button', { name: 'Remove incompatible Invoices' }));
    expect(screen.getByRole('dialog', { name: 'Delete Saved View?' })).toBeDefined();
    expect(requests).toHaveLength(1);
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm delete' }));
    await waitFor(() => expect(requests).toHaveLength(2));
    expect(requests[1]!.method).toBe('DELETE');
  });
});

describe('native executable filter compatibility', () => {
  it.each(['participant', 'participant_id'])('keeps legacy email-valued %s definitions visible for recreation', async (field) => {
    const onOpen = vi.fn();
    const legacy = savedView({ canonical_state: { filters: [{ field, operator: 'eq', values: ['person@example.com'] }] } });
    const original = structuredClone(legacy);
    const requests: Request[] = [];
    render(SavedViewsWorkspace, {
      client: createAPIClient(vi.fn<typeof fetch>(async (input) => {
        requests.push(input instanceof Request ? input : new Request(input));
        return Response.json({ saved_views: [legacy] });
      })), currentState, onOpen
    });
    const open = await screen.findByRole('button', { name: 'Open Invoices' });
    expect((open as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByRole('alert').textContent).toContain('Select the people again in Explore and save a new view');
    expect(screen.getByRole('button', { name: 'Remove incompatible Invoices' })).toBeTruthy();
    expect(onOpen).not.toHaveBeenCalled();
    expect(requests.map((request) => request.method)).toEqual(['GET']);
    expect(legacy).toEqual(original);
    expect(legacy.revision).toBe(3);
  });

  it('opens a matching-source identity with the native empty direction', async () => {
    const onOpen = vi.fn();
    const view = savedView({ canonical_state: { filters: [
      { field: 'source', operator: 'eq', values: ['1'] },
      { field: 'identity', operator: 'eq', values: ['1', 'person@example.com', ''] }
    ] } });
    render(SavedViewsWorkspace, {
      client: createAPIClient(vi.fn<typeof fetch>(async () => Response.json({ saved_views: [view] }))), currentState, onOpen
    });
    const open = await screen.findByRole('button', { name: 'Open Invoices' });
    expect((open as HTMLButtonElement).disabled).toBe(false);
    await fireEvent.click(open);
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ filters: [
      { dimension: 'source', values: ['1'] },
      { dimension: 'identity', values: ['1', 'person@example.com', ''] }
    ] }));
  });
});

describe('legacy date and deletion compatibility', () => {
  it.each([
    ['after', ['yesterday']], ['before', ['2026-10-14T00:00:00Z', '2026-10-15T00:00:00Z']],
    ['after', ['2026-02-30T00:00:00Z']], ['before', ['2026-10-14T00:00:00']],
    ['deletion', ['maybe']], ['deletion', ['active', 'deleted']]
  ])('shows an actionable incompatible %s filter', async (field, values) => {
    const view = savedView({ canonical_state: { filters: [{ field, operator: 'in', values }] } });
    const fetchFn = vi.fn<typeof fetch>(async () => Response.json({ saved_views: [view] }));
    const onOpen = vi.fn();
    render(SavedViewsWorkspace, { client: createAPIClient(fetchFn), currentState, onOpen });
    const open = await screen.findByRole('button', { name: 'Open Invoices' });
    expect((open as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByRole('alert').textContent).toContain('save a new view. Your saved definition is unchanged');
    expect(onOpen).not.toHaveBeenCalled();
    expect(fetchFn).toHaveBeenCalledTimes(1);
    expect(view.revision).toBe(3);
  });
  it.each(['2024-02-29T01:02:03Z', '2026-10-14T01:02:03.123456789+05:30', '2026-10-14T01:02:03-11:00'])('keeps native valid timestamp %s executable', async (value) => {
    const view = savedView({ canonical_state: { filters: [{ field: 'after', operator: 'eq', values: [value] }] } });
    render(SavedViewsWorkspace, { client: createAPIClient(vi.fn<typeof fetch>(async () => Response.json({ saved_views: [view] }))), currentState });
    const open = await screen.findByRole('button', { name: 'Open Invoices' });
    expect((open as HTMLButtonElement).disabled).toBe(false);
  });
});

describe('explicit legacy filter repair', () => {
  it('shows replacement filters and preserves the definition until revision-guarded Save', async () => {
    const legacy = savedView({ canonical_state: { query: 'invoice', presentation: 'table', filters: [{ field: 'participant', operator: 'eq', values: ['person@example.com'] }] } });
    const original = structuredClone(legacy);
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      requests.push(request);
      if (request.method === 'GET') return Response.json({ saved_views: [legacy] });
      const patch = await request.clone().json();
      return Response.json({ ...legacy, canonical_state: patch.canonical_state, revision: 4 });
    });
    render(SavedViewsWorkspace, { client: createAPIClient(fetchFn), currentState });
    await fireEvent.click(await screen.findByRole('button', { name: 'Repair filters for Invoices' }));
    expect(screen.getByRole('list', { name: 'Replacement filters' }).textContent).toContain('source');
    expect(requests.map((request) => request.method)).toEqual(['GET']);
    expect(legacy).toEqual(original);
    await fireEvent.click(screen.getByRole('button', { name: 'Save repaired filters' }));
    await waitFor(() => expect(requests).toHaveLength(2));
    expect(requests[1]!.method).toBe('PATCH');
    expect(requests[1]!.headers.get('If-Match')).toBe('"saved-view-7-r3"');
    expect((await requests[1]!.clone().json()).canonical_state).toEqual({
      ...legacy.canonical_state, filters: [{ field: 'source', operator: 'in', values: ['1'] }]
    });
    expect((await screen.findByRole('button', { name: 'Open Invoices' }) as HTMLButtonElement).disabled).toBe(false);
    expect(legacy).toEqual(original);
  });
});
