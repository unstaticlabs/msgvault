<script lang="ts">
  import {
    deleteSavedView as generatedDeleteSavedView,
    listSavedViews as generatedListSavedViews,
    patchSavedView as generatedPatchSavedView,
  } from '../../api/generated/api/api';
  import { Button, EmptyState, Modal, TextInput } from '@kenn-io/kit-ui';
  import { onMount } from 'svelte';
  import PageHeader from '../shell/PageHeader.svelte';
  import type { APIClient } from '../../api/client';
  import type { SavedView } from '../../api/generated/models';
  import type { ExploreURLState } from '../../explore/models';
  import { ExploreSelectionState } from '../../explore/state.svelte';
  import SelectionCheckbox from '../common/SelectionCheckbox.svelte';
  import {
    CURRENT_SCHEMA_VERSION,
    exploreStateFromSavedView,
    savedViewSummary,
    type CanonicalState,
  } from '../../saved-views/canonical';
  import { isFilterDimension, isValidFilterID, type ExploreColumn } from '../../explore/models';
  import { defaultExploreURLState } from '../../explore/state.svelte';
  import { canonicalSavedViewState } from '../../saved-views/canonical';
  import { emailWorkFilterProblem } from '../../explore/email-work';
  import { filterCaption } from '../../explore/mailbox-state';
  import { isGroupingDimension } from '../../grouping/catalog';
  const FILTER_ALIASES = new Map<string, ExploreURLState['filters'][number]['dimension']>([['source_id','source'], ['participant_id','participant']]);
  let {
    client,
    currentState = defaultExploreURLState,
    onOpen = () => undefined,
  }: {
    client: APIClient;
    currentState?: ExploreURLState;
    onOpen?: (state: Partial<ExploreURLState>) => void;
  } = $props();
  let views = $state<SavedView[]>([]);
  let loading = $state(true);
  let saving = $state(false);
  let error = $state('');
  let editing = $state<SavedView>();
  let repairingFilters = $state(false);
  let editName = $state('');
  let editDescription = $state('');
  let deleting = $state<SavedView[]>([]);
  const selection = new ExploreSelectionState();
  const orderedKeys = $derived(views.map((view) => String(view.id)));
  const selectedViews = $derived(views.filter((view) => selection.isSelected(String(view.id))));
  const allSelected = $derived(orderedKeys.length > 0 && orderedKeys.every((key) => selection.isSelected(key)));
  const someSelected = $derived(orderedKeys.some((key) => selection.isSelected(key)));
  onMount(() => void load());
  async function load(): Promise<void> {
    loading = true;
    error = '';
    try {
      const { data, error: responseError } = await generatedListSavedViews(client);
      if (!data) throw new Error(messageFor(responseError, 'Unable to load saved views.'));
      views = data.saved_views ?? [];
      selection.clear();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Unable to load saved views.';
    } finally {
      loading = false;
    }
  }
  function beginEdit(view: SavedView): void {
    repairingFilters = false;
    editing = view;
    editName = view.name;
    editDescription = view.description ?? '';
  }
  function beginFilterRepair(view: SavedView): void {
    beginEdit(view);
    repairingFilters = true;
  }
  function canonicalState(): CanonicalState { return canonicalSavedViewState(currentState); }
  function localIncompatibilityFor(view: SavedView): string {
    if (!view.canonical_state || typeof view.canonical_state !== "object" || Array.isArray(view.canonical_state)) return "This saved definition cannot be opened. Create a new view from Explore.";
    const saved = view.canonical_state as CanonicalState;
    const seen = new Set<string>();
    let sourceValues: string[] | undefined;
    let identityValues: string[] | undefined;
    for (const filter of saved.filters ?? []) {
      if (!isFilterDimension(filter.field) && !FILTER_ALIASES.has(filter.field)) {
        return `This view has an unsupported v1 filter field: ${filter.field}.`;
      }
      if (filter.operator !== 'eq' && filter.operator !== 'in') {
        return `This view has an unsupported v1 filter operator: ${filter.operator}.`;
      }
      const dimension = FILTER_ALIASES.get(filter.field) ?? filter.field;
      if (!Array.isArray(filter.values) || filter.values.length === 0 || filter.values.some((value, index) =>
        typeof value !== 'string' || (!value.trim() && !(dimension === 'identity' && index === 2 && value === '')))) {
        return `This view has an unsupported v1 filter value for ${filter.field}.`;
      }
      if (!['account', 'participant', 'domain', 'mailing_list'].includes(dimension)) {
        if (seen.has(dimension)) return `This view repeats its ${dimension} filter. Select the filters again in Explore and save a new view. Your saved definition is unchanged.`;
        seen.add(dimension);
      }
      if (dimension === 'participant' && filter.values.some((value) => !isValidFilterID(value))) {
        return 'This saved people filter does not identify selected people. Select the people again in Explore and save a new view, or repair it with the current filters. Your saved definition is unchanged.';
      }
      if (dimension === 'source') {
        if (filter.values.some((value) => !isValidFilterID(value))) return 'This saved source filter does not identify selected sources. Select the sources again in Explore and save a new view. Your saved definition is unchanged.';
        sourceValues = filter.values;
      }
      if (dimension === 'identity') {
        if (filter.values.length !== 3 || !isValidFilterID(filter.values[0]!) || !['', 'any', 'sender', 'recipient'].includes(filter.values[2]!)) {
          return 'This saved identity filter has incomplete values or an unsupported direction. Select the identity again in Explore and save a new view. Your saved definition is unchanged.';
        }
        identityValues = filter.values;
      }
      if (dimension === 'after' || dimension === 'before') {
        if (filter.values.length !== 1 || !rfc3339Timestamp(filter.values[0]!)) {
          return 'This saved date filter needs one valid timestamp. Select the date again in Explore and save a new view. Your saved definition is unchanged.';
        }
      }
      if (dimension === 'deletion' && (filter.values.length !== 1 || !['active', 'deleted'].includes(filter.values[0]!))) {
        return 'This saved deletion filter needs one recognized status. Select the deletion status again in Explore and save a new view. Your saved definition is unchanged.';
      }
      const mailboxValues = dimension === 'inbox_state' ? ['inbox', 'outside', 'unknown'] : dimension === 'read_state' ? ['read', 'unread', 'unknown'] : undefined;
      if (mailboxValues && filter.values.some((value) => !mailboxValues.includes(value))) {
        return 'This saved mailbox filter has an unrecognized status. Select the mailbox status again in Explore and save a new view. Your saved definition is unchanged.';
      }
    }
    const workProblem = emailWorkFilterProblem((saved.filters ?? []).map((filter) => ({dimension: FILTER_ALIASES.get(filter.field) ?? filter.field, values: filter.values})));
    if (workProblem) return workProblem;
    if (identityValues && (sourceValues?.length !== 1 || BigInt(sourceValues[0]!) !== BigInt(identityValues[0]!))) {
      return 'This identity view needs its matching source. Select the source and identity again in Explore and save a new view. Your saved definition is unchanged.';
    }
    if (saved.search_mode && !['full_text', 'semantic', 'hybrid'].includes(saved.search_mode)) {
      return `This view has an unsupported v1 search mode: ${saved.search_mode}.`;
    }
    if ((saved.grouping ?? []).some((dimension) => !isGroupingDimension(dimension))) {
      return 'This view has an unsupported v1 grouping dimension.';
    }
    if (saved.presentation && !['table', 'timeline', 'files'].includes(saved.presentation)) return 'This view has an unsupported v1 presentation.';
    if ((saved.sort ?? []).some((sort) => sort.field !== 'occurred_at' || sort.direction !== 'desc')) {
      return 'This view has an unsupported v1 sort.';
    }
    const columns = new Set<ExploreColumn>(['kind', 'people', 'title', 'excerpt', 'time', 'attachments', 'size']);
    if ((saved.columns ?? []).some((column) => !columns.has(column as ExploreColumn))) {
      return 'This view has an unsupported v1 column.';
    }
    return '';
  }
  function repairFilterState(view: SavedView): Record<string, unknown> {
    const state = view.canonical_state;
    return { ...(state && typeof state === "object" && !Array.isArray(state) ? state : {}), filters: canonicalState().filters };
  }
  function canRepairFilters(view: SavedView): boolean {
    if (!view.canonical_state || typeof view.canonical_state !== "object" || Array.isArray(view.canonical_state)) return false;
    return view.schema_version === CURRENT_SCHEMA_VERSION && !localIncompatibilityFor({
      ...view, canonical_state: repairFilterState(view)
    });
  }
  async function saveEdit(): Promise<void> {
    if (!editing || !editName.trim()) return;
    saving = true;
    error = '';
    const target = editing;
    try {
      const {
        data,
        error: responseError,
        response,
      } = await generatedPatchSavedView(
        { id: target.id },
        {
          name: editName.trim(),
          description: editDescription.trim(),
          ...(repairingFilters ? { canonical_state: repairFilterState(target) } : {}),
        },
        {
          ...client,
          headers: { 'If-Match': `"saved-view-${target.id}-r${target.revision}"` },
        },
      );
      if (!data) {
        if (response.status === 409)
          throw new Error('This saved view changed in another session. Reload and review the latest revision.');
        throw new Error(messageFor(responseError, 'Unable to update this view.'));
      }
      views = views.map((view) => (view.id === data.id ? data : view));
      editing = undefined;
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Unable to update this view.';
    } finally {
      saving = false;
    }
  }
  function toggleSelection(view: SavedView, index: number, range: boolean): void {
    selection.toggle(String(view.id), index, orderedKeys, range);
  }
  function toggleAll(): void {
    if (allSelected) selection.clear();
    else selection.selectVisible(orderedKeys);
  }
  function beginBulkDelete(): void {
    if (selectedViews.length === 0 || saving) return;
    deleting = [...selectedViews];
  }
  async function confirmDelete(): Promise<void> {
    if (deleting.length === 0) return;
    const targets = [...deleting];
    saving = true;
    error = '';
    const deletedIDs = new Set<number>();
    const failures: string[] = [];
    for (const target of targets) {
      try {
        const { response, error: responseError } = await generatedDeleteSavedView(
          { id: target.id },
          {
            ...client,
            headers: { 'If-Match': `"saved-view-${target.id}-r${target.revision}"` },
          },
        );
        if (!response.ok) throw new Error(messageFor(responseError, `Unable to delete ${target.name}.`));
        deletedIDs.add(target.id);
      } catch (cause) {
        failures.push(cause instanceof Error ? cause.message : `Unable to delete ${target.name}.`);
      }
    }
    views = views.filter((view) => !deletedIDs.has(view.id));
    for (const id of deletedIDs) selection.explicitKeys.delete(String(id));
    deleting = [];
    saving = false;
    if (failures.length > 0) {
      error = `${deletedIDs.size.toLocaleString()} deleted; ${failures.length.toLocaleString()} could not be deleted. ${failures[0]}`;
    } else {
      selection.clear();
    }
  }
  function open(view: SavedView): void {
    const incompatibility = incompatibilityFor(view);
    if (incompatibility) return;
    onOpen({ ...exploreStateFromSavedView(view.canonical_state as CanonicalState), savedViewID: view.id, savedViewRevision: view.revision });
  }
  function incompatibilityFor(view: SavedView): string {
    if (view.schema_version !== CURRENT_SCHEMA_VERSION) {
      return `This view uses schema version ${view.schema_version}. Automatic migration is not supported; remove it and save the current view again.`;
    }
    return view.incompatibility_reason || localIncompatibilityFor(view);
  }
  function rfc3339Timestamp(value: string): boolean {
    // Match the native time.Parse(RFC3339) contract, including fractional
    // seconds and its accepted numeric zone range, without Date.parse's
    // normalization of invalid calendar dates.
    const parts = /^(\d{4})-(\d{2})-(\d{2})T(\d{1,2}):(\d{2}):(\d{2})(?:[.,]\d+)?(Z|[+-](\d{2}):(\d{2}))$/.exec(value);
    if (!parts) return false;
    const [year, month, day, hour, minute, second] = parts.slice(1, 7).map(Number);
    const leap = year! % 4 === 0 && (year! % 100 !== 0 || year! % 400 === 0);
    const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
    return month! >= 1 && month! <= 12 && day! >= 1 && day! <= days[month! - 1]! &&
      hour! <= 23 && minute! <= 59 && second! <= 59 &&
      (parts[7] === 'Z' || (Number(parts[8]) <= 24 && Number(parts[9]) <= 60));
  }
  function messageFor(value: unknown, fallback: string): string {
    return typeof value === 'object' && value !== null && 'message' in value && typeof value.message === 'string'
      ? value.message
      : fallback;
  }
</script>

<main class="saved-views" aria-label="Saved views">
  <PageHeader title="Saved views" description="Searches and layouts you've saved to reuse." />

  {#if error}<p class="notice notice--error" role="alert">{error}</p>{/if}

  {#if loading}
    <p role="status">Loading saved views…</p>
  {:else if views.length === 0}
    <EmptyState
      title="No saved views yet"
      description="Use Save view… in Everything or Files to keep a search and layout you want to return to."
    />
  {:else}
    <div class="selection-toolbar" aria-label="Saved view selection">
      <SelectionCheckbox
        checked={allSelected}
        mixed={!allSelected && someSelected}
        label={allSelected ? 'Unselect all saved views' : 'Select all saved views'}
        disabled={saving}
        onToggle={toggleAll}
      />
      <span class="selection-count">{selection.count > 0 ? `${selection.count.toLocaleString()} selected` : 'Select all'}</span>
      {#if selection.count > 0}
        <Button
          size="sm"
          tone="danger"
          surface="soft"
          label="Delete selected…"
          disabled={saving}
          onclick={beginBulkDelete}
        />
        <Button size="sm" surface="soft" label="Clear" disabled={saving} onclick={() => selection.clear()} />
      {/if}
    </div>
    <section class="view-list" aria-label="Saved view library">
      {#each views as view, index (view.id)}
        {@const incompatibility = incompatibilityFor(view)}
        <article class:selected={selection.isSelected(String(view.id))} class:needs-attention={Boolean(incompatibility)} class:repairing={editing?.id === view.id && repairingFilters}>
          <span class="row-checkbox">
            <SelectionCheckbox
              checked={selection.isSelected(String(view.id))}
              label={`${selection.isSelected(String(view.id)) ? 'Unselect' : 'Select'} ${view.name}`}
              disabled={saving}
              onToggle={(range) => toggleSelection(view, index, range)}
            />
          </span>
          {#if editing?.id === view.id}
            <label>Edit name<TextInput ariaLabel="Edit name" bind:value={editName} /></label>
            <label>Edit description<TextInput ariaLabel="Edit description" bind:value={editDescription} /></label>
            {#if repairingFilters}
              <p class="notice">Replace this view’s filters with the current Explore filters below. The saved definition stays unchanged until you select Save repaired filters.</p>
              <ul aria-label="Replacement filters">
                {#each currentState.filters as filter}<li>{filterCaption(filter)}</li>{:else}<li>All archive entries</li>{/each}
              </ul>
            {/if}
            <div class="actions">
              <Button
                size="sm"
                tone="info"
                surface="solid"
                label={repairingFilters ? 'Save repaired filters' : 'Save changes'}
                onclick={() => void saveEdit()}
                disabled={saving}
              />
              <Button
                size="sm"
                surface="soft"
                label="Cancel edit"
                onclick={() => {
                  editing = undefined;
                }}
              />
            </div>
          {:else}
            <div class="view-copy">
              <h2>{view.name}</h2>
              <p>{view.description ?? 'No description'}</p>
              {#if !incompatibility}
                <ul class="summary" aria-label={`${view.name} summary`}>
                  {#each savedViewSummary(view.canonical_state as CanonicalState) as part, index (index)}
                    <li>{part}</li>
                  {/each}
                </ul>
              {/if}
            </div>
            {#if incompatibility}
              <p class="notice" role="alert">{incompatibility}</p>
            {/if}
            <div class="actions">
              <Button
                size="sm"
                tone="info"
                surface="soft"
                label={`Open ${view.name}`}
                disabled={Boolean(incompatibility)}
                onclick={() => open(view)}
              />
              {#if !incompatibility}
                <Button size="sm" surface="soft" label={`Edit ${view.name}`} onclick={() => beginEdit(view)} />
                <Button
                  size="sm"
                  tone="danger"
                  surface="soft"
                  label={`Delete ${view.name}`}
                  onclick={() => {
                    deleting = [view];
                  }}
                />
              {:else}
                {#if canRepairFilters(view)}
                  <Button size="sm" surface="soft" label={`Repair filters for ${view.name}`} onclick={() => beginFilterRepair(view)} />
                {/if}
                <Button
                  size="sm"
                  tone="danger"
                  surface="soft"
                  label={`Remove incompatible ${view.name}`}
                  onclick={() => {
                    deleting = [view];
                  }}
                />
              {/if}
            </div>
          {/if}
        </article>
      {/each}
    </section>
  {/if}
</main>

{#if deleting.length > 0}
  <Modal
    title={deleting.length === 1 ? 'Delete saved view?' : `Delete ${deleting.length.toLocaleString()} saved views?`}
    tone="danger"
    onclose={() => {
      if (!saving) deleting = [];
    }}
  >
    <p>
      {deleting.length === 1
        ? `Delete “${deleting[0]!.name}” from every authenticated browser session?`
        : `Delete these ${deleting.length.toLocaleString()} saved views from every authenticated browser session?`}
    </p>
    {#snippet footer()}
      <Button
        surface="soft"
        label="Cancel"
        disabled={saving}
        onclick={() => {
          deleting = [];
        }}
      />
      <Button
        tone="danger"
        surface="solid"
        label={saving ? 'Deleting…' : 'Confirm delete'}
        disabled={saving}
        onclick={() => void confirmDelete()}
      />
    {/snippet}
  </Modal>
{/if}

<style>
  .saved-views {
    display: flex;
    width: 100%;
    min-height: 0;
    flex: 1;
    flex-direction: column;
    gap: var(--space-4);
    padding: var(--space-5) var(--page-gutter) var(--space-4);
    background: var(--bg-primary);
  }
  article,
  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-3);
  }
  .selection-toolbar {
    display: flex;
    min-height: 28px;
    align-items: center;
    gap: var(--space-2);
    padding-inline: var(--space-2);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
  .selection-count {
    min-width: 0;
    flex: 1;
  }
  h2,
  article p {
    margin: 0;
  }
  article p {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
  label {
    display: grid;
    gap: var(--space-1);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
  .view-list {
    display: grid;
    width: 100%;
    overflow: hidden;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-surface);
    box-shadow: var(--shadow-sm);
  }
  article {
    justify-content: space-between;
    min-height: 64px;
    padding: var(--space-3) var(--space-4);
    border-bottom: 1px solid var(--border-muted);
  }
  article.needs-attention { display: grid; grid-template-columns: minmax(0, 1fr); align-items: stretch; }
  article.repairing { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); align-items: stretch; }
  article.repairing .notice, article.repairing ul, article.repairing .actions { grid-column: 1 / -1; }
  article.repairing ul { margin: 0; padding-left: var(--space-5); }
  .actions { flex-wrap: wrap; }
  article:last-child {
    border-bottom: 0;
  }
  article:hover {
    background: var(--bg-surface-hover);
  }
  article.selected {
    background: var(--selected-bg);
  }
  .row-checkbox {
    display: grid;
    width: 24px;
    height: 24px;
    flex: none;
    place-items: center;
  }
  .view-copy {
    display: grid;
    min-width: min(12rem, 100%);
    flex: 1;
    gap: var(--space-1);
  }
  .summary {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .summary li {
    padding: 0 var(--space-2);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    background: var(--bg-subtle);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }
  .notice {
    padding: var(--space-2) var(--space-3);
    border: 1px solid var(--accent-amber);
    border-radius: var(--radius-md);
    background: var(--bg-subtle);
  }
  .notice--error {
    border-color: var(--accent-red);
    color: var(--text-danger);
  }
  @media (max-width: 760px) {
    article.repairing {
      grid-template-columns: 1fr;
    }
    article {
      align-items: flex-start;
    }
    article .actions {
      flex-basis: 100%;
      padding-left: calc(24px + var(--space-3));
    }
  }
</style>
