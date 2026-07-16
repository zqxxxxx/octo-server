---
type: Task
title: "Task: loop-origin-card-sync"
description: Project Loop state into its immutable origin group or thread using versioned Adaptive Cards, with reviewer-only actions and durable delivery coordination.
tags: ["loop", "matter", "card", "group", "thread", "space", "isolation", "acl", "idempotency", "revision", "testing"]
timestamp: 2026-07-15T16:00:00+08:00
slug: loop-origin-card-sync
source: self
---

# Task: loop-origin-card-sync

## Goal

Project an Octo Loop into the exact group or thread in which it was created.
Ordinary progress edits the current card in place. A key revision sends a new
authoritative card first and only then collapses the previous card into a
read-only superseded frame. Loop state remains authoritative when delivery
fails.

This is a cross-repository change:

- `octo-multica/server` owns Loop state, immutable origin provenance, business events,
  revision coordination, retry state, reviewer authorization, and idempotency.
- `octo-server` owns trusted Bot identity, live Space/group/thread
  authorization, server-authored Card JSON, message send/edit transport, and
  the authenticated card-action ingress.
- `octo-web` owns Adaptive Card rendering, viewer-aware action visibility,
  transient submit state, deep-link navigation, and visual fallback.

## Frozen product decisions

1. There is no proactive share-to-group feature in this phase. A Loop without
   complete creation-time `origin_context` never starts group synchronization.
2. A main-group origin sends only to that group. A thread origin sends only to
   that exact thread and never duplicates into its parent group.
3. The origin is immutable. Completion workers, retries, and action callbacks
   cannot select or replace the destination.
4. Creation plus immediate Agent start produces one first card after the first
   workflow settles or the bounded initial wait expires.
5. Title, description, priority, ordinary progress, and non-lifecycle Agent
   progress edit the active card in place and do not bump the conversation.
6. Lifecycle, assignee, deadline, blocked/unblocked, pending confirmation,
   confirmation result, done, cancelled, and reopen are key revisions.
7. A key revision sends the new card first. The previous card becomes
   superseded only after the new send succeeds.
8. A pending confirmation has exactly one `reviewer_id`. Only that current
   reviewer may see a quick action, and the server always rechecks identity,
   current group membership, Loop access, current lifecycle, and revisions.
9. Quick actions use `Action.Submit`; `Action.Execute` remains unsupported.
10. Comments, reactions, comment resolution, and Agent Tasks that do not map to
    a Loop lifecycle event never update the group card.
11. Done/cancelled produce a final card with only `View Loop`; reopen creates a
    new active card; delete creates a buttonless tombstone.
12. Outbound webhook routing and sender selection are out of scope.

## Event and revision contract

- New write paths emit an `event_id`; derived work preserves `causation_id`.
- Multiple field changes from one `event_id` create at most one card revision.
- Different `event_id` values are independent even when less than five seconds
  apart.
- Only legacy events missing both identifiers enter a fixed five-second window
  keyed by Loop, actor, and source. The window never slides. Terminal,
  pending-confirmation, and confirmation-result events may flush it early.
- `loop_revision` advances with authoritative Loop data. `card_revision`
  advances when the rendered projection changes. New-message identity changes
  only for key revisions; in-place edits preserve the message identity while
  monotonically advancing that message's `card_seq` compare-and-swap version.

## Load-bearing list

- Immutable origin fields and their creation-time provenance.
- Space isolation and live group/thread authorization at every send and action.
- Single-reviewer authorization and idempotent action handling.
- New-first/old-second revision ordering and stale action rejection.
- Trusted `loop-origin` producer identity and `octo/v2` profile.
- Durable delivery state and retry classification in `octo-multica/server`.
- Card message send/edit CAS and server-authoritative `plain` fallback.
- No secrets, tokens, comments, or untrusted HTML in Card JSON or logs.

## Out of scope

- User-selected destination, forwarding, or multi-conversation subscription.
- Outbound webhook delivery.
- Inline comments, comment counters, reactions, or live Agent reasoning in a
  Loop card.
- Multi-reviewer approval, quorum, or first-responder approval.
- `Action.Execute` or client-side DOM mutation as authoritative state.
- Mobile-specific presentation changes in this phase.

## Acceptance

- Complete origin data is persisted at authenticated Loop creation; partial or
  forged origin data is rejected, and origin fields are immutable thereafter.
- Valid group and thread origins each receive one first `octo/v2` card from the
  trusted Loop producer; no other conversation receives a copy.
- Ordinary progress changes only the active message frame. A key revision sends
  one new message and then replaces the previous frame with a compact,
  actionless superseded card.
- New-send failure leaves the old card active. Old-edit failure leaves the new
  card authoritative, rejects old actions, and enters bounded retry.
- Duplicate events and callbacks do not duplicate business actions or cards.
- Only the current reviewer can execute a pending-confirmation action. Forged,
  stale, non-member, or non-reviewer submissions fail closed.
- Final, reopened, deleted, comments-only, and AgentTask-only scenarios follow
  the frozen decisions above.
- Card fixtures cover active, pending, superseded, done, cancelled, deleted,
  loading/submitting, error, and no-permission states; all fixtures validate
  against the Octo profile and have server-authored plain fallback.
- Web visual QA covers 1440, 1024, and 720 widths, light/dark themes, keyboard
  focus, reduced motion, and fallback rendering.
