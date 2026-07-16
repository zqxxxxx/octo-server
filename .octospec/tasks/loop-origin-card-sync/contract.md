# Loop origin card cross-repository contract

Status: implementation contract, 2026-07-15.

## 1. Origin context

`octo-web` sends origin context only when the user creates a Loop from an
authenticated conversation surface:

```json
{
  "origin_context": {
    "space_id": "space_123",
    "channel_id": "group_123",
    "channel_type": 2,
    "message_id": "optional-source-message-id",
    "initiator_uid": "user_123"
  }
}
```

`octo-multica/server` does not trust `space_id` or `initiator_uid` from the JSON body.
It replaces them with authenticated request context and verifies the exact
group/thread before persisting the immutable binding. `channel_type` must be an
Octo group or community-topic value. A thread must resolve to an active parent
group in the same Space.

## 2. Loop projection fields

The Loop service provides raw, non-localized values to `octo-server`:

```json
{
  "variant": "active",
  "issue_id": "uuid",
  "workspace_id": "uuid",
  "identifier": "LOOP-42",
  "title": "Prepare launch review",
  "summary": "Prepare the launch plan",
  "lifecycle_status": "in_progress",
  "priority": "high",
  "assignee_name": "Alice",
  "due_date": "2026-07-20",
  "progress": "Draft is ready for review",
  "revision": 8,
  "updated_at": "2026-07-15T10:30:00+08:00",
  "confirmation": {
    "id": "confirmation_uuid",
    "reviewer_uid": "octo_user_2",
    "prompt": "Approve publishing the launch plan?"
  }
}
```

Card copy, labels, layout, escaping, truncation, Adaptive Card JSON, metadata,
and plain fallback are authored by `octo-server`. Tokens and credentials are
never accepted in this contract.

### 2.1 `/issue` execution ownership

An Octo `/issue` command creates one origin-bound Loop through
`IssueService.Create`. Assignment inside that service owns the single Issue
Task that performs the work. The dispatcher must not enqueue a second Chat
Task for the same command. The initial Loop projection card is the durable
conversation acknowledgement, and subsequent Issue Task progress updates that
card through the projection queue.

## 3. Delivery operations

The implementation uses the existing authenticated Bot API with
`Authorization: Bearer <bot-token>`. It accepts only structured Loop fields and
never caller-authored type-17 payloads.

- `send`: dispatch a new card to the immutable exact origin and return
  `message_id`, `message_seq`, and `client_msg_no`. Every immutable frame uses
  a stable, bounded `client_msg_no`, so a worker retry remains one visible
  message. `mention_reviewer=true` is accepted only when the structured card
  has a pending reviewer and is used for the one real reviewer mention on a
  new confirmation/reviewer-replacement frame.
- `edit`: replace one existing Loop card frame after verifying sender, origin,
  Loop metadata, and monotonically valid `card_seq`.

`octo-multica/server` persists delivery intent and result. `octo-server` performs one
transport attempt per request and returns a bounded category such as
`delivered`, `target_denied`, `stale`, `busy`, or `dispatch_failed`.

The remote message anchor and the projection's active-frame pointer are
committed atomically after transport success. Processing jobs older than the
stale threshold are recovered periodically while the worker remains running,
not only during process startup. When the retry limit is reached, one durable
Inbox notification batch is written for the Loop creator plus current
workspace owners/admins; this diagnostic path never rolls the Loop domain
state back.

## 4. Action.Submit

The server-authored action data contains identifiers, never authorization:

```json
{
  "operation": "loop.confirm",
  "issue_id": "uuid",
  "confirmation_id": "confirmation_uuid",
  "loop_revision": 8,
  "reviewer_uid": "octo_user_2"
}
```

The Web request continues using the existing
`POST /v1/message/card/action` envelope. Static `data` is extracted from the
stored active frame by `octo-server`; the client cannot override it.

Before the action is applied, the Loop domain service re-reads current state
and verifies:

- `operator_uid == reviewer_id` and matches the current pending record;
- current Loop visibility and exact-origin group membership;
- the named confirmation record is still `pending` (independent of the Loop
  lifecycle status);
- all revisions point to the active card;
- the action idempotency identity has not already been applied.

Success creates a new domain event. The action handler never directly edits the
card or skips the Loop service.

## 5. Viewer-aware actions

All group members may read pending-confirmation content. The action carries a
server-authored reviewer visibility hint so `octo-web` hides it from other
viewers. This is presentation only; backend checks above are the security
boundary. A reviewer who is no longer in the origin group sees no quick action
and must use Loop detail after membership or reviewer correction.

## 6. Superseded frames

A superseded frame contains only Loop identity, a neutral `Superseded` label,
one primary reason, optional compact secondary changes, and the instruction to
use the newer card below. It contains no actions, inputs, live assignees,
deadline, progress, comments, or AgentTask state.
