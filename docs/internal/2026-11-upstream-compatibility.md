# November upstream integration

The distribution integrates upstream `c3afaa35db8fb3ca1f07cee000dc82468d376b33`
into USL `96879bb993edc9f122fcb7978c0403215282fcfc`, preserving both histories.
This adds 288 upstream commits, including 0.23.0 and subsequent search, sync and
MCP improvements. The [downstream build guide](downstream-build.md) owns the
repository roles and release procedure.

## Combined contracts

| Contract | Previous distribution | Upstream snapshot | Combined build |
|---|---|---|---|
| Completed archive schema | 7 | 30 | 31 |
| Rebuildable analytics cache | 32 | 34 | 35 |
| HTTP API | 3.19 | 3.43 | 3.44 |

The deployed September build predates the completed archive marker. A synthetic
archive created by its exact source, `0f414b68c5a750b5354291d42320169fc0d75543`,
was upgraded twice with the combined store. Original message bytes, bodies,
labels, conversation identity, user identities and source grants stayed intact.
This is synthetic predecessor evidence, not a production-archive restore claim.

Native setup remains the schema migration owner. Readers require the completed
matching schema. Cache35 requires a real rebuild; changing a marker cannot
upgrade an old publication. Rollback after archive migration requires restoring
the predecessor backup, not merely selecting an older image.

## Preserved user outcomes

The native Messages, People, Files and Sources interface replaces superseded
components. Inbox/read filters and related-email work remain available. The
Saved Views management route retains explicit recovery of invalid historical
filters; repair preserves the stored query/schema and uses revision guards.
Native person and conversation components replace duplicate downstream panels.
Users settings and existing source grants remain available.

Named keys, OIDC sessions and delegated requests keep their existing roles and
source bindings. The new embedded MCP route forwards the actual caller through
nested daemon requests. New calendar, chat, messaging-route, tier and embedding
reads apply those bindings before ranking, pagination or counts. Shared curated
People data retains the existing shared policy. These are existing access rules,
not new Agent-specific restrictions.

Administrator source merging keeps native destination ownership, as explicitly
requested. Its timestamped preview names enabled users gaining or losing source
access to moved history, leaves existing destination history and source grants
unchanged, and explains that recorded retry assessments are historical. It adds
context, not an access-set equality rule or another approval step.

The exact-message Inbox selector remains available alongside upstream's bounded
source-deletable search. Provider mutations retain native confirmation/recovery
semantics and existing opt-ins.

## Compatibility changes

- DuckDB2 rejects deprecated lambda arrows. Four downstream cache/mailbox SQL
  expressions use its native `lambda value:` syntax. Native and CSV publication
  exercise valid, empty and malformed provider flags and check actual Parquet
  delivery facts and user-visible mailbox states.
  Incremental publication uses committed plus staged membership Parquet rather
  than rescanning unrelated old label cells; malformed label text cannot become
  role evidence or crash Unicode comparison.
- Native contexts flow into IMAP mailbox discovery/selection. Provider calendar
  tests account for both raw synchronization and expanded occurrence coverage.
- The cache snapshot schema probe closes its rows before COPY, so the native
  one-connection builder cannot deadlock on its own outstanding query.
- API and browser clients, the tool guide and schema digests are regenerated
  from the actual combined routes and database schema.
- Local reusable CI validates this checkout. Retained downstream publishers use
  the package-declared Bun and the native Windows build helper, including its
  pinned DuckDB2 static loader. The action lock covers downstream publishers.
- Linux release archives include the hash-linked native companion. Updaters
  verify both archive members before destination writes, publish a digest-named
  companion, then replace the main executable last. Prior companions remain.
  This provides safe ordering for process interruption and ordinary failures;
  it is not a claim of a power-loss durable two-file transaction.
- Native JSONv2 migration preserves the distinct all-source and empty-source
  memo scopes, historical linkage serialization and newline-delimited exports.
- Windows token-directory repair keeps current-user inheritance for shared
  OAuth lock files. Existing provider locks can recover an already stripped
  inherited ACL through an owner-verified security handle. Secret files remain
  protected, and native locks and exact owner checks stay in place. Provider
  and CardDAV publication both preserve subsequent OAuth writes and deletes.

## Evidence and limits

Synthetic browser journeys cover Inbox filter removal, explicit legacy-view
repair and native Person Files/Details at desktop and phone widths. Screenshots
illustrate layout; assertions separately exercise behavior. Their dated manifest
is on the repository's docs-assets branch.

The upstream Codex executable-isolation tests can exceed the unchanged 500 ms
version-command budget on cold macOS execution. Diagnostics reproduced a native
OS deadline, not a digest or version mismatch; warmed launches completed in
milliseconds. No containment rule or deadline was relaxed. Native test reruns and
Linux CI report their own results; a still image or isolated pass does not erase
an earlier failed attempt.
