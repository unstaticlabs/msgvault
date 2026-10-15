<script lang="ts">
  import {
    createSavedView as generatedCreateSavedView,
    deleteSavedView as generatedDeleteSavedView,
    listSavedViews as generatedListSavedViews,
    patchSavedView as generatedPatchSavedView,
  } from '../../api/generated/api/api';
  import { Button, Card, EmptyState, Modal, TextInput } from '@kenn-io/kit-ui';
  import { onMount } from 'svelte';
  import type { APIClient } from '../../api/client';
  import type {
    SavedView as GeneratedSavedView,
    SavedViewStateEnvelope as GeneratedSavedViewStateEnvelope,
  } from '../../api/generated/models';
  import {
    DEFAULT_EXPLORE_COLUMNS,
    isFilterDimension,
    isValidFilterID,
    type ExploreColumn,
    type ExploreURLState,
  } from '../../explore/models';
  import { emailWorkFilterProblem } from '../../explore/email-work';
  import { filterCaption } from '../../explore/mailbox-state';
  import { isGroupingDimension } from '../../grouping/catalog';
  type SavedView = GeneratedSavedView;
  type CanonicalState = GeneratedSavedViewStateEnvelope;
  const CURRENT_SCHEMA_VERSION = 1;
  /** Persisted v1 field names that predate the current filter dimension vocabulary. */
  const FILTER_ALIASES = new Map<string, ExploreURLState['filters'][number]['dimension']>([
    ['source_id', 'source'],
    ['participant_id', 'participant'],
  ]);
  let {
    client,
    currentState,
    selection = undefined,
    onOpen = () => undefined,
  }: {
    client: APIClient;
    currentState: ExploreURLState;
    selection?: unknown;
    onOpen?: (state: Partial<ExploreURLState>) => void;
  } = $props();
  let views = $state<SavedView[]>([]);
  let loading = $state(true);
  let saving = $state(false);
  let error = $state('');
  let name = $state('');
  let description = $state('');
  let editing = $state<SavedView>();
  let repairingFilters = $state(false);
  let editName = $state('');
  let editDescription = $state('');
  let deleting = $state<SavedView>();
  onMount(() => void load());
  async function load(): Promise<void> {
    loading = true;
    error = '';
    try {
      const { data, error: responseError } = await generatedListSavedViews(client);
      if (!data) throw new Error(messageFor(responseError, 'Unable to load Saved Views.'));
      views = data.saved_views ?? [];
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Unable to load Saved Views.';
    } finally {
      loading = false;
    }
  }
  function canonicalState(): CanonicalState {
    return {
      ...(currentState.query ? { query: currentState.query } : {}),
      search_mode: currentState.searchMode,
      filters: currentState.filters.map((filter) => ({
        field: filter.dimension,
        operator: 'in',
        values: [...filter.values],
      })),
      grouping: [...currentState.groupingChain],
      presentation: currentState.presentation,
      sort: currentState.sort.map((sort) => ({ field: sort.field, direction: sort.direction })),
      columns: [...currentState.columns],
    };
  }
  async function createView(): Promise<void> {
    if (!name.trim()) return;
    saving = true;
    error = '';
    try {
      const { data, error: responseError } = await generatedCreateSavedView(
        {
          name: name.trim(),
          ...(description.trim() ? { description: description.trim() } : {}),
          canonical_state: canonicalState(),
          schema_version: CURRENT_SCHEMA_VERSION,
        },
        client,
      );
      if (!data) throw new Error(messageFor(responseError, 'Unable to save this view.'));
      views = [...views, data].sort((left, right) => left.name.localeCompare(right.name));
      name = '';
      description = '';
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Unable to save this view.';
    } finally {
      saving = false;
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
  function canRepairFilters(view: SavedView): boolean {
    return view.schema_version === CURRENT_SCHEMA_VERSION && !incompatibilityFor({
      ...view, canonical_state: { ...view.canonical_state, filters: canonicalState().filters }
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
          ...(repairingFilters ? { canonical_state: { ...target.canonical_state, filters: canonicalState().filters } } : {}),
        },
        {
          ...client,
          headers: { 'If-Match': `"saved-view-${target.id}-r${target.revision}"` },
        },
      );
      if (!data) {
        if (response.status === 409)
          throw new Error('This Saved View changed in another session. Reload and review the latest revision.');
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
  async function confirmDelete(): Promise<void> {
    if (!deleting) return;
    const target = deleting;
    saving = true;
    error = '';
    try {
      const { response, error: responseError } = await generatedDeleteSavedView(
        { id: target.id },
        {
          ...client,
          headers: { 'If-Match': `"saved-view-${target.id}-r${target.revision}"` },
        },
      );
      if (!response.ok) throw new Error(messageFor(responseError, 'Unable to delete this view.'));
      views = views.filter((view) => view.id !== target.id);
      deleting = undefined;
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Unable to delete this view.';
    } finally {
      saving = false;
    }
  }
  function open(view: SavedView): void {
    const incompatibility = incompatibilityFor(view);
    if (incompatibility) return;
    const saved = view.canonical_state;
    const filters = (saved.filters ?? []).map((filter) => ({
      dimension:
        FILTER_ALIASES.get(filter.field) ??
        (filter.field as ExploreURLState['filters'][number]['dimension']),
      values: [...filter.values],
    }));
    onOpen({
      workspace: 'everything',
      savedViewID: view.id, savedViewRevision: view.revision,
      query: saved.query ?? '',
      searchMode: saved.search_mode === 'semantic' || saved.search_mode === 'hybrid' ? saved.search_mode : 'full_text',
      filters,
      groupingChain: [...(saved.grouping ?? [])] as ExploreURLState['groupingChain'],
      presentation: saved.presentation ?? 'table',
      sort: (saved.sort ?? [{ field: 'occurred_at', direction: 'desc' }]) as ExploreURLState['sort'],
      columns: (saved.columns ?? DEFAULT_EXPLORE_COLUMNS) as ExploreURLState['columns'],
      activeRow: null,
      selectedRow: null,
      conversationAnchor: null,
      scrollAnchor: null,
    });
  }
  function incompatibilityFor(view: SavedView): string {
    if (view.schema_version !== CURRENT_SCHEMA_VERSION) {
      return `This view uses schema version ${view.schema_version}. Automatic migration is not supported; remove it and save the current view again.`;
    }
    const saved = view.canonical_state;
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
      if (!['participant', 'domain', 'mailing_list'].includes(dimension)) {
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

<main class="saved-views" aria-label="Saved Views">
  <header>
    <div>
      <p>Archive workspace</p>
      <h1>Saved Views</h1>
    </div>
  </header>

  {#if error}<p class="notice notice--error" role="alert">{error}</p>{/if}

  <Card padding="sm" title="Save this view" meta="Shared across sessions">
    <form
      class="create"
      onsubmit={(event) => {
        event.preventDefault();
        void createView();
      }}
    >
      <TextInput ariaLabel="Name" placeholder="View name" bind:value={name} autocomplete="off" block />
      <TextInput
        ariaLabel="Description"
        placeholder="Description (optional)"
        bind:value={description}
        autocomplete="off"
        block
      />
      <Button type="submit" tone="workflow" surface="solid" label="Save" disabled={saving || !name.trim()} />
    </form>
  </Card>

  {#if loading}
    <p role="status">Loading Saved Views…</p>
  {:else if views.length === 0}
    <EmptyState title="No Saved Views yet" description="Save the current archive context to reuse it later." />
  {:else}
    <section class="view-list" aria-label="Saved View library">
      {#each views as view (view.id)}
        {@const incompatibility = incompatibilityFor(view)}
        <article class:needs-attention={Boolean(incompatibility)} class:repairing={editing?.id === view.id && repairingFilters}>
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
                    deleting = view;
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
                    deleting = view;
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

{#if deleting}
  <Modal
    title="Delete Saved View?"
    tone="danger"
    onclose={() => {
      deleting = undefined;
    }}
  >
    <p>Delete “{deleting.name}” from every authenticated browser session?</p>
    {#snippet footer()}
      <Button
        surface="soft"
        label="Cancel"
        onclick={() => {
          deleting = undefined;
        }}
      />
      <Button
        tone="danger"
        surface="solid"
        label="Confirm delete"
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
    max-width: 1080px;
    min-height: 0;
    flex: 1;
    flex-direction: column;
    gap: var(--space-4);
    margin-inline: auto;
    padding: var(--space-5) var(--space-6);
  }
  header,
  article,
  .actions {
    display: flex;
    align-items: center;
    gap: var(--space-3);
  }
  header p,
  h1,
  h2,
  article p {
    margin: 0;
  }
  header p {
    color: var(--status-warning-ink);
    font-size: var(--font-size-2xs);
    font-weight: 800;
    letter-spacing: 0.1em;
    text-transform: uppercase;
  }
  article p {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
  .create {
    display: grid;
    grid-template-columns: minmax(11rem, 0.8fr) minmax(15rem, 1.2fr) auto;
    align-items: center;
    gap: var(--space-2);
  }
  label {
    display: grid;
    gap: var(--space-1);
    color: var(--text-muted);
    font-size: var(--font-size-xs);
  }
  .view-list {
    display: grid;
    gap: var(--space-2);
  }
  article {
    justify-content: space-between;
    padding: var(--space-3);
    border-bottom: 1px solid var(--border-muted);
  }
  article.needs-attention { display: grid; grid-template-columns: minmax(0, 1fr); align-items: stretch; }
  article.repairing { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); align-items: stretch; }
  article.repairing .notice, article.repairing ul, article.repairing .actions { grid-column: 1 / -1; }
  article.repairing ul { margin: 0; padding-left: var(--space-5); }
  .actions { flex-wrap: wrap; }
  .view-copy {
    display: grid;
    min-width: 12rem;
    gap: var(--space-1);
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
    .create, article.repairing {
      grid-template-columns: 1fr;
    }
    article {
      align-items: stretch;
      flex-direction: column;
    }
  }
</style>
