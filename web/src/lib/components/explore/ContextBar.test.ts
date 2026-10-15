import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

import { createAPIClient } from '../../api/client';
import { chooseSelectOption } from '../../../test/kit-ui';
import ContextBar from './ContextBar.svelte';

describe('ContextBar presentation control', () => {
  it('exposes Table, Timeline, and Files as one keyboard-operable Show-as control', async () => {
    const onPresentationChange = vi.fn();
    render(ContextBar, {
      client: createAPIClient(vi.fn<typeof fetch>()),
      query: 'pasta', searchMode: 'hybrid', filters: [], groupingChain: [],
      presentation: 'table', onPresentationChange,
      onAddGroup: vi.fn(), onRemoveGroup: vi.fn(), onClearFilters: vi.fn(),
      onFiltersChange: vi.fn()
    });

    const control = screen.getByRole('combobox', { name: 'Show as: Table' });
    await fireEvent.click(control);
    expect(screen.getAllByRole('option').map((option) => option.textContent?.trim()))
      .toEqual(['Table', 'Timeline', 'Files']);
    await fireEvent.click(screen.getByRole('option', { name: 'Timeline' }));
    expect(onPresentationChange).toHaveBeenCalledWith('timeline');
  });
});

describe('Mailbox filters', () => {
  it('adds Inbox while retaining the account union and participant context', async () => {
    const onFiltersChange = vi.fn();
    render(ContextBar, {
      client: createAPIClient(vi.fn<typeof fetch>()), query: '', searchMode: 'full_text',
      filters: [{ dimension: 'source', values: ['1', '2', '3'] }, { dimension: 'participant', values: ['7'] }],
      groupingChain: [], onAddGroup: vi.fn(), onRemoveGroup: vi.fn(), onClearFilters: vi.fn(), onFiltersChange
    });
    await fireEvent.click(screen.getByRole('button', { name: 'Filters' }));
    await chooseSelectOption(screen.getByRole('combobox', { name: 'Inbox: All inbox states' }), 'In inbox');
    expect(onFiltersChange).toHaveBeenCalledWith([
      { dimension: 'source', values: ['1', '2', '3'] }, { dimension: 'participant', values: ['7'] },
      { dimension: 'inbox_state', values: ['inbox'] }
    ]);
  });
  it('opens persisted Inbox+Unread with readable labels and clears only read status', async () => {
    const onFiltersChange = vi.fn();
    render(ContextBar, {
      client: createAPIClient(vi.fn<typeof fetch>()), query: '', searchMode: 'full_text',
      filters: [{ dimension: 'inbox_state', values: ['inbox'] }, { dimension: 'read_state', values: ['unread'] }],
      groupingChain: [], onAddGroup: vi.fn(), onRemoveGroup: vi.fn(), onClearFilters: vi.fn(), onFiltersChange
    });
    expect(screen.getByText('In inbox')).toBeTruthy();
    expect(screen.getByText('Unread')).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Filters' }));
    await chooseSelectOption(screen.getByRole('combobox', { name: 'Read status: Unread' }), 'All read states');
    expect(onFiltersChange).toHaveBeenCalledWith([{ dimension: 'inbox_state', values: ['inbox'] }]);
  });
});
