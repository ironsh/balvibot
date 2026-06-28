---
name: grantee-lifecycle
description: "Handle the complete grantee lifecycle in balvibot: creating grantees, authorizing sender emails, whitelisting Google Drive docs or folders, and explaining the approval flow. Use when the team asks to create a grantee, associate an email with a grantee, add docs, list or check approvals, or asks how these approval-gated operations work."
license: Proprietary
metadata:
  version: "0.1.0"
  hermes:
    tags: [grantee, approval, email, docs, balvi, philanthropy]
---
# grantee-lifecycle

Use this for approval-gated grantee administration in balvibot. These actions
queue requests in `approval_actions`; they do not take effect until a human
approves them with `balvi-approve` on the host.

## When To Use

- The user asks to create a new grantee.
- The user asks to associate or authorize an email address for a grantee.
- The user asks to add, whitelist, index, or authorize a Google Drive doc or
  folder for a grantee.
- The user asks what approvals are pending, executed, failed, or rejected.
- The user asks how to approve an action.

## Tools

- `balvibot-api.list_grantees`: resolve names and inspect existing grantees.
- `balvibot-api.add_grantee`: queue grantee creation.
- `balvibot-api.authorize_grantee_email`: queue sender email authorization.
- `balvibot-api.whitelist_doc`: queue Google Drive doc or folder authorization.
- `balvibot-api.add_approval_user`: queue a new approval operator.
- `balvibot-api.list_approvals`: inspect queued approval actions.

## Procedure

1. Resolve the grantee first when the action targets an existing grantee.
   - Call `list_grantees`.
   - Match by `grantee_id`, display name, or known emails.
   - If exactly one grantee matches, use that `grantee_id`.
   - If none match, say so and stop unless the user asked to create one.
   - If several match, ask the user to disambiguate.

2. For a new grantee:
   - Gather the requested id or slug and display name.
   - If the user gives only one identifier, use it as the `grantee_id` only
     when it is clearly intended as the canonical id.
   - Call `add_grantee`.
   - Tell the user the returned approval id and that it must be approved before
     the grantee exists.

3. For an email association:
   - Resolve the target grantee.
   - Confirm the email address is exactly what the user intended.
   - Call `authorize_grantee_email`.
   - Tell the user the returned approval id. Do not claim the email is active
     until approval executes.

4. For a Google Drive doc or folder:
   - Resolve the target grantee.
   - Use the Drive id exactly as provided by the user. The API detects doc vs
     folder at approval time.
   - Call `whitelist_doc`.
   - Tell the user the returned approval id and that indexing starts only after
     approval executes.

5. For approval status:
   - Call `list_approvals`.
   - If the user asks for a status, pass `pending`, `executed`, `failed`, or
     `rejected`.
   - Summarize ids, actions, status, and the relevant args.

6. Explain approval plainly:
   - The agent can queue actions but cannot approve them.
   - A human approves from the host with `balvi-approve`.
   - If an action id is known, say: `balvi-approve approve <id>`.

## Output Rules

- Be explicit that queued actions are not yet applied.
- Always include the approval id returned by the tool.
- Do not say "done" for approval-gated actions. Say "queued".
- Keep approval instructions short. The user is likely in Signal.

## Pitfalls

- `authorize_grantee_email` only affects indexing after the approval executes.
  Earlier mail may be backfilled by thread after the mapping exists.
- `whitelist_doc` does not immediately ingest content. Approval must execute,
  then the docs indexer has to sync.
- `add_grantee` can fail at approval time if the id already exists. Check
  `list_grantees` first when uncertain.
- Approval ids are numeric ids from `approval_actions`, not grantee ids.
- If a user says an approval was executed, refresh state with `list_approvals`
  or reload the relevant grantee data before assuming the change is visible.
