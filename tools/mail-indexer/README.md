# mail-indexer

A small Go daemon that indexes an IMAP mailbox (typically the in-cluster
ProtonMail Bridge) into a SQLite database, attributing each message to a
*grantee*. Long-running; uses IMAP IDLE for realtime updates. Attachments are
written to a content-addressable directory keyed by SHA-256.

## Build

```sh
go build ./cmd/mail-indexer
```

CGO is required (uses `github.com/mattn/go-sqlite3`).

## Configuration

All configuration is via environment variables:

| Var | Required | Description |
|---|---|---|
| `IMAP_ADDR` | yes | `host:port`, e.g. `protonmail-bridge.philanthropy-os.svc.cluster.local:143` |
| `IMAP_USER` | yes | IMAP username |
| `IMAP_PASS` | yes | IMAP password |
| `IMAP_TLS` | no | `starttls` (default), `tls`, or `none` |
| `MAIL_DB_PATH` | yes | SQLite database file path |
| `MAIL_ATTACHMENTS_DIR` | yes | Root directory for the CAS store |
| `MAIL_GRANTEES_FILE` | yes | Path to the grantee mapping JSON |
| `MAIL_FOLDERS` | no | Comma-separated folders. Default `INBOX,Sent` |
| `MAIL_LOG_LEVEL` | no | `debug` \| `info` (default) \| `warn` \| `error` |
| `MCP_ENABLED` | no | `true` (default) starts the MCP server; `false` disables it |
| `MCP_BIND_ADDR` | no | Listen address for the MCP server, default `:8080` |
| `MCP_BEARER_TOKEN` | yes¹ | Shared secret required on every MCP request (required when `MCP_ENABLED=true`) |

The bridge uses a self-signed certificate, so the client allows insecure TLS.

### Grantee mapping

```json
{
  "grantees": [
    { "id": "acme-foundation", "name": "Acme Foundation",
      "emails": ["contact@acme.org", "jane@acme.org"] },
    { "id": "beta-collective", "name": "Beta Collective",
      "emails": ["hello@beta.example"] }
  ]
}
```

Send `SIGHUP` to reload the file without restarting.

## Schema

See `internal/store/schema.sql`. Highlights:

- `messages` — one row per RFC 5322 Message-ID, indexed by `from_addr`,
  `subject`, `thread_id`, `grantee_id`, `date`.
- `message_recipients` — exploded To/Cc/Bcc for recipient queries.
- `threads` — derived from Message-ID / In-Reply-To / References headers.
- `attachments` — points at `<MAIL_ATTACHMENTS_DIR>/<sha[0:2]>/<sha[2:4]>/<sha>`.
- `mailbox_state` — per-folder UIDVALIDITY + last indexed UID for resumable sync.

## Grantee attribution

For each new message:

1. If `From:` matches a `grantee_emails` row, that grantee owns the message.
2. Otherwise, if the thread already has a grantee, inherit it.
3. Otherwise, `grantee_id` is `NULL`.

When a sender match resolves a previously-`NULL` thread, the thread *and* all
prior `NULL`-grantee messages on it are back-filled. This is how replies in the
`Sent` folder (where we are the sender) get attributed to the right grantee.

## Local development

```sh
kubectl -n philanthropy-os port-forward svc/protonmail-bridge 1143:143

export IMAP_ADDR=127.0.0.1:1143
export IMAP_USER=...
export IMAP_PASS=...
export MAIL_DB_PATH=./mail.db
export MAIL_ATTACHMENTS_DIR=./attachments
export MAIL_GRANTEES_FILE=./grantees.json

go run ./cmd/mail-indexer
```

Then query with `sqlite3 mail.db`:

```sql
SELECT from_addr, subject, grantee_id, thread_id
FROM messages
ORDER BY date DESC LIMIT 20;

SELECT m.subject, m.from_addr
FROM messages m
JOIN message_recipients r ON r.message_id = m.id
WHERE r.email = 'jane@acme.org';
```

## MCP server

The daemon also exposes a read-only [MCP](https://modelcontextprotocol.io)
server over Streamable HTTP at `http://<host>:<MCP_BIND_ADDR>/mcp`. Every
request must carry `Authorization: Bearer $MCP_BEARER_TOKEN`. A bare
`/healthz` endpoint (no auth) is exposed for k8s probes.

Tools:

| Tool | Purpose |
|---|---|
| `list_grantees` | All grantees and their email aliases |
| `list_emails_for_grantee` | Paginated message summaries for a grantee (or `_unassigned`) |
| `get_email` | Full message by numeric id or RFC 5322 `Message-ID` |
| `list_threads_for_grantee` | Paginated threads for a grantee |
| `get_thread` | Thread metadata + all messages in chronological order |
| `search_emails` | Filter by grantee / From / Subject / Body / date range |
| `list_attachments` | Attachment metadata for a message |

In-cluster the server sits behind the `mail-indexer` `Service` on port 8080.
For local testing:

```sh
curl -H "Authorization: Bearer $MCP_BEARER_TOKEN" http://localhost:8080/healthz
```

## Tests

```sh
go test ./...
```
