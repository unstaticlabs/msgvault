import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import { ExploreGroupDimension } from '../../api/generated/models';
import { defaultExploreURLState, parseExploreURLState, serializeExploreURLState } from '../../explore/state.svelte';
import SavedViewsWorkspace from './SavedViewsWorkspace.svelte';
const currentState = { ...defaultExploreURLState, query: 'invoice', filters: [{dimension: 'source' as const, values: ['1']}], groupingChain: ['domain' as const], columns: ['kind' as const, 'title' as const] };

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
  it('is a library: no save form, and an empty state that points to Save view…', async () => {
    render(SavedViewsWorkspace, {
      client: createAPIClient(vi.fn<typeof fetch>(async () => Response.json({ saved_views: [] })))
    });
    await screen.findByText('No saved views yet');
    expect(screen.getByText(
      'Use Save view… in Everything or Files to keep a search and layout you want to return to.'
    )).toBeDefined();
    expect(screen.queryByText('Save this view')).toBeNull();
    expect(screen.queryByLabelText('Name')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
  });

  it('shows each view with its description, a readable summary, and its actions', async () => {
    render(SavedViewsWorkspace, {
      client: createAPIClient(vi.fn<typeof fetch>(async () => Response.json({ saved_views: [savedView()] })))
    });
    const heading = await screen.findByRole('heading', { name: 'Invoices' });
    const card = heading.closest('article')!;
    expect(within(card).getByText('Quarterly review')).toBeDefined();
    const summary = within(card).getByRole('list', { name: 'Invoices summary' });
    expect(within(summary).getAllByRole('listitem').map((item) => item.textContent?.trim())).toEqual([
      'Full text: “invoice”', 'Source: 1', 'Grouped by Domain', 'Table'
    ]);
    for (const name of ['Open Invoices', 'Edit Invoices', 'Delete Invoices']) {
      expect(within(card).getByRole('button', { name })).toBeDefined();
    }
  });

  it('opens a saved Files view in Files', async () => {
    const onOpen = vi.fn();
    render(SavedViewsWorkspace, {
      client: createAPIClient(vi.fn<typeof fetch>(async () => Response.json({ saved_views: [savedView({
        canonical_state: { ...savedView().canonical_state, grouping: [], presentation: 'files' }
      })] }))),
      onOpen
    });
    await fireEvent.click(await screen.findByRole('button', { name: 'Open Invoices' }));
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ workspace: 'files', presentation: 'files' }));
  });

  it('blocks opening a definition the server marks incompatible', async () => {
    const onOpen = vi.fn();
    render(SavedViewsWorkspace, {
      client: createAPIClient(vi.fn<typeof fetch>(async () => Response.json({ saved_views: [savedView({
        canonical_state: { search_mode: 'semantic' },
        incompatibility_reason: 'Semantic and hybrid exploration require free text'
      })] }))), onOpen
    });
    expect((await screen.findByRole('alert')).textContent).toContain('require free text');
    const open = screen.getByRole('button', { name: 'Open Invoices' }) as HTMLButtonElement;
    expect(open.disabled).toBe(true);
    await fireEvent.click(open);
    expect(onOpen).not.toHaveBeenCalled();
  });

  it.each(Object.values(ExploreGroupDimension))('opens %s grouping and preserves it in the analytical URL state', async (dimension) => {
    const onOpen = vi.fn();
    render(SavedViewsWorkspace, {
      client: createAPIClient(vi.fn<typeof fetch>(async () => Response.json({ saved_views: [savedView({
        canonical_state: { ...savedView().canonical_state, grouping: [dimension] }
      })] }))),
      onOpen
    });

    await fireEvent.click(await screen.findByRole('button', { name: 'Open Invoices' }));

    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({
      workspace: 'everything', query: 'invoice', searchMode: 'full_text',
      filters: [{ dimension: 'source', values: ['1'] }], groupingChain: [dimension],
      activeRow: null, selectedRow: null, scrollAnchor: null
    }));
    expect(onOpen.mock.calls[0]![0]).not.toHaveProperty('selection');
    expect(onOpen.mock.calls[0]![0]).not.toHaveProperty('inspectorPinned');
    const restored = parseExploreURLState(serializeExploreURLState({
      ...defaultExploreURLState, ...onOpen.mock.calls[0]![0]
    }));
    expect(restored.groupingChain).toEqual([dimension]);
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
      onOpen
    });

    await fireEvent.click(await screen.findByRole('button', { name: 'Open Invoices' }));

    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({
      filters: [{ dimension: 'source', values: ['1'] }]
    }));
  });

  it('opens views filtered by every Explore dimension the daemon executes', async () => {
    const onOpen = vi.fn();
    const view = savedView({
      canonical_state: {
        filters: [
          { field: 'source', operator: 'in', values: ['1'] },
          { field: 'identity', operator: 'in', values: ['1', 'me@example.com', 'sender'] },
          { field: 'mailing_list', operator: 'eq', values: ['<dev@example.test>'] },
          { field: 'participant_id', operator: 'in', values: ['42'] }
        ],
        presentation: 'table'
      }
    });
    render(SavedViewsWorkspace, {
      client: createAPIClient(vi.fn<typeof fetch>(async () => Response.json({ saved_views: [view] }))),
      onOpen
    });

    await fireEvent.click(await screen.findByRole('button', { name: 'Open Invoices' }));

    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({
      filters: [
        { dimension: 'source', values: ['1'] },
        { dimension: 'identity', values: ['1', 'me@example.com', 'sender'] },
        { dimension: 'mailing_list', values: ['<dev@example.test>'] },
        { dimension: 'participant', values: ['42'] }
      ]
    }));
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
    render(SavedViewsWorkspace, { client: createAPIClient(fetchFn) });

    await fireEvent.click(await screen.findByRole('button', { name: 'Edit Invoices' }));
    await fireEvent.input(screen.getByLabelText('Edit name'), { target: { value: 'Invoices 2026' } });
    await fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await screen.findByRole('heading', { name: 'Invoices 2026' });
    expect(requests[1]!.headers.get('If-Match')).toBe('"saved-view-7-r3"');

    await fireEvent.click(screen.getByRole('button', { name: 'Delete Invoices 2026' }));
    expect(screen.getByRole('dialog', { name: 'Delete saved view?' })).toBeDefined();
    expect(requests).toHaveLength(2);
    await fireEvent.click(screen.getByRole('button', { name: 'Confirm delete' }));
    await waitFor(() => expect(requests).toHaveLength(3));
    expect(requests[2]!.headers.get('If-Match')).toBe('"saved-view-7-r4"');
    expect(await screen.findByText('No saved views yet')).toBeDefined();
  });

  it('keeps incompatible schema records visible and offers confirmed removal, not migration', async () => {
    const requests: Request[] = [];
    const fetchFn = vi.fn<typeof fetch>(async (input) => {
      const request = input instanceof Request ? input : new Request(input);
      requests.push(request);
      if (request.method === 'DELETE') return new Response(null, { status: 204 });
      return Response.json({ saved_views: [savedView({ schema_version: 99, incompatibility_reason: "unsupported schema", canonical_state: { query: { text: "future" } } })] });
    });
    render(SavedViewsWorkspace, {
      client: createAPIClient(fetchFn)
    });

    expect((await screen.findByRole('alert')).textContent).toContain('schema version 99');
    expect(screen.getByRole('alert').textContent).toContain('Automatic migration is not supported');
    expect((screen.getByRole('button', { name: 'Open Invoices' }) as HTMLButtonElement).disabled).toBe(true);
    await fireEvent.click(screen.getByRole('button', { name: 'Remove incompatible Invoices' }));
    expect(screen.getByRole('dialog', { name: 'Delete saved view?' })).toBeDefined();
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
