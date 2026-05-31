---
name: grantee-status
description: Summarize a single grantee's recent email activity as a short bulleted status — what they're waiting on us for, notable items, and what the most recent threads were about. Use whenever the team asks for the status, state, or recent activity of one named grantee, including phrasings like "where are we with X", "what's going on with X", "status on X", "summarize comms with X", "check on X", "grantee status for X", or just "X status". Pulls data from the philos-api MCP server.
license: Proprietary
metadata:
  version: "0.2.0"
  hermes:
    tags: [grantee, balvi, philanthropy]
---
# grantee-status

Given a grantee's name (or partial name), pull their recent email activity from
the `philos-api` MCP server and return a short bulleted status report.

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

1. **Resolve the grantee.**
   - Call `philos-api.list_grantees`.
   - Match `name` against each grantee's `name` and `emails`
     (case-insensitive substring). If exactly one matches, use it.
   - If zero match, report that and stop. Suggest the closest names.
   - If multiple match, list the candidates and ask the user to
     disambiguate. Do not guess.

2. **Pull recent threads.**
   - Compute `since` if not provided. Example: if today is `2026-05-21`, 90
     days ago is `2026-02-20T00:00:00Z`.
   - Call `philos-api.list_threads_for_grantee` with `grantee_id` from
     step 1, the computed `since`, and `limit: 50`.
   - Sort by `last_seen_at` descending. Keep the top `limit` (default 10)
     for deep reads.

3. **Read thread contents.**
   - For each kept thread, call `philos-api.get_thread` to get the full
     message list. Read subjects, participants, dates, and body snippets.
   - Identify threads whose latest message is from the grantee (they sent
     the last reply) — these are what they're **waiting on us** for.
   - Note any notable items: attachments, payment references, deadlines,
     intros, escalations.

4. **Output as bullets.** Return 3–6 short bullets, in this order. Omit a
   bullet if there's nothing useful to say in it.

   - **Status:** one-clause framing of where the relationship is right now.
   - **Waiting on us:** the specific ask + how long it has been
     outstanding, or "nothing on our side" if the inbox is clear.
   - **Notable:** deadlines, attachments worth opening, escalations.
   - **Recent threads:** what the most recent threads have been about —
     topics, not subject lines.
   - (Optional) **Heads-up:** anything the team should know that doesn't
     fit above — e.g. grantee hasn't checked in for >30 days, only message
     in the window is a delivery test.

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
- Pagination: `list_threads_for_grantee` returns `next_cursor` when more
  results exist. For a status summary we don't need to drain it — the
  default `limit` is plenty.
