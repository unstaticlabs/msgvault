# Upstream integration compatibility

This integration preserves both upstream history and the archive's existing multi-user, calendar, Inbox, and related-email behavior. The merge joins the two histories. Compatibility changes keep the newly shared surfaces within the same source and provider authority boundaries.

## Version identities

| Contract | Existing fork | Upstream revision `60b903a5` | Combined tree |
|---|---:|---:|---:|
| HTTP API schema | 2.26 | 3.17 | 3.18 |
| Completed archive schema marker | absent (legacy version 0) | 6 | 7 |
| Rebuildable analytics cache | 29 | 31 | 32 |

These version numbers describe the combined interfaces and schema. They do not imply compatibility with an older reader. A schema-6 embedded reader refuses a schema-7 archive. Before upgrading a persistent archive, retain its restorable predecessor backup; an older binary cannot serve as a schema downgrade.

The API keeps the native upstream daemon-owned Saved View execution endpoint. Saved definitions, including incompatible future values, still round-trip without being rewritten. Incompatible views remain visible and require an explicit repair. Recovery requests retain Saved View and revision authority.

Cache 32 retains the related-email facts, stable family identities, local recovery journal, provider Inbox/read state, and upstream incremental child-row publication. A predecessor cache requires rebuilding; it is not promoted by changing its marker.

## Draft evidence

Historical trusted `draft` labels normalize to the native `drafts` role during setup. Label IDs, message membership, display names, and Sent evidence remain in place. A real normalization invalidates derived facts once. Repeating setup makes no further change. Both literal roles remain recognized when reading historical cache inputs, and a draft cannot become positive Sent evidence simply because both labels exist.

## Authority

A delegated agent grant and a restricted remote client keep their native route and source policies. Named keys and identity-provider sessions retain Administrator, Member, and Viewer authority and per-user source bindings.

Ordinary message reads, original MIME/thread exports, recordings, native tag reads, meeting intelligence, filename suggestions, and media search must intersect the authenticated source scope before reading content or deriving counts. An empty binding means no sources. Grouped related email keeps every permitted family member after a matching-copy filter; it cannot turn an excluded copy into evidence or a provider-mutation target.

Provider changes, archive maintenance, credential/consent changes, and external task creation remain administrative. CardDAV publication preview also remains administrative because it mints a token that authorizes publication. Generated person briefs remain administrative because their prose can summarize evidence across source boundaries; source filtering cannot safely redact such prose after generation. Existing local person-graph review remains available to Members.

## Upgrade evidence

Qualification uses separate synthetic predecessor archives produced by the exact original store implementations. The legacy fork archive upgrades from version 0 to 7 while preserving its original message bytes, bodies, recipients, labels, sources, conversations, linkage facts, stable work identities, recovery records, users, and source bindings. An archive produced by exact upstream `60b903a5` upgrades from schema 6 to 7. The schema-6 reader's refusal of the upgraded archive is checked separately.

These are upgrade/read contracts. Still images of the interface do not prove provider behavior, migration safety, or recovery correctness.
