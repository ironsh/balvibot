---
name: grantee-status
description: Summarize a single grantee's recent activity as a short bulleted status — what they're waiting on us for, notable items, and what the most recent threads were about. Always reloads the grantee's latest emails, documents, and notes before summarizing. Use whenever the team asks for the status, state, or recent activity of one named grantee, including phrasings like "where are we with X", "what's going on with X", "status on X", "summarize comms with X", "check on X", "grantee status for X", or just "X status". Pulls data from the balvibot-api MCP server.
license: Proprietary
metadata:
  version: "0.3.0"
  hermes:
    tags: [grantee, balvi, philanthropy]
---
# grantee-status

Given a grantee's name (or partial name), reload their latest emails, documents,
and notes from the `balvibot-api` MCP server and return a short bulleted status
report. Always pull all three sources fresh on every run — never rely on what you
saw in an earlier turn, since any of them may have changed since.

## When to use

The user asks for the current status, recent activity, or a "what's going on
with" summary for a specific grantee — e.g. "where are we with Acme
Foundation?", "summarize recent comms with Beta Collective", "grantee status
for Jane", "check on Acme".

## Inputs

- `name` (required): grantee name as the user typed it. May be partial, may
  match the display name or any of the grantee's email aliases.
- `since` (optional): RFC 3339 lower bound on activity. If the user doesn't
  give one, compute 90 days ago yourself and pass it — the MCP tools have no
  built-in default and will return unbounded results otherwise.
- `limit` (optional): max threads to inspect in depth. Default: 10.

## Procedure

Always run steps 2–4 fresh on every invocation. Reload emails, documents, and
notes each time, even if you pulled them earlier in the conversation — they may
have changed.

1. **Resolve the grantee.**
   - Call `balvibot-api.list_grantees`.
   - Match `name` against each grantee's `name` and `emails`
     (case-insensitive substring). If exactly one matches, use it.
   - If zero match, report that and stop. Suggest the closest names.
   - If multiple match, list the candidates and ask the user to
     disambiguate. Do not guess.

2. **Reload the latest emails.**
   - Compute `since` if not provided. Example: if today is `2026-05-21`, 90
     days ago is `2026-02-20T00:00:00Z`.
   - Call `balvibot-api.list_threads_for_grantee` with `grantee_id` from
     step 1, the computed `since`, and `limit: 50`.
   - Sort by `last_seen_at` descending. Keep the top `limit` (default 10)
     for deep reads.
   - For each kept thread, call `balvibot-api.get_thread` to get the full
     message list. Read subjects, participants, dates, and body snippets.
   - Identify threads whose latest message is from the grantee (they sent
     the last reply) — these are what they're **waiting on us** for.
   - Note any notable items: attachments, payment references, deadlines,
     intros, escalations.

3. **Reload the latest documents.**
   - Call `balvibot-api.list_documents_for_grantee` with the `grantee_id`.
     This returns the freshest set of indexed docs with their titles and
     modified/sync times.
   - Surface docs that are new or recently modified within the window
     (compare `modified` against `since`). For those, use
     `balvibot-api.get_document_metadata` for a cheap relevance check, and
     `balvibot-api.get_document_for_grantee` to read the markdown only when a
     doc looks materially relevant to the status (a new proposal, report,
     budget, or agreement).
   - You don't need to read every doc — the goal is to catch what's changed,
     not to re-summarize the whole corpus.

4. **Reload the latest notes.**
   - Call `balvibot-api.list_notes` with the `grantee_id` to get the
     newest-first notes (preferences, status, contacts, facts). These are
     things the team told you that may not be in the email or doc corpus.
   - Use them to frame the status: a saved `status` note or `preference`
     often explains what the emails are about.

5. **Output as bullets.** Return 3–6 short bullets, in this order. Omit a
   bullet if there's nothing useful to say in it.

   - **Status:** one-clause framing of where the relationship is right now.
     Fold in any saved `status` note from step 4.
   - **Waiting on us:** the specific ask + how long it has been
     outstanding, or "nothing on our side" if the inbox is clear.
   - **Notable:** deadlines, attachments worth opening, escalations, and
     any new or recently modified documents from step 3.
   - **Recent threads:** what the most recent threads have been about —
     topics, not subject lines.
   - (Optional) **Heads-up:** anything the team should know that doesn't
     fit above — e.g. grantee hasn't checked in for >30 days, only message
     in the window is a delivery test, or a relevant note/preference on
     file.

## Output rules

- Bullets only. No headers, no tables, no prose paragraphs.
- Each bullet stands alone — one short sentence or sentence fragment. Lead
  with the bold label.
- Reference dates in natural language ("last Tuesday", "three weeks ago")
  when it reads better than `YYYY-MM-DD`.
- If the grantee has zero activity in the window, return a single bullet
  saying so and suggesting a wider `since`.

## Pitfalls

- `list_grantees` returns the canonical id (e.g. `acme-foundation`), not
  the display name. Always use the id for downstream calls.
- `get_thread` takes the internal numeric `thread_id`, not the RFC 5322
  Message-ID. `get_email` accepts either.
- The MCP tools have no built-in default for `since` — if you don't pass
  one, you get unbounded results. Always compute and pass an explicit
  `since`.
- Pagination: `list_threads_for_grantee`, `list_documents_for_grantee`, and
  `list_notes` all return a cursor when more results exist. For a status
  summary we don't need to drain it — the first page is plenty.
- `get_document_for_grantee` fails with `document_not_found` if the `doc_id`
  doesn't belong to the given `grantee_id`. Always pass the `doc_id` straight
  from `list_documents_for_grantee` for the same grantee.
- `list_notes` hides superseded notes by default; that's what you want for a
  status — only the current view matters.
- Don't skip a source because it was empty or quiet last time. Reload emails,
  docs, and notes on every run.
