# gdocs-indexer

A small Go daemon that mirrors a curated set of Google Docs into SQLite and
exposes them to agents over MCP. Egress to Google goes through `iron-proxy`,
which uses its [gcp_auth transform](https://docs.iron.sh/credential-proxying/gcp-auth)
to substitute a real service-account access token for the literal stub
`iron-proxy-stub-token` at the wire — so the indexer pod holds no Google
credentials.

## Trust model

The Google service account (e.g.
`balvi-indexer@balvi-project.iam.gserviceaccount.com`) is not a secret —
anyone can share a doc with it. The indexer therefore distinguishes:

- **Registered docs**: owned by a grantee's allowlisted `owner_email` AND
  reached via an allowlisted source (folder or direct doc id). These land
  in the main `docs` corpus that agents see.
- **Unregistered docs**: anything else shared with the SA. These land in
  `unregistered_docs` for separate human triage. The MCP server never
  exposes them.

## Build

```sh
go build ./cmd/gdocs-indexer
```

CGO is required (uses `github.com/mattn/go-sqlite3`).

## Configuration

All env vars:

| Var | Required | Default | Notes |
|---|---|---|---|
| `IRON_GDOCS_DB_PATH` | yes | | SQLite path |
| `IRON_GDOCS_POLL_INTERVAL` | no | `5m` | Go duration |
| `IRON_GDOCS_MCP_LISTEN_ADDR` | no | `127.0.0.1:8800` | MCP HTTP bind |
| `IRON_GDOCS_MCP_BEARER_TOKEN` | yes¹ | | Shared secret. Required when MCP enabled |
| `IRON_GDOCS_MCP_ENABLED` | no | `true` | |
| `IRON_PROXY_URL` | yes | | Informational; egress goes via dnsConfig + MITM |
| `IRON_GDOCS_BROKER_TOKEN` | no | `iron-proxy-stub-token` | Placeholder Bearer the iron-proxy `gcp_auth` transform substitutes |
| `IRON_PROXY_CA_FILE` | no | `/etc/ssl/iron-proxy/ca.crt` | Trust anchor for TLS to googleapis.com |
| `IRON_GDOCS_DRIVE_BASE_URL` | no | `https://www.googleapis.com/drive/v3` | Hidden test override |
| `IRON_GDOCS_LOG_LEVEL` | no | `info` | `debug` / `info` / `warn` / `error` |

## CLI

```
gdocs-indexer run                            # daemon: poll loop + MCP
gdocs-indexer sync-once                      # one cycle, exit
gdocs-indexer serve-mcp                      # MCP only, no poll loop
gdocs-indexer register-grantee \
    --id <slug> --owner-email <email> [--display-name <name>] \
    [--folder <drive-folder-id>]... [--doc <drive-doc-id>]...
gdocs-indexer add-source --grantee <id> (--folder <id> | --doc <id>)
gdocs-indexer list-grantees
```

All subcommands read `IRON_GDOCS_DB_PATH` from the environment.

To onboard a new grantee against a running deployment:

```sh
kubectl -n philanthropy-os exec deploy/gdocs-indexer -- \
  gdocs-indexer register-grantee \
    --id acme-foundation --owner-email contact@acme.org \
    --folder 1a2b3c-folder-id
```

The next poll cycle will pick up the new sources without any pod restart.

## Sync algorithm

Each `IRON_GDOCS_POLL_INTERVAL`:

1. Bump a monotonic `cycle_counter` in `sync_state`.
2. For each `active` grantee, walk every `grantee_sources` entry:
   - folder: paginate `files.list?q='<id>' in parents and trashed=false`,
     recursing into subfolders.
   - doc: `files.get?fileId=<id>`.
3. For each discovered file:
   - Verify `mimeType == application/vnd.google-apps.document`. Skip otherwise.
   - Verify `owners[0].emailAddress == grantee.owner_email`. Skip + log on
     mismatch (policy violation).
   - If `modifiedTime` is unchanged from the existing row: bump
     `last_seen_cycle` and continue (no re-export).
   - Else: `files.export?mimeType=text/markdown` and upsert the row.
4. Page `files.list?q=sharedWithMe=true and trashed=false`. Anything not
   already a registered source lands in `unregistered_docs` (or has its
   `last_seen` bumped). Existing status is preserved (humans own it).
5. Any `active` doc that wasn't seen this cycle gets flipped to `stale`.
   Markdown is kept — staleness is a signal, not an eviction.

Per-doc errors do not fail the cycle. 403/404 from `files.export` marks the
row stale but preserves cached content (acceptance criteria #5).

## Schema

See `internal/store/schema.sql`. Tables:

- `grantees` — registry of who exists
- `grantee_sources` — which folders/docs map to each grantee
- `docs` — registered corpus
- `unregistered_docs` — shadow inbox (not exposed by this MCP)
- `blocked_owners` — created for forward compat; not read in v1
- `sync_state` — cycle counter, last_successful_sync

## MCP server

Read-only Streamable HTTP at `http://<host>:<MCP_LISTEN_ADDR>/mcp`. Every
request must carry `Authorization: Bearer $IRON_GDOCS_MCP_BEARER_TOKEN`. A
bare `/healthz` endpoint (no auth) is exposed for k8s probes.

| Tool | Purpose |
|---|---|
| `list_grantees` | All grantees + document counts |
| `list_documents_for_grantee` | Paginated doc summaries for a grantee |
| `get_document_for_grantee` | Full markdown by `(grantee_id, doc_id)`. Wrong grantee → `document_not_found` |
| `get_document_metadata` | Same minus the markdown body |

The MCP server NEVER reads `unregistered_docs` or `blocked_owners` — those
belong to a separate human-triage MCP that is out of scope here.

## Local development

There is a hidden `IRON_GDOCS_DRIVE_BASE_URL` env var for pointing at a
fake Drive endpoint:

```sh
export IRON_GDOCS_DB_PATH=./gdocs.db
export IRON_PROXY_URL=http://localhost:8888       # any non-empty value
export IRON_GDOCS_MCP_BEARER_TOKEN=test-token
export IRON_GDOCS_DRIVE_BASE_URL=http://localhost:5500   # your fake server
go run ./cmd/gdocs-indexer register-grantee --id a --owner-email o@a.org --folder folder-1
go run ./cmd/gdocs-indexer sync-once
```

## Tests

```sh
go test ./...
```
