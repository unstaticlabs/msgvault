<script lang="ts">
  import XIcon from '@lucide/svelte/icons/x';
  import { Button, Checkbox, IconButton, SelectDropdown } from '@kenn-io/kit-ui';
  import type { Snippet } from 'svelte';

  import type { APIClient } from '../../api/client';
  import type { ExploreFilter, ExploreGroupDimension, ExploreSearchMode, ExploreURLState } from '../../explore/models';
  import { filterDimensionLabel, filterValueLabel, groupedByLabel, searchModeLabel } from '../../explore/labels';
  import { groupingOptions, isGroupingDimension } from '../../grouping/catalog';
  import AccountFilter from './AccountFilter.svelte';
  import { isEmailWork, withEmailWork, withHandlingState, HANDLING_LABELS } from '../../explore/email-work';
  import IdentityFilter from './IdentityFilter.svelte';
 import { filterCaption, mailboxFilterOptions, mailboxFilterValue, withMailboxFilter } from '../../explore/mailbox-state';

  interface ExtraChip {
    key: string;
    label: string;
    removeLabel: string;
    onRemove: () => void;
  }

  interface SortConfig {
    options: { value: string; label: string }[];
    value: string;
    note?: string;
    onchange?: (value: string) => void;
  }

  let {
    client,
    query,
    searchMode,
    filters,
    groupingChain,
    countLabel,
    sort = undefined,
    presentation = 'table',
    extra = undefined,
    extraChips = [],
    onAddGroup,
    onRemoveGroup,
    onClearFilters,
    onFiltersChange,
    onRemoveQuery = undefined,
    onRemoveFilter = undefined,
    onPresentationChange = undefined
  }: {
    client: APIClient;
    query: string;
    searchMode: ExploreSearchMode;
    filters: ExploreFilter[];
    groupingChain: ExploreGroupDimension[];
    countLabel: string;
    sort?: SortConfig;
    presentation?: ExploreURLState['presentation'];
    extra?: Snippet;
    extraChips?: ExtraChip[];
    onAddGroup: (dimension: ExploreGroupDimension) => void;
    onRemoveGroup: (index: number) => void;
    onClearFilters: () => void;
    onFiltersChange: (filters: ExploreFilter[]) => void;
    onRemoveQuery?: () => void;
    onRemoveFilter?: (index: number) => void;
    onPresentationChange?: (presentation: ExploreURLState['presentation']) => void;
  } = $props();

  let filtersOpen = $state(false);
  const handlingValues = $derived(filters.find((filter) => filter.dimension === 'handling_state')?.values ?? []);
  const handlingValue = $derived(handlingValues.length === 0 ? '' : handlingValues.length === 1 ? handlingValues[0] : handlingValues.length === 2 && handlingValues.includes('actionable') && handlingValues.includes('unknown') ? 'needs_attention' : 'multiple');
  const handlingOptions = $derived([{value: '', label: 'All handling states'}, ...Object.entries(HANDLING_LABELS).map(([value, label]) => ({value, label})), {value: 'needs_attention', label: 'Actionable or unknown'}, ...(handlingValue === 'multiple' ? [{value: 'multiple', label: 'Multiple handling states', disabled: true}] : [])]);
  const groupOptions = $derived(groupingOptions({ excluded: groupingChain, includeUnavailable: true }));
  const canGroup = $derived(groupOptions.some((option) => !option.disabled));
  // The triggers carry their own names (no kit `title`), so the visible text says what each control is.
  const options = $derived([
    {
      value: '',
      label: groupingChain.length > 0 ? 'Add grouping' : 'None',
      triggerLabel: groupingChain.length > 0 ? 'Add grouping' : 'Group by',
      disabled: true
    },
    ...groupOptions
  ]);
  const presentationOptions = [
    { value: 'table', label: 'Table', triggerLabel: 'Show as: Table' },
    { value: 'timeline', label: 'Timeline', triggerLabel: 'Show as: Timeline' },
    { value: 'files', label: 'Files', triggerLabel: 'Show as: Files' }
  ];
  const sortOptions = $derived([
    ...(sort?.options ?? []).map((option) => ({ ...option, triggerLabel: `Sort: ${option.label}` })),
    ...(sort?.note ? [{ value: '__note', label: sort.note, disabled: true }] : [])
  ]);
  const hasChips = $derived(
    Boolean(query) || filters.length > 0 || extraChips.length > 0 || groupingChain.length > 0
  );

  function selectGrouping(value: string): void {
    if (isGroupingDimension(value)) onAddGroup(value);
  }
</script>

<section class="context-bar" aria-label="Active analytical context">
  <div class="context-controls">
    <Checkbox checked={isEmailWork(filters)} label="Group related email"
      onchange={(enabled) => onFiltersChange(withEmailWork(filters, enabled))} />
    {#if isEmailWork(filters)}
      <SelectDropdown title="Handling" value={handlingValue} options={handlingOptions}
        onchange={(value) => onFiltersChange(value === 'needs_attention' ? [...withHandlingState(filters, ''), {dimension: 'handling_state', values: ['actionable', 'unknown']}] : withHandlingState(filters, value))} />
    {/if}
    <Button
      size="sm"
      surface={filtersOpen || filters.length > 0 ? 'soft' : 'outline'}
      label="Filters"
      ariaLabel="Filters"
      ariaExpanded={filtersOpen}
      onclick={() => { filtersOpen = !filtersOpen; }}
    />
    <SelectDropdown
      value={presentation}
      options={presentationOptions}
      onchange={(value) => onPresentationChange?.(value as ExploreURLState['presentation'])}
    />
    <div class="group-picker" data-group-picker>
      <SelectDropdown
        value=""
        {options}
        disabled={!canGroup}
        onchange={selectGrouping}
      />
    </div>
    {#if sort}
      <div data-sort-menu>
        <SelectDropdown
          value={sort.value}
          options={sortOptions}
          onchange={(value) => sort.onchange?.(value)}
        />
      </div>
    {/if}
    {@render extra?.()}
    <span class="context-count" aria-live="polite" data-mono>{countLabel}</span>
  </div>

  {#if hasChips}
    <div class="context-chips">
      {#if query}
        <span class="chip">
          {searchModeLabel(searchMode)}: “{query}”
          <IconButton size="sm" ariaLabel="Remove search" onclick={() => onRemoveQuery?.()}
          ><XIcon size="12" aria-hidden="true" /></IconButton>
        </span>
      {/if}
      {#each filters as filter, index (`${filter.dimension}:${filter.values.join('\u0000')}`)}
        <span class="chip chip--filter">
          {#if ['inbox_state', 'read_state', 'row_unit', 'handling_state'].includes(filter.dimension)}{filterCaption(filter)}{:else}{filterDimensionLabel(filter.dimension)}: {filter.values.map((value) => filterValueLabel(filter.dimension, value)).join(', ')}{/if}
          <IconButton
            size="sm"
            ariaLabel={`Remove ${filterDimensionLabel(filter.dimension)} filter`}
            onclick={() => onRemoveFilter?.(index)}
          ><XIcon size="12" aria-hidden="true" /></IconButton>
        </span>
      {/each}
      {#each extraChips as chip (chip.key)}
        <span class="chip chip--filter">
          {chip.label}
          <IconButton size="sm" ariaLabel={chip.removeLabel} onclick={chip.onRemove}
          ><XIcon size="12" aria-hidden="true" /></IconButton>
        </span>
      {/each}
      {#each groupingChain as dimension, index (`${dimension}:${index}`)}
        <span class="chip chip--group">
          {groupedByLabel([dimension])}
          <IconButton
            size="sm"
            ariaLabel={`Remove ${filterDimensionLabel(dimension)} grouping`}
            onclick={() => onRemoveGroup(index)}
          ><XIcon size="12" aria-hidden="true" /></IconButton>
        </span>
      {/each}
    </div>
  {/if}

  {#if filtersOpen}
    <div class="filter-panel">
      <div class="filter-summary">
        {#if filters.length === 0}
          <span>No filters applied.</span>
        {:else}
          <span>{filters.length} active {filters.length === 1 ? 'filter' : 'filters'}</span>
          <Button size="sm" surface="outline" label="Clear filters" onclick={onClearFilters} />
        {/if}
      </div>
      <AccountFilter {client} {filters} onChange={onFiltersChange} />
      <div class="mailbox-filters">
        <SelectDropdown title="Inbox" value={mailboxFilterValue(filters, 'inbox_state')}
          options={mailboxFilterOptions(filters, 'inbox_state')}
          onchange={(value) => onFiltersChange(withMailboxFilter(filters, 'inbox_state', value))} />
        <SelectDropdown title="Read status" value={mailboxFilterValue(filters, 'read_state')}
          options={mailboxFilterOptions(filters, 'read_state')}
          onchange={(value) => onFiltersChange(withMailboxFilter(filters, 'read_state', value))} />
      </div>
      {#if isEmailWork(filters)}<p class="mailbox-note">Related email is grouped within the selected accounts. Handling uses reply evidence and local confirmations; read and Inbox status remain separate.</p>{/if}
      <p class="mailbox-note">Mailbox status reflects saved provider data. Sync to refresh it. Unknown status stays separate.</p>
      <IdentityFilter {client} {filters} onChange={onFiltersChange} />
    </div>
  {/if}
</section>

<style>
  .context-bar {
    position: relative;
    display: flex;
    min-width: 0;
    flex-direction: column;
    gap: var(--space-2);
    padding: var(--space-2) var(--space-3);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-surface);
    font-size: var(--font-size-xs);
  }

  .context-controls,
  .context-chips {
    display: flex;
    min-width: 0;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
  }

  .group-picker {
    width: 172px;
  }

  .chip {
    display: inline-flex;
    max-width: 100%;
    align-items: center;
    gap: var(--space-1);
    padding: 0 0 0 var(--space-2);
    border-radius: var(--radius-sm);
    background: var(--bg-inset);
    color: var(--text-secondary);
    overflow-wrap: anywhere;
  }

  .chip--filter {
    border: 1px solid color-mix(in srgb, var(--accent-amber) 35%, var(--border-muted));
    background: color-mix(in srgb, var(--accent-amber) 8%, var(--bg-surface));
  }

  .chip--group {
    border: 1px solid color-mix(in srgb, var(--accent-teal) 35%, var(--border-muted));
    background: color-mix(in srgb, var(--accent-teal) 8%, var(--bg-surface));
  }

  .context-count {
    margin-left: auto;
    color: var(--text-muted);
    white-space: nowrap;
  }

  .filter-panel {
    position: absolute;
    z-index: var(--z-popover);
    top: calc(100% + var(--space-2));
    left: 0;
    display: flex;
    min-width: min(320px, calc(100vw - 2 * var(--space-4)));
    max-width: calc(100vw - 2 * var(--space-4));
    align-items: stretch;
    flex-direction: column;
    gap: var(--space-4);
    padding: var(--space-4);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-surface);
    color: var(--text-muted);
    box-shadow: var(--shadow-md);
  }

  .mailbox-filters {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
  }
  .mailbox-note {
    max-width: 40ch;
    margin: 0;
    color: var(--text-secondary);
    line-height: 1.5;
  }
  .filter-summary {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-4);
  }
  @media (max-width: 640px) {
    .context-bar { flex-wrap: wrap; }
    .context-controls { flex: 1 1 100%; flex-wrap: wrap; }
    .context-chips { flex: 1 1 calc(100% - 80px); flex-wrap: wrap; overflow: visible; }
    .context-count { margin-left: auto; }
  }
</style>
