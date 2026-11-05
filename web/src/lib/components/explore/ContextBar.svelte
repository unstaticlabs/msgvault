<script lang="ts">
  import type { Snippet } from 'svelte';
  import XIcon from '@lucide/svelte/icons/x';
  import { Button, Checkbox, Chip, IconButton, SelectDropdown } from '@kenn-io/kit-ui';

  import type { APIClient } from '../../api/client';
  import type { ExploreFilter, ExploreGroupDimension, ExploreSearchMode, ExploreURLState } from '../../explore/models';
  import { filterDimensionLabel, filterValueLabel, groupedByLabel, searchModeLabel } from '../../explore/labels';
  import { groupingOptions, isGroupingDimension } from '../../grouping/catalog';
  import AccountFilter from './AccountFilter.svelte';
  import { isEmailWork, withEmailWork, withHandlingState, HANDLING_LABELS } from '../../explore/email-work';
  import IdentityFilter from './IdentityFilter.svelte';
 import { filterCaption, mailboxFilterOptions, mailboxFilterValue, withMailboxFilter } from '../../explore/mailbox-state';
  import FilterMenu from './FilterMenu.svelte';

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
    messages = false,
    lockedFilter = undefined,
    filterExtra = undefined,
    extraControls = undefined,
    query,
    searchMode,
    filters,
    groupingChain,
    countLabel,
    sort = undefined,
    presentation = 'table',
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
    messages?: boolean;
    lockedFilter?: ExploreFilter;
    filterExtra?: Snippet;
    extraControls?: Snippet;
    query: string;
    searchMode: ExploreSearchMode;
    filters: ExploreFilter[];
    groupingChain: ExploreGroupDimension[];
    countLabel: string;
    sort?: SortConfig;
    presentation?: ExploreURLState['presentation'];
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

{#snippet mailboxFilters()}
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
{/snippet}

{#snippet messageFilterExtra()}
  {@render mailboxFilters()}
  {@render filterExtra?.()}
{/snippet}

<section class="context-bar" aria-label="Active analytical context">
  <div class="context-controls">
    <Checkbox checked={isEmailWork(filters)} label="Group related email"
      onchange={(enabled) => onFiltersChange(withEmailWork(filters, enabled))} />
    {#if isEmailWork(filters)}
      <SelectDropdown title="Handling" value={handlingValue} options={handlingOptions}
        onchange={(value) => onFiltersChange(value === 'needs_attention' ? [...withHandlingState(filters, ''), {dimension: 'handling_state', values: ['actionable', 'unknown']}] : withHandlingState(filters, value))} />
    {/if}
    {#if messages}
      <FilterMenu {client} {filters} {lockedFilter} onChange={onFiltersChange} onClear={onClearFilters} extra={messageFilterExtra} />
    {:else}
    <Button
      size="sm"
      surface={filtersOpen || filters.length > 0 ? 'soft' : 'outline'}
      label="Filters"
      ariaLabel="Filters"
      ariaExpanded={filtersOpen}
      onclick={() => { filtersOpen = !filtersOpen; }}
    />
    {/if}
    {#if !lockedFilter}
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
    {/if}
    {#if sort}
      <div data-sort-menu>
        <SelectDropdown
          value={sort.value}
          options={sortOptions}
          onchange={(value) => sort.onchange?.(value)}
        />
      </div>
    {/if}
    {@render extraControls?.()}
    <span class="context-count" aria-live="polite" data-mono>{countLabel}</span>
  </div>

  {#if hasChips}
    <div class="context-chips">
      {#if query}
        <Chip size="sm" uppercase={false} class="chip" title={`${searchModeLabel(searchMode)}: “${query}”`}>
          {searchModeLabel(searchMode)}: “{query}”
          {#snippet trailing()}
            <IconButton size="sm" ariaLabel="Remove search" onclick={() => onRemoveQuery?.()}
            ><XIcon size="12" aria-hidden="true" /></IconButton>
          {/snippet}
        </Chip>
      {/if}
      {#each filters as filter, index (`${filter.dimension}:${filter.values.join('\u0000')}`)}
        <Chip size="sm" uppercase={false} class="chip chip--filter" title={`${filterDimensionLabel(filter.dimension)}: ${filter.values.map((value) => filterValueLabel(filter.dimension, value)).join(', ')}`}>
          {#if ['inbox_state', 'read_state', 'row_unit', 'handling_state'].includes(filter.dimension)}{filterCaption(filter)}{:else}{filterDimensionLabel(filter.dimension)}: {filter.values.map((value) => filterValueLabel(filter.dimension, value)).join(', ')}{/if}
          {#snippet trailing()}
            <IconButton
              size="sm"
              ariaLabel={`Remove ${filterDimensionLabel(filter.dimension)} filter`}
              onclick={() => onRemoveFilter?.(index)}
            ><XIcon size="12" aria-hidden="true" /></IconButton>
          {/snippet}
        </Chip>
      {/each}
      {#each extraChips as chip (chip.key)}
        <Chip size="sm" uppercase={false} class="chip chip--filter" title={chip.label}>
          {chip.label}
          {#snippet trailing()}
            <IconButton size="sm" ariaLabel={chip.removeLabel} onclick={chip.onRemove}
            ><XIcon size="12" aria-hidden="true" /></IconButton>
          {/snippet}
        </Chip>
      {/each}
      {#each groupingChain as dimension, index (`${dimension}:${index}`)}
        <Chip size="sm" uppercase={false} class="chip chip--group" title={groupedByLabel([dimension])}>
          {groupedByLabel([dimension])}
          {#snippet trailing()}
            <IconButton
              size="sm"
              ariaLabel={`Remove ${filterDimensionLabel(dimension)} grouping`}
              onclick={() => onRemoveGroup(index)}
            ><XIcon size="12" aria-hidden="true" /></IconButton>
          {/snippet}
        </Chip>
      {/each}
    </div>
  {/if}

  {#if filtersOpen && !messages}
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
      {@render mailboxFilters()}
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
    padding: var(--pane-pad-y) var(--pane-pad-x);
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

  .context-chips :global(.chip) {
    background: var(--bg-inset);
    color: var(--text-secondary);
  }

  .context-chips :global(.chip--filter) {
    border: 1px solid color-mix(in srgb, var(--accent-amber) 35%, var(--border-muted));
    background: color-mix(in srgb, var(--accent-amber) 8%, var(--bg-surface));
  }

  .context-chips :global(.chip--group) {
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
</style>
