import type { ExploreFilter } from './models';
export type MailboxDimension = 'inbox_state' | 'read_state';
const labels: Record<MailboxDimension, Record<string, string>> = {
  inbox_state: { inbox: 'In inbox', outside: 'Outside inbox', unknown: 'Inbox status unknown' },
  read_state: { read: 'Read', unread: 'Unread', unknown: 'Read status unknown' }
};
export function mailboxFilterValue(filters: ExploreFilter[], dimension: MailboxDimension): string {
  const values = filters.find((filter) => filter.dimension === dimension)?.values;
  return !values?.length ? '' : values.length === 1 ? values[0] : 'multiple';
}
export function mailboxFilterOptions(filters: ExploreFilter[], dimension: MailboxDimension) {
  return [
    { value: '', label: dimension === 'inbox_state' ? 'All inbox states' : 'All read states' },
    ...Object.entries(labels[dimension]).map(([value, label]) => ({ value, label })),
    ...(mailboxFilterValue(filters, dimension) === 'multiple' ? [{ value: 'multiple', label: 'Multiple states', disabled: true }] : [])
  ];
}
export function withMailboxFilter(filters: ExploreFilter[], dimension: MailboxDimension, value: string): ExploreFilter[] {
  const remaining = filters.filter((filter) => filter.dimension !== dimension);
  return value ? [...remaining, { dimension, values: [value] }] : remaining;
}
export function filterCaption(filter: ExploreFilter): string {
  if (filter.dimension === 'inbox_state' || filter.dimension === 'read_state') {
    return filter.values.map((value) => labels[filter.dimension as MailboxDimension][value] ?? value).join(' or ');
  }
  return `Filter ${filter.dimension}: ${filter.values.join(', ')}`;
}
export function mailboxCaption(inboxState?: string, readState?: string): string {
  const read = labels.read_state[readState ?? 'unknown'] ?? labels.read_state.unknown;
  const inbox = labels.inbox_state[inboxState ?? 'unknown'] ?? labels.inbox_state.unknown;
  return `${read} · ${inbox}`;
}
