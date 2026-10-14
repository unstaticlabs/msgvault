<script lang="ts">
  import { Button, Checkbox, SearchInput } from '@kenn-io/kit-ui';
  import { onDestroy, untrack } from 'svelte';
  import type { APIClient } from '../../api/client';
  import type { EmailWorkMembersResponse, EmailWorkMemberFact, EmailWorkActionPreview, EmailWorkActionResponse } from '../../api/generated/models';
  import { emailWorkMembers, emailWorkPreview, emailWorkApply, emailWorkUndo, emailWorkExport, emailWorkBody } from '../../explore/email-work-api';
  import type { ExplorePredicate, EntryRow } from '../../explore/models';
  import { HANDLING_LABELS, emailWorkReasonLabel, emailWorkExtractionLabel, emailWorkActorLabel } from '../../explore/email-work';
  import { mailboxCaption } from '../../explore/mailbox-state';
  import { createExploreAPI } from '../../explore/api';
  import type { ArchiveMessageDetail, MessageViewMode } from '../../archive/types';
  import MessageCard from './MessageCard.svelte';

  let { client, uid, predicate, cacheRevision, canCurate = false, onRefresh, savedViewID, savedViewRevision, onOpenIndividual }: {
    client: APIClient; uid: string; predicate: ExplorePredicate; cacheRevision: string; canCurate?: boolean;
    onOpenIndividual?: (member: EmailWorkMemberFact) => void; onRefresh?: () => void; savedViewID?: number; savedViewRevision?: number;
  } = $props();
  const explore = createExploreAPI(untrack(() => client));
  let detail = $state<EmailWorkMembersResponse>();
  let loading = $state(false);
  let error = $state('');
  let notice = $state('');
  let moreError = $state('');
  let busy = $state(false);
  let selected = $state<string[]>([]);
  let preview = $state<EmailWorkActionPreview>();
  let linking = $state(false);
  let search = $state('');
  let candidates = $state<EntryRow[]>([]);
  let candidateMembers = $state<EmailWorkMemberFact[]>([]);
  let candidateError = $state('');
  let searching = $state(false);
  let searched = $state(false);
  let bodies = $state<Record<string, ArchiveMessageDetail>>({});
  let bodyErrors = $state<Record<string, string>>({});
  let expanded = $state<string[]>([]);
  let bodyLoading = $state<string[]>([]);
  let modes = $state<Record<string, MessageViewMode>>({});
  let controller: AbortController | undefined;
  let generation = 0;
  let retry = $state(0);
  const predicateKey = $derived(JSON.stringify(predicate));
  const scopePredicate = $derived({ ...predicate, query: undefined, cursor: undefined, candidate_snapshot_id: undefined, grouping: undefined,
    filters: (predicate.filters ?? []).filter((filter) => filter.dimension === 'source' || filter.dimension === 'row_unit'), presentation: 'table' as const });
  const viewPin = $derived(savedViewID && savedViewRevision ? {saved_view_id: savedViewID, saved_view_revision: savedViewRevision} : {});
  const allKnown = $derived([...(detail?.members ?? []), ...candidateMembers]);

  $effect(() => {
    const target = uid; const revision = cacheRevision; void predicateKey; void retry;
    const currentPredicate = untrack(() => predicate);
    generation += 1; controller?.abort(); controller = new AbortController();
    const current = generation; const signal = controller.signal;
    detail = undefined; error = ''; moreError = ''; preview = undefined; selected = []; candidates = []; candidateMembers = [];
    busy = false;
    bodies = {}; bodyErrors = {}; expanded = []; bodyLoading = []; linking = false;
    if (!revision) { loading = true; return; }
    loading = true;
    void emailWorkMembers(client, {uid: target, predicate: currentPredicate, cache_revision: revision, limit: 100, offset: 0}, signal)
      .then((value) => { if (current === generation && !signal.aborted) detail = value; })
      .catch((cause: unknown) => { if (current === generation && !signal.aborted) error = cause instanceof Error ? cause.message : 'Could not load related email.'; })
      .finally(() => { if (current === generation) loading = false; });
  });
  onDestroy(() => { generation += 1; controller?.abort(); });

  function refresh(): void { preview = undefined; retry += 1; onRefresh?.(); }
  async function more(): Promise<void> {
    if (!detail || busy) return;
    const current = generation; const existing = detail; busy = true; moreError = '';
    try {
      const page = await emailWorkMembers(client, {uid, predicate, cache_revision: existing.cache_revision, limit: 100, offset: existing.members.length}, controller?.signal);
      if (current === generation) detail = {...page, members: [...existing.members, ...page.members]};
    } catch (cause) { if (current === generation) moreError = cause instanceof Error ? cause.message : 'Could not load more copies.'; }
    finally { if (current === generation) busy = false; }
  }
  function toggle(member: EmailWorkMemberFact): void {
    if (!member.message_uid) return;
    preview = undefined;
    selected = selected.includes(member.message_uid) ? selected.filter((value) => value !== member.message_uid) : [...selected, member.message_uid];
  }
  async function openBody(member: EmailWorkMemberFact): Promise<void> {
    const key = member.message_uid;
    if (!key) return;
    if (expanded.includes(key)) { expanded = expanded.filter((value) => value !== key); return; }
    expanded = [...expanded, key];
    if (bodies[key]) return;
    const current = generation; bodyLoading = [...bodyLoading, key]; bodyErrors = {...bodyErrors, [key]: ''};
    try {
      const message = await emailWorkBody(client, member.message_id, key, controller?.signal);
      if (current === generation) bodies = {...bodies, [key]: {id: message.id, conversationId: message.conversation_id ?? member.conversation_id, subject: message.subject, sender: message.from,
        recipients: message.to ?? [], sentAt: message.sent_at, snippet: message.snippet, body: message.body, bodyHtml: message.body_html,
        attachments: (message.attachments ?? []).map((file) => ({filename: file.filename, mimeType: file.mime_type, sizeBytes: file.size_bytes}))}};
    } catch (cause) { if (current === generation) bodyErrors = {...bodyErrors, [key]: cause instanceof Error ? cause.message : 'Could not open this copy.'}; }
    finally { if (current === generation) bodyLoading = bodyLoading.filter((value) => value !== key); }
  }
  async function openIndividual(member: EmailWorkMemberFact): Promise<void> {
    if (!member.message_uid || busy) return;
    const current = generation; busy = true; error = '';
    try {
      await emailWorkBody(client, member.message_id, member.message_uid, controller?.signal);
      if (current === generation) onOpenIndividual?.(member);
    } catch (cause) { if (current === generation) error = cause instanceof Error ? cause.message : 'This copy changed. Refresh before opening the individual item.'; }
    finally { if (current === generation) busy = false; }
  }
  async function findRelated(): Promise<void> {
    const current = generation; searching = true; searched = true; candidateError = ''; candidates = []; candidateMembers = []; preview = undefined;
    try {
      const result = await explore.explore({...scopePredicate, ...(search.trim() ? {query: search.trim(), search_mode: 'full_text' as const} : {query: undefined, search_mode: undefined}), limit: 30}, controller?.signal);
      if (current !== generation) return;
      if (result.status === 'unavailable') throw new Error(result.unavailable.message);
      if (result.result.cacheRevision !== detail?.cache_revision) throw new Error('The archive changed. Refresh this item before choosing another copy.');
      candidates = result.result.rows.filter((row) => row.email_work && row.email_work.uid !== detail?.uid);
    } catch (cause) { if (current === generation) candidateError = cause instanceof Error ? cause.message : 'Could not search permitted accounts.'; }
    finally { if (current === generation) searching = false; }
  }
  async function chooseCandidate(row: EntryRow): Promise<void> {
    if (!detail || !row.email_work) return;
    const current = generation; busy = true; candidateError = '';
    try {
      const result = await emailWorkMembers(client, {uid: row.email_work.uid, predicate: scopePredicate, cache_revision: detail.cache_revision, limit: 1, offset: 0}, controller?.signal);
      if (current !== generation) return;
      const member = result.requested_anchor;
      if (!member?.message_uid) throw new Error('This copy has no current identity. Refresh and choose it again.');
      if (!allKnown.some((value) => value.message_uid === member.message_uid)) candidateMembers = [...candidateMembers, member];
      if (!selected.includes(member.message_uid)) selected = [...selected, member.message_uid];
      preview = undefined;
    } catch (cause) { if (current === generation) candidateError = cause instanceof Error ? cause.message : 'Could not choose this copy.'; }
    finally { if (current === generation) busy = false; }
  }
  async function review(kind: 'link' | 'handled'): Promise<void> {
    if (!detail || busy) return;
    const current = generation; busy = true; error = ''; preview = undefined;
    try {
      const targets = selected.map((key) => { const member = allKnown.find((value) => value.message_uid === key)!; return {uid: key, source_id: member.source_id}; });
      const value = await emailWorkPreview(client, {kind, targets, predicate: scopePredicate, cache_revision: detail.cache_revision, ...viewPin}, controller?.signal);
      if (current === generation) preview = value;
    } catch (cause) { if (current === generation) error = cause instanceof Error ? cause.message : 'Could not review the selected copies.'; }
    finally { if (current === generation) busy = false; }
  }
  async function apply(): Promise<void> {
    if (!preview || busy) return;
    const current = generation; busy = true; error = '';
    try {
      const {messages: _messages, ...plan} = preview;
      const result = await emailWorkApply(client, plan, controller?.signal);
      if (current !== generation) return;
      notice = result.cache_state === 'ready' ? 'Local confirmation saved. All original copies are preserved.' : 'Local confirmation saved. The archive cache is refreshing; retry when it is ready.';
      refresh();
    } catch (cause) { if (current === generation) error = cause instanceof Error ? cause.message : 'Could not save this local confirmation.'; }
    finally { if (current === generation) busy = false; }
  }
  async function download(): Promise<void> {
    if (!detail || busy) return;
    const current = generation; busy = true; error = '';
    try {
      const result = await emailWorkExport(client, {uid, predicate, cache_revision: detail.cache_revision}, controller?.signal);
      if (current !== generation) return;
      const url = URL.createObjectURL(result.blob);
      const link = document.createElement('a'); link.href = url; link.download = 'related-email.zip'; link.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      notice = result.state === 'partial' ? 'Partial export downloaded. Every permitted copy is listed; the manifest marks copies whose raw email is unavailable.' : 'Export downloaded. The manifest lists every permitted copy and its provenance.';
    } catch (cause) { if (current === generation) error = cause instanceof Error ? cause.message : 'Could not export related email.'; }
    finally { if (current === generation) busy = false; }
  }
  async function undo(action: NonNullable<EmailWorkMembersResponse['local_actions']>[number]): Promise<void> {
    if (!detail || busy) return;
    const current = generation; busy = true; error = '';
    try {
      await emailWorkUndo(client, {uid: action.uid, expected_revision: action.revision, snapshot: detail.recovery_snapshot, predicate: scopePredicate, cache_revision: detail.cache_revision, ...viewPin}, controller?.signal);
      if (current !== generation) return;
      notice = 'Local confirmation undone. Original reply evidence and copies are preserved.'; refresh();
    } catch (cause) { if (current === generation) error = cause instanceof Error ? cause.message : 'Could not undo this confirmation. Refresh and try again.'; }
    finally { if (current === generation) busy = false; }
  }
</script>

<section class="email-work-view" aria-label="Related email details" data-scroll>
  {#if notice}<p role="status">{notice}</p>{/if}
  {#if loading}<p role="status">Loading related email…</p>{/if}
  {#if error}<p role="alert">{error}</p><Button size="sm" surface="outline" label="Refresh related email" onclick={refresh} />{/if}
  {#if detail}
    <header class="work-summary">
      <strong>{HANDLING_LABELS[detail.summary.state] ?? 'Handling unknown'} in these accounts</strong>
      <span>{detail.total_count} archived {detail.total_count === 1 ? 'copy' : 'copies'} · {detail.summary.source_ids.length} {detail.summary.source_ids.length === 1 ? 'account' : 'accounts'} · {detail.summary.outstanding_count} outstanding · {detail.summary.evidence_count} outbound evidence</span>
      <p>Read, Inbox and local-read status describe each copy. They do not mean this item was handled.</p>
      {#if detail.requested_uid !== detail.uid}<p role="status">This saved link now resolves to the related item containing its original copy.</p>{/if}
      {#if detail.summary.reasons?.length}<ul aria-label="Handling uncertainty">{#each detail.summary.reasons as reason}<li>{emailWorkReasonLabel(reason)}</li>{/each}</ul>{/if}
      <div class="recovery-actions"><Button size="sm" surface="outline" label="Refresh related email" onclick={refresh} /><Button size="sm" surface="outline" label="Export all permitted copies" disabled={busy} onclick={() => void download()} /></div>
    </header>
    {#each detail.members as member (member.message_uid ?? member.message_id)}
      <article class="work-copy" aria-label={`Archived copy from ${member.source_identifier}`}>
        <div class="copy-heading">
          {#if canCurate && member.message_uid}<Checkbox checked={selected.includes(member.message_uid)} ariaLabel={`Select copy ${member.message_id} from ${member.source_identifier}`} onchange={() => toggle(member)} />{/if}
          <div><strong>{member.title || '(untitled)'}</strong><span>{member.source_identifier} · {member.source_type}</span></div>
          <Button size="sm" surface="outline" disabled={!member.message_uid} label={expanded.includes(member.message_uid ?? '') ? 'Close copy' : 'Read copy'} onclick={() => void openBody(member)} />
        </div>
        {#if onOpenIndividual}<Button size="sm" surface="outline" label="Open individual item" disabled={busy || !member.message_uid} onclick={() => void openIndividual(member)} />{/if}
        <p>{mailboxCaption(member.inbox_state, member.read_state)} · Local read: {member.local_read_state ?? 'unknown'} · {member.native_draft ? 'Draft' : member.native_sent ? 'Sent' : member.incoming ? 'Received' : 'Direction unknown'}</p>
        <p>{detail.outstanding_message_ids.includes(member.message_id) ? 'Outstanding received event' : detail.evidence_message_ids.includes(member.message_id) ? 'Completed outbound reply evidence' : 'Retained related copy'}</p>
        <details><summary>Why these copies are linked</summary>
          <dl><dt>Source message ID</dt><dd>{member.source_message_id}</dd><dt>{member.provider_thread_id ? 'Source thread ID' : 'Archive conversation ID'}</dt><dd>{member.provider_thread_id || member.conversation_id}</dd><dt>Saved labels</dt><dd>{member.labels.join(', ') || 'None'}</dd><dt>Archived evidence</dt><dd>{emailWorkExtractionLabel(member.linkage_outcome)}</dd><dt>Sender matches a confirmed account identity</dt><dd>{member.confirmed_source_sender ? 'Yes' : 'No'}</dd>
            {#if member.rfc_message_id}<dt>Message-ID</dt><dd>{member.rfc_message_id}</dd>{/if}
            {#if member.reply_ids?.length}<dt>Direct reply</dt><dd>{member.reply_ids.join(', ')}</dd>{/if}
            {#if member.reference_ids?.length}<dt>References</dt><dd>{member.reference_ids.join(', ')}</dd>{/if}
            {#if member.attached_original_ids?.length}<dt>Attached original</dt><dd>{member.attached_original_ids.join(', ')}</dd>{/if}
            {#if member.unavailable_reasons?.length}<dt>Uncertainty</dt><dd>{member.unavailable_reasons.map(emailWorkReasonLabel).join(', ')}</dd>{/if}
          </dl>
        </details>
        {#if expanded.includes(member.message_uid ?? '')}
          {#if bodyLoading.includes(member.message_uid!)}<p role="status">Loading this archived copy…</p>
          {:else if bodyErrors[member.message_uid!]}<p role="alert">{bodyErrors[member.message_uid!]}</p><Button size="sm" surface="outline" label="Retry this copy" onclick={() => {expanded = expanded.filter((value) => value !== member.message_uid); void openBody(member);}} />
          {:else if bodies[member.message_uid!]}
            <MessageCard {client} message={bodies[member.message_uid!]} expanded={true} viewMode={modes[member.message_uid!] ?? 'html'} onToggle={() => void openBody(member)} onViewModeChange={(_id, mode) => {modes = {...modes, [member.message_uid!]: mode};}} />
          {/if}
        {/if}
      </article>
    {/each}
    {#if detail.members.length < detail.total_count}<Button size="sm" surface="outline" label="Load more copies" disabled={busy} onclick={() => void more()} />{/if}
    {#if moreError}<p role="alert">{moreError}</p><Button size="sm" surface="outline" label="Retry more copies" onclick={() => void more()} />{/if}
    {#if canCurate}
      <section class="local-recovery" aria-label="Local email recovery">
        <strong>Local recovery</strong><p>Select exact copies to link them or confirm they were handled. This changes only this archive, preserves uncertainty and never sends, archives or deletes mail.</p>
        <Button size="sm" surface="outline" label="Find another related item" onclick={() => {linking = !linking; preview = undefined;}} />
        {#if linking}
          <SearchInput value={search} ariaLabel="Search permitted accounts" placeholder="Subject or archived text" oninput={(value) => {search = value;}} onkeydown={(event) => {if (event.key === 'Enter') {event.preventDefault(); void findRelated();}}} />
          <Button size="sm" surface="outline" label="Search permitted accounts" disabled={searching || busy} onclick={() => void findRelated()} />
          {#if searching}<p role="status">Searching selected accounts…</p>{/if}
          {#if candidateError}<p role="alert">{candidateError}</p>{:else if searched && !searching && candidates.length === 0}<p role="status">No other related items matched in these accounts. Try fewer words or clear the search to browse.</p>{/if}
          {#each candidates as row (row.key)}<Button size="sm" surface="outline" label={`Choose ${row.title || '(untitled)'} · ${row.source_identifier}`} disabled={busy} onclick={() => void chooseCandidate(row)} />{/each}
          {#each candidateMembers as member (member.message_uid)}<p>Chosen additional copy: {member.title} · {member.source_identifier}<Button size="sm" surface="outline" label="Remove additional copy" onclick={() => {selected = selected.filter((value) => value !== member.message_uid); candidateMembers = candidateMembers.filter((value) => value.message_uid !== member.message_uid); preview = undefined;}} /></p>{/each}
        {/if}
        <p>{selected.length} exact copies selected. New inbound copies are never covered by an earlier confirmation.</p>
        <div class="recovery-actions"><Button size="sm" surface="outline" label="Review local link" disabled={busy || selected.length < 2 || selected.length > 100} onclick={() => void review('link')} /><Button size="sm" surface="outline" label="Review handled confirmation" disabled={busy || selected.length === 0 || selected.length > 100} onclick={() => void review('handled')} /></div>
        {#if preview}<section class="recovery-preview" aria-label="Review fixed copies"><strong>{preview.kind === 'link' ? 'Link these exact copies locally?' : 'Confirm these exact copies were handled?'}</strong><ul>{#each preview.messages as member (member.message_uid)}<li>{member.title} · {member.source_identifier} · Source message ID: {member.source_message_id}</li>{/each}</ul><p>This assertion is local and undoable. It supplies no automatic reply evidence.</p><Button size="sm" surface="soft" label={preview.kind === 'link' ? 'Link these copies' : 'Mark these copies handled'} disabled={busy} onclick={() => void apply()} /><Button size="sm" surface="outline" label="Cancel review" disabled={busy} onclick={() => {preview = undefined;}} /></section>{/if}
        {#each detail.local_actions ?? [] as action (action.uid)}<p>{action.kind === 'link' ? 'Local link' : 'Local handled confirmation'} · {action.targets.length} fixed {action.targets.length === 1 ? 'copy' : 'copies'} · {new Date(action.created_at).toLocaleString()} · {emailWorkActorLabel(action.actor_kind, action.actor_user_id)}
          {#if action.active}<Button size="sm" surface="outline" label={action.kind === 'link' ? 'Undo local link' : 'Undo handled confirmation'} disabled={busy} onclick={() => void undo(action)} />{:else}<span> · Undone</span>{/if}</p>{/each}
      </section>
    {:else}<p>Local recovery is available to archive Members. You can inspect every permitted copy.</p>{/if}
    <p>Provider archive and delete actions apply to individual messages. Grouped items do not perform provider mutations.</p>
  {/if}
</section>
<style>
  .email-work-view { min-height: 0; overflow: auto; display: flex; flex-direction: column; gap: var(--space-4); padding: var(--space-4); }
  .work-summary, .local-recovery { display: flex; flex-direction: column; align-items: flex-start; gap: var(--space-2); }
  .work-copy { border-top: 1px solid var(--border-muted); padding-top: var(--space-3); }
  .copy-heading { display: flex; align-items: flex-start; gap: var(--space-2); }
  .copy-heading > div { min-width: 0; flex: 1; display: flex; flex-direction: column; overflow-wrap: anywhere; }
  p, li, dd, span { font-size: var(--font-size-xs); color: var(--text-secondary); line-height: 1.5; }
  p { margin: var(--space-2) 0; } strong { color: var(--text-primary); font-size: var(--font-size-sm); }
  details { margin: var(--space-2) 0; } summary { cursor: pointer; color: var(--text-secondary); font-size: var(--font-size-xs); }
  dl { display: grid; grid-template-columns: minmax(90px, 1fr) minmax(0, 3fr); gap: var(--space-2); }
  dt { font-size: var(--font-size-xs); color: var(--text-muted); } dd { margin: 0; overflow-wrap: anywhere; }
  .local-recovery { border-top: 1px solid var(--border-muted); padding-top: var(--space-4); }
  .recovery-actions { display: flex; flex-wrap: wrap; gap: var(--space-2); }
  .recovery-preview { padding: var(--space-3); border: 1px solid var(--border-default); border-radius: var(--radius-md); background: var(--bg-inset); }
</style>
