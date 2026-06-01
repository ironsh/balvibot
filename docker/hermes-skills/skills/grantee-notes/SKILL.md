---
name: grantee-notes
description: Remember and recall durable facts about a grantee — preferences, status, contacts, and ad-hoc notes — using the philos-api notes tools. Use whenever the team asks you to remember, note, jot down, track, or save something about a named grantee ("remember that Acme prefers email", "note that Beta is in diligence", "what do we know about Jane?", "what have I told you about Acme?"), or to recall, update, or correct a previously saved note. Reads and writes the philos-api MCP server.
license: Proprietary
metadata:
  version: "0.1.0"
  hermes:
    tags: [grantee, notes, memory, balvi, philanthropy]
---
# grantee-notes

Durable, agent-managed memory about a grantee. When the team tells you
something worth keeping, save it as a note on the `philos-api` MCP server so you
(or a teammate) can recall it later. When they ask what you know, read it back.

Notes are written directly: unlike `add_grantee` or `whitelist_doc`, saving a
note takes effect immediately and needs no human approval.

## When to use

- **Save:** the user asks you to remember, note, track, or save something about
  a specific grantee. E.g. "remember that Acme prefers async updates", "note
  that Beta's main contact is Jane Doe", "Acme is now funded".
- **Recall:** the user asks what you know, what they've told you, or for the
  saved notes/preferences/status of a grantee.
- **Correct:** the user updates or fixes something you saved earlier.

Do not use this for email or document content — that lives in the mail and docs
tools. Notes are for things a human tells you that aren't already in the corpus.

## Tools

- `philos-api.list_grantees` — resolve a name to a `grantee_id`.
- `philos-api.create_note` — write a note. Inputs:
  - `grantee_id` (required)
  - `content` (required): the text to remember.
  - `kind` (optional): one of `note`, `fact`, `preference`, `status`,
    `contact`. Defaults to `note`. Pick the most specific fit.
  - `supersedes_id` (optional): id of a note this one replaces.
  - `signal_number` (optional): pass the requester's Signal number when the
    request came from a Signal conversation and you have it.
- `philos-api.list_notes` — read notes, newest first. Inputs: `grantee_id`
  (required), `kind`, `since`/`until` (RFC 3339 bounds on when the note was
  written), `include_superseded` (default false), `limit`, `cursor`.

## Procedure

### Saving a note

1. **Resolve the grantee.** Call `list_grantees` and match the name the user
   typed against each grantee's name and emails (case-insensitive substring).
   - If exactly one matches, use its `grantee_id`.
   - If none match, say so and stop. Do not invent a grantee — creating one is
     a separate, approval-gated action.
   - If several match, list them and ask which one. Do not guess.
2. **Choose a kind.** Map the content to the closest kind:
   - `preference` — how they like to work (channel, cadence, format).
   - `status` — where things stand (in diligence, funded, paused).
   - `contact` — a person, role, email, or phone to reach them.
   - `fact` — a stable truth about the org (focus area, location, fiscal host).
   - `note` — anything else, or when unsure.
3. **Write it.** Call `create_note` with a concise, self-contained `content`
   string. Write it so it still makes sense months later — include who/what,
   not just "they said yes". Pass `signal_number` if you have it.
4. **Confirm.** Tell the user you saved it, and to which grantee.

### Recalling notes

1. Resolve the grantee (as above).
2. Call `list_notes` with the `grantee_id`. Filter by `kind` if the user asked
   for a specific category (e.g. "what are Acme's preferences?" → `kind:
   preference`). Use `since` to bound a time window when asked ("what did I tell
   you this month?").
3. Summarize the notes plainly. Group by kind when there are several.
4. If there are no notes, say so.

### Correcting or replacing a note

1. `list_notes` to find the note that's now wrong; grab its `id`.
2. `create_note` with the corrected `content` and `supersedes_id` set to that
   id. The old note is preserved for history but hidden from the default
   `list_notes` view.
3. Do not try to delete or edit a note in place — superseding is the mechanism.

## Pitfalls

- Always resolve to the canonical `grantee_id` first; the notes tools key off
  the id, not the display name.
- `supersedes_id` must reference a note for the **same** grantee, or the write
  is refused.
- `list_notes` hides superseded notes by default. Pass `include_superseded:
  true` only when the user wants the full history or an audit trail.
- Keep `content` atomic: one fact per note. That makes superseding a single
  fact later much cleaner than editing a paragraph.
