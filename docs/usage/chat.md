---
title: MCP Server
description: Expose your email, chat, calendar, and meeting archive to AI assistants via MCP.
---

The MCP server operates on your msgvault archive through the selected daemon, not your live Gmail account. Without `[remote].url`, `msgvault mcp` starts or reuses the local background daemon; with `[remote].url`, it uses that remote server. The AI never sees your Google credentials, and by default it cannot change anything at your mail provider: every tool reads the local archive. Standard read and search operations go through the daemon. If [vector search](/docs/usage/vector-search/) is enabled, semantic and hybrid searches also call the embedding endpoint configured in `[vector.embeddings]`; use a local or self-hosted endpoint if message text must stay on your machine or network. The `stage_deletion` tool asks the selected daemon to save a deletion manifest, and `export_attachment` saves an attachment to a requested path on the MCP server's filesystem. Neither modifies the database, and actual deletion still requires you to run `msgvault delete-staged` from the CLI. Saved View management tools change only persistent reusable view definitions; deleting a Saved View never deletes archive messages. You control when data enters the archive (via sync and import commands) and when anything is deleted (via the explicit [deletion workflow](/docs/usage/deletion/)).

There is exactly one exception, and it is off unless you turn it on twice: [`archive_from_inbox`](#archiving-your-inbox-via-mcp) removes messages from your inbox at the provider. It deletes nothing and is reversible, but it does change your live mailbox, so it requires both `msgvault mcp --allow-mailbox-writes` and `[inbox_archive] remote_enabled = true` (or `MSGVAULT_INBOX_ARCHIVE_REMOTE_ENABLED=true`) on the daemon. With either unset, the tool is not offered and the daemon refuses the request. Compared to giving an AI assistant direct OAuth access to your mailbox, this is a fundamentally smaller attack surface.

## Setup

The `mcp` command starts a [Model Context Protocol](https://modelcontextprotocol.io/) (MCP) server that exposes your archive as a set of tools. This lets AI assistants like Claude Desktop search, read, and analyze archived email, chats, calendar events, and meeting notes directly.

### Claude Desktop Configuration

Add the following to your Claude Desktop config file:

- **macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
- **Windows**: `%APPDATA%\Claude\claude_desktop_config.json`

```json
{
  "mcpServers": {
    "msgvault": {
      "command": "msgvault",
      "args": ["mcp"]
    }
  }
}
```

If `msgvault` is not on your PATH, use the full path to the binary. Restart Claude Desktop after saving the config.

### StreamableHTTP Transport

For MCP clients that connect over HTTP instead of stdio, run:

```bash
msgvault mcp --http 8080
```

Bare ports and `:port` forms bind to loopback only, so the command above listens on `127.0.0.1:8080`. Explicit loopback addresses such as `127.0.0.1:8080` and `[::1]:8080` are also allowed.

The endpoint is `http://127.0.0.1:8080/mcp`. To require authentication, set
the API key in the configuration used by the `msgvault mcp` process:

```toml
[server]
api_key = "replace-with-a-long-random-key"
```

When `[server].api_key` is configured, every HTTP request to `/mcp` must send
the key as a bearer token, including session `GET` and `DELETE` requests:

```http
Authorization: Bearer replace-with-a-long-random-key
```

Missing or incorrect credentials return `401 Unauthorized`. Configure your
MCP client to send the header on every request. For clients that accept the
common JSON server configuration shape, that looks like:

```json
{
  "mcpServers": {
    "msgvault": {
      "url": "http://127.0.0.1:8080/mcp",
      "headers": {
        "Authorization": "Bearer replace-with-a-long-random-key"
      }
    }
  }
}
```

The key protects loopback and non-loopback listeners alike. A configured key
also permits a non-loopback `--http` address without
`--http-allow-insecure`. Without a key, non-loopback addresses remain rejected
unless you pass `--http-allow-insecure`; use that override only behind an
authenticating reverse proxy or another trusted network boundary. The built-in
listener serves plain HTTP, so put non-loopback connections behind TLS or an
encrypted private network to prevent the bearer token and archive data from
being exposed in transit.

`[server].api_key` authenticates clients connecting to this MCP HTTP listener.
It is separate from `[remote].api_key`, which authenticates `msgvault mcp` to a
selected remote msgvault daemon. Stdio transport does not use bearer
authentication.

The listener also serves its own icon at `/icon.png`, and the same mark at
`/favicon.ico`, without authentication. The server declares the first in the
MCP `icons` field so a client can show it beside the connector, including
before anyone has signed in; no client renders that field yet.

Named `[[auth.api_keys]]` entries are accepted on the same listener, and each
key sees only the tools its role permits: a `viewer` key gets the read tools, a
`member` key adds Saved View management and profile Notes (still behind
`--http-allow-writes` and `--allow-profile-writes`), and only `admin` keys,
`[server].api_key` among them, may call `export_attachment` and
`stage_deletion`. See [Configuration](/docs/configuration/#auth).

### OAuth sign-in for MCP clients

With `[auth.oidc]` configured in the `msgvault mcp` process's config (issuer,
group mapping, and `resource` set to the public MCP URL, for example
`https://mcp.example.com/mcp`), the listener is an OAuth 2.1 resource server:
it publishes `/.well-known/oauth-protected-resource`, answers unauthenticated
requests with a `WWW-Authenticate` challenge that points at it, and validates
the provider's access tokens. MCP clients discover the authorization server
from that document and sign the user in through the provider; the tools they
see follow the user's role and the token's `msgvault:read` / `msgvault:write`
scopes. Register an API resource named by the same URL at the provider with
those two permissions. The challenge asks clients for both permissions
together with the identity scopes, so one consent covers reads and writes and
the person's role decides which tools appear.

Claude Code and claude.ai connectors identify themselves with Client ID
Metadata Documents, so no client registration is needed when the provider
accepts them: allow Claude's document URLs at the provider (Pocket ID:
*Application Configuration → OIDC → Allowed metadata document URLs*), grant
metadata-document clients the two permissions on the API resource, and adding
the MCP URL in Claude is enough. The documents are
`https://claude.ai/oauth/mcp-oauth-client-metadata` for claude.ai, Claude
Desktop and mobile, and `https://claude.ai/oauth/claude-code-client-metadata`
for Claude Code; list them exactly, because a provider's wildcard may not span
path segments (Pocket ID's `https://claude.ai/*` matches neither).

MCP calls act as the signed-in person at the daemon. The listener forwards
the identity it verified — provider account, display name, and the role it
derived from the groups — and the daemon records it as a sign-in, so a person
can connect from Claude before ever opening the Web UI: their user exists
from the first tool call (see
[Users and Visible Sources](/docs/usage/users/)). If the daemon refuses them
instead — an older daemon, a sidecar key without `on_behalf_of`, or a
disabled user — tool calls fail with an `acting_user_refused` error that asks
the person to sign in to the Web UI once; on a daemon that only records
browser logins, that first sign-in creates the user.

Without metadata documents, Claude Code connects with a client registered at
the provider (a public client with PKCE and Claude Code's localhost callback):

```bash
claude mcp add --transport http msgvault https://mcp.example.com/mcp \
  --client-id <client id from the provider> --scope user
```

claude.ai custom connectors take the client ID and secret of a confidential
client under the connector's advanced settings. Static keys keep working beside
the provider for clients that cannot run an OAuth flow. Serve the listener over
HTTPS: the provider's tokens are bearer credentials.

When the daemon serves several users, the listener forwards the signed-in
person (or the user a named key is bound to) in `X-Msgvault-On-Behalf-Of`,
and for a person it verified through the provider also
`X-Msgvault-On-Behalf-Of-Identity`, so the tools see that person's
[visible sources](/docs/usage/users/) and the daemon can create the user on
first use. For the daemon to honour either header, the sidecar's
`[remote].api_key` must be a daemon `[[auth.api_keys]]` entry with
`role = "admin"` and `on_behalf_of = true`.

## Available Tools

The MCP server exposes the following tools to connected AI clients:

| Tool | Description | Parameters |
|---|---|---|
| `search_messages` | Deprecated compatibility wrapper. Omitted mode dispatches to `search_metadata`; `vector`/`hybrid` dispatch to `semantic_search_messages`. | `query` (string, required), `mode` (string: `vector`/`hybrid`), `explain` (bool), `min_score` (number), `limit` (int), `offset` (int), `account` (string) |
| `search_metadata` | Search message metadata with a subset of Gmail query syntax (not full Gmail compatibility). Matches subject, snippet, and sender/recipient metadata, not message bodies. | `query` (string, required), `limit` (int), `offset` (int), `account` (string) |
| `search_message_bodies` | Keyword full-text search inside message bodies. Returns `matches` excerpts (up to 5 per message), ordered newest-first. Backend excerpts may omit `char_offset` and `line`; use `search_in_message` when exact locations are needed. | `query` (string, required), `limit` (int), `offset` (int), `account` (string) |
| `semantic_search_messages` | Semantic search over preprocessed message subjects and bodies when [vector search](/docs/usage/vector-search/) is configured. Returns scored chunk excerpts; `min_score` filters excerpts, not ranked messages. | `query` (string, required), `mode` (string: `vector`/`hybrid`, default `hybrid`), `explain` (bool), `min_score` (number), `limit` (int), `offset` (int), `account` (string) |
| `search_in_message` | Find case-insensitive literal matches within one message body, with raw-body offsets and line numbers. | `id` (int, required), `query` (string, required), `limit` (int), `offset` (int) |
| `find_similar_messages` | Nearest-neighbor search from a seed message's embedding. Requires vector search to be configured and an active index generation. | `message_id` (int, required), `limit` (int), `account` (string), `message_type` (string), `after` (string), `before` (string), `has_attachment` (bool) |
| `search_by_domains` | Find messages where any participant (`from`, `to`, or `cc`) belongs to one of several domains, regardless of direction. | `domains` (comma-separated string, required), `limit` (int), `offset` (int), `after` (string), `before` (string) |
| `get_message` | Get message details with windowed body paging | `id` (int, required), `offset` (int), `center_at` (int), `max_chars` (int), `body_format` (string: `auto`/`text`/`html`), `full_body` (bool) |
| `list_messages` | List messages with filters | `from` (string), `to` (string), `label` (string), `after` (string), `before` (string), `has_attachment` (bool), `conversation_id` (int), `limit` (int), `offset` (int), `account` (string) |
| `get_attachment` | Get attachment content by ID | `attachment_id` (int) |
| `export_attachment` | Save attachment to filesystem | `attachment_id` (int), `destination` (string) |
| `get_stats` | Archive overview statistics. Includes vector index state when configured. | — |
| `aggregate` | Grouped statistics (top senders, domains, labels, or message volume by calendar year) | `group_by` (string: sender/recipient/domain/label/time), `limit` (int), `after` (string), `before` (string), `account` (string) |
| `list_saved_views` | List persistent reusable Saved Views and their complete definitions. Read-only. | — |
| `get_saved_view` | Get one Saved View and its canonical definition and revision. Read-only. | `id` (int, required) |
| `run_saved_view` | Execute a Saved View through Explore without reconstructing its query. Returns typed entries, groups, or files. Read-only. | `id` (int, required), `limit` (int), `cursor` (string) |
| `create_saved_view` | Create a persistent Saved View. Write-class. | `name` (string, required), `canonical_state` (object, required), `schema_version` (int, required; currently `1`), `description` (string) |
| `update_saved_view` | Patch supplied Saved View fields using optimistic revision checking. Write-class. | `id` (int, required), `revision` (int, required), at least one of `name`, `description`, `canonical_state`, `schema_version` |
| `delete_saved_view` | Delete a Saved View definition, not archive messages. Write-class and destructive. | `id` (int, required), `revision` (int, required) |
| `stage_deletion` | Stage messages for deletion (creates manifest only) | `query` (string) OR structured filters: `from` (string), `domain` (string), `label` (string), `after` (string), `before` (string), `has_attachment` (bool); optional: `account` (string) |
| `archive_from_inbox` | Remove messages from your inbox at the mail provider. Disabled unless both opt-ins are set. `confirm=true` archives; without `confirm` it returns a plan and changes nothing. `confirmation_token` with `confirm=true` archives that plan and needs no selection. | `query` (string) OR structured filters: `from` (string), `domain` (string), `label` (string), `after` (string), `before` (string), `has_attachment` (bool); optional: `account` (string), `confirm` (bool), `confirmation_token` (string) |
| `get_person_profile` | One durable person's overview from local derived state: display name, tracking, contact state (first/last contact, last inbound and outbound, interaction count, inferred channel), the curated `primary_channel`, non-sensitive attributes, current employment, typed relationships, contact points, dates, and categories. Excludes sensitive attributes, private Notes, addresses, and media; makes no provider calls. | `person_id` (int, required) |

`search_metadata`, `search_message_bodies`, `semantic_search_messages`, and `list_messages` return paginated JSON. `search_metadata` reports an exact `total`; `search_message_bodies`, `semantic_search_messages`, and `list_messages` return `total = -1` because they do not run a separate count query:

```json
{
  "data": [],
  "total": -1,
  "returned": 20,
  "offset": 0,
  "has_more": true
}
```

Use `offset` and `limit` to request subsequent pages. `search_metadata`,
`search_message_bodies`, `semantic_search_messages`, and `list_messages` default to `limit = 20` and
cap it at 50. `search_message_bodies`, `semantic_search_messages`, and `list_messages` use this
`total = -1` shape because they do not run a separate count query.
`search_metadata` accepts msgvault's local subset of Gmail-like syntax,
including case-insensitive literal `list:` and `list-id:` List-Id filters.
To restrict mixed archives to values such as `email`, `calendar_event`,
`teams`, `discord`, `sms`, or `mms`, include a `message_type:` operator in the query
(for example `message_type:teams incident review`). `find_similar_messages`
accepts a dedicated `message_type` parameter; `list_messages` does not
support message-type filtering.

`get_message` returns large bodies in windows: each response carries one
slice of the body plus `body_length`, `body_returned`, `offset`, and
`has_more`, so unusually large messages are paged across calls instead of
being returned in a single response.

### `search_metadata` and `search_message_bodies` / `semantic_search_messages` query syntax

Supported operators: `from:`, `to:`, `cc:`, `bcc:`, `subject:`, `label:` (or `l:`), `list:` (or `list-id:`), `has:attachment`, `before:`/`after:` (YYYY-MM-DD), `older_than:`/`newer_than:` (e.g. `7d`, `2w`, `1m`, `1y`), `larger:`/`smaller:` (e.g. `5M`). Bare domains on `from:`/`to:` match any address at that domain. Multiple terms are ANDed; repeated List-Id operators require every literal substring.

Not supported: negation (`-has:attachment`), `OR`, or parentheses grouping.

Free text in `search_metadata` matches subject, snippet, and sender/recipient metadata only. Use `search_message_bodies` for keyword body search or `semantic_search_messages` for vector/hybrid search over preprocessed subject and body content; both require at least one free-text term. Keyword matches literal words; semantic returns ranked messages with scored chunk excerpts. Keyword backend excerpts omit `char_offset` and `line` when the search backend does not provide efficient locations; semantic excerpts also commonly omit them because preprocessing rewrites message text. Use distinctive snippet terms with keyword `search_in_message` when raw-body navigation is needed.

### `search_in_message`

Pass a message `id` from any list or search result plus a `query`. The tool
performs case-insensitive literal matching in `body_text` and
returns an exact `total`, paginated `data`, and a `char_offset`, `line`, and
centered `snippet` for every match. Feed `char_offset` to `get_message` as
`center_at` to read a larger body window around that occurrence.

The tool defaults to `limit = 10`. For semantic search across the archive, use
`semantic_search_messages`; `msgvault mcp` does not expose a vector mode for
searching within a single message.

### `aggregate` response

`group_by=time` buckets messages by **calendar year** only. Each row's `Key` is a year string (e.g. `"2024"`). Month or day granularity is not available via MCP.

All `group_by` values return a JSON array of objects with these fields:

| Field | Description |
|---|---|
| `Key` | Grouping value (email, domain, label name, or year) |
| `Count` | Number of messages in the group |
| `TotalSize` | Sum of `size_estimate` in bytes |
| `AttachmentSize` | Sum of attachment sizes in bytes |
| `AttachmentCount` | Number of attachments |
| `TotalUnique` | Total number of distinct groups (same on every row) |

`semantic_search_messages` is always registered so callers receive actionable discovery guidance. Without vector search it exposes a reduced schema and calls return `vector_not_enabled`; with vector search it advertises the full vector parameters. `search_message_bodies` and the deprecated `search_messages` compatibility wrapper are always available. Vector and hybrid queries require at least one free-text term (operator-only queries return `missing_free_text`). They support `offset`/`limit` pagination inside the configured hybrid ranking window; when `[vector.search].max_page_size_hybrid` is positive, an `offset` at or beyond that cap returns `pagination_limit`. `min_score` filters returned chunk excerpts only and does not remove ranked messages. For deeper pagination, adjust `[vector.search].max_page_size_hybrid`.

In `semantic_search_messages` (vector/hybrid), the paginated response also includes
top-level `mode`, `pool_saturated`, and `generation` fields. When
`explain = true`, each item in `data` may include a `score` object with
the fused ranking components.

### Saved Views

Saved Views are persistent reusable query and presentation definitions shared
with msgvault's Web UI through the selected daemon. Use `list_saved_views` to
discover them and prefer `run_saved_view` over manually rebuilding a known
view's query. Execution preserves its query, full-text/semantic/hybrid search
mode, filters, grouping, presentation, and sort. Each response identifies its
`result_kind` as `entries`, `groups`, or `files` and provides the matching typed
array. Request the next page with the opaque `next_cursor` as `cursor`;
`run_saved_view` defaults to 20 results and caps each page at 50.

Semantic and hybrid Saved Views require configured, ready
[vector search](/docs/usage/vector-search/). The tool returns the vector capability
or index-state error when unavailable and never silently downgrades the view to
full-text or metadata search.

Create, update, and delete use the same Saved View validation and persistent
state as the API and Web UI. Updates patch only supplied fields. Pass the latest
`revision` returned by list, get, create, or update; a stale revision returns a
conflict so the agent can reload before retrying. An empty update description
clears it.

Stdio exposes these write-class tools. StreamableHTTP hides them by default;
pass `--http-allow-writes` only for trusted clients to expose Saved View
management, attachment export, and deletion staging over HTTP.

## Example Usage with Claude

Once configured, you can ask Claude questions like:

- *"Search my email for messages from alice@example.com about the project proposal"*
- *"How many emails did I receive last month?"*
- *"Show me the top 10 senders in my archive"*
- *"Find all messages with attachments larger than 5MB"*
- *"Stage all messages from linkedin.com for deletion"*
- *"Stage promotional emails from before 2023 for deletion"*

Claude will automatically call the appropriate msgvault tools to retrieve and analyze your messages.

## Staged Deletion via MCP

The `stage_deletion` tool lets an AI assistant help you clean up your inbox. It accepts either a Gmail-style query string or structured filters (sender, domain, label, date range), but not both at once. Results are capped at 100,000 messages per call.

When called, `stage_deletion` creates a pending deletion manifest through the selected daemon. With a remote server configured, the manifest is saved on that remote host; otherwise it is saved by the local daemon. It does **not** delete anything. To execute the deletion, you must run `msgvault delete-staged` from the CLI. See [Deleting Email](/docs/usage/deletion/) for the full workflow.

The tool returns the batch ID, message count, and next steps:

```json
{
  "batch_id": "20260224-095132-from-linkedin",
  "message_count": 150,
  "status": "pending",
  "next_step": "Run 'msgvault delete-staged' to execute deletion"
}
```

## Archiving Your Inbox via MCP

`archive_from_inbox` removes messages from your inbox at the mail provider —
Gmail's "Archive", and on IMAP a move into the account's archive folder. It
deletes nothing: archived messages keep every other label, stay searchable at
the provider, and stay in your local archive. It is reversible from your mail
client.

It is also the only msgvault MCP tool that changes anything outside the local
archive, so it is off unless you enable it in **both** places:

```bash
# 1. Let a model ask. Per session, on the MCP server.
msgvault mcp --allow-mailbox-writes
```

```toml
# 2. Let the daemon act. In the daemon's config.toml.
[inbox_archive]
remote_enabled = true
```

On a container deployment, set `MSGVAULT_INBOX_ARCHIVE_REMOTE_ENABLED=true` in
the daemon's environment instead: the archive's `config.toml` travels with your
data rather than with the deployment, and a gate that permits mailbox changes
belongs where it can be reviewed and rolled back with the stack.

The two are separate on purpose. The flag decides whether the tool is offered to
a model at all; the config decides whether the daemon will carry it out. The
daemon endpoint is reachable by anything holding your API key, not only the MCP
server, so enabling the flag alone does not hand mailbox access to every other
client — and with the config unset, the daemon refuses whoever asks.

### Archiving, and the optional plan

`confirm=true` archives the selection in one call. That is the normal path: you
already decided a model may do this when you turned on both opt-ins, so the tool
does not ask again.

Called **without** `confirm`, it changes nothing and returns a plan instead: how
many messages match, a sample of real subjects and senders so you can see
whether the selection is what you meant, and a `confirmation_token`. Reach for
that when a selection is broad or unfamiliar and you want to look before
anything moves.

```json
{
  "status": "plan",
  "account": "you@gmail.com",
  "selection": "query: label:INBOX from:newsletter before:2024-01-01",
  "message_count": 1000,
  "total_matching": 1412,
  "has_more": true,
  "sample": [
    {"from": "news@example.com", "subject": "Weekly digest", "sent_at": "2023-11-04"}
  ],
  "confirmation_token": "op2....",
  "next_step": "Nothing has changed yet. To archive exactly these 1000 messages, call archive_from_inbox again with confirm=true and confirmation_token=\"op2....\" — no selection argument is needed."
}
```

The token *is* the plan. It names the exact messages the plan covered, recorded
by the daemon when it was minted, so confirming needs nothing but the token and
`confirm=true` — the query does not have to be repeated and cannot drift into a
different set between the two calls. `selection` is there for you to read, not
to send back. A token can be spent once; a direct `confirm=true` call mints its
own over the messages it just resolved.

Add `label:INBOX` to your selection. Archiving a message that has already left
the inbox is harmless, but including such messages inflates the count and makes
it harder to see what will actually move.

At most 1000 messages move per call. `total_matching` says how many the
selection matches in total and `has_more` says whether this call covered them
all, so a backlog is worked through by repeating until `has_more` is false
rather than by guessing. Interrupting a run is safe — messages already archived
drop out of an inbox-scoped selection, so the next call picks up where the last
one stopped.

### Unattended and scheduled runs

There is no separate mandate to register. Turning on both opt-ins **is** the
standing authorization: it is explicit, it is written down where you can review
and revoke it, and it is scoped by the API key the caller presents. A scheduled
agent running under that key archives without stopping to ask, in one call —
`confirm=true` with a selection, or `confirm=true` with a plan's token.

Every run is recorded in the daemon log with the account, the selection the
caller asked for, who asked, and how many messages moved, failed and remain:

```
level=INFO msg="inbox archive completed" batch=inbox-archive-1757... account=you@gmail.com
  caller=api_key:assistant selection="query: label:INBOX from:newsletter before:2024-01-01"
  requested=412 archived=412 failed=0 remaining=0
```

A run that stopped part-way or archived nothing logs at `WARN` with the reason.

### What a fresh selection reads

Search tools read an analytics cache, which is rebuilt when messages arrive or
are deleted — not when their labels change. An archive removes a label, so a
selection resolved from that cache still offers messages a previous call
already archived, and a loop over it never converges.

When the call names one Gmail account with `account`, the selection is resolved
against the archive of record instead, and reflects what the previous call
archived immediately. Otherwise the plan sets `"stale": true` and says so in
`next_step`. Either way the archive result is authoritative: `search_metadata`
may keep reporting an archived message as `INBOX` until the analytics cache is
next rebuilt.

### Provider support

| Source | Supported | Notes |
|---|---|---|
| Gmail (OAuth) | Yes | One `batchModify` removing the `INBOX` label. No new OAuth scope: `gmail.modify` is already part of the standard grant. Accounts added with `add-account --readonly` are refused. |
| IMAP | Yes, via `query` | One `UID MOVE` per message into the `\Archive` mailbox, or a folder named `Archive`/`Archived`/`Archives`. Messages with no `Message-ID` header are skipped, because the archive could not rejoin them to their existing row after the move. Select with `query`, not the structured filters: those resolve through a Gmail-scoped path and are refused on an IMAP account rather than reporting a misleading zero. |
| Gmail over IMAP | No | These accounts sync from All Mail, never from INBOX, so the inbox copy is not addressable over IMAP. Add the account with OAuth instead. |
| Everything else | No | Slack, Teams, imported archives and the like return `unsupported_source`. |

## CLI Flags

```bash
# Start the MCP server (stdio transport)
msgvault mcp

# StreamableHTTP transport on loopback
msgvault mcp --http 8080
```

| Flag | Default | Description |
|---|---|---|
| `--force-sql` | `false` | Deprecated in 0.17.0; use `[analytics].engine = "sql"` in `config.toml` instead. See [Configuration: analytics](/docs/configuration/#analytics). |
| `--no-sqlite-scanner` | `false` | Deprecated in 0.17.0; cache engine selection is daemon-managed. Use `[analytics].engine = "sql"` for live SQL. |
| `--http` | — | Serve over MCP StreamableHTTP instead of stdio. Bare ports bind to `127.0.0.1`; non-loopback addresses require `[server].api_key` or `--http-allow-insecure`. |
| `--http-allow-insecure` | `false` | Allow non-loopback HTTP binding without `[server].api_key`. A configured key is still enforced. Without a key, use only behind your own network or authentication layer. |
| `--http-allow-writes` | `false` | Expose Saved View management, attachment export, and deletion staging tools over StreamableHTTP. Enable only for trusted, authenticated clients. |
| `--allow-profile-writes` | `false` | Expose `promote_person` and private Notes writes. |
| `--allow-mailbox-writes` | `false` | Expose [`archive_from_inbox`](#archiving-your-inbox-via-mcp), which changes your live mailbox. The daemon needs `[inbox_archive] remote_enabled = true` as well. |

Deprecated in 0.17.0: MCP analytics behavior moved from per-command flags to daemon configuration. Use `[analytics].engine` and `[analytics].auto_build_cache` in `config.toml` so local and remote daemon behavior stays consistent.

## Agent Skills

For terminal coding agents, msgvault also bundles read-only skills covering
search, attachment retrieval, and analytics. Install them into detected Claude
Code and Codex skill directories with:

```bash
msgvault skills install
```

The skills teach agents the CLI; the MCP server exposes structured tool calls.
They can be used independently or together. See [Agent Skills](/docs/guides/agent-skills/)
for installation targets, update behavior, and uninstall instructions.
