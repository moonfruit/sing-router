# sing-router

Glossary for the sing-router project — a sing-box transparent-proxy manager for ASUS routers. This file is a shared-language reference, not a spec.

## Language

### Notifications

**Notification**:
A curated, channel-agnostic message pushed to a human about a notable state change.
_Avoid_: alert, message, push (reserve "push" for the Bark transport verb)

**clef Event**:
An observability record on the internal `clef.Bus` — the raw material a Notification may be translated from. Most Events never become Notifications.
_Avoid_: log line, notification

**Channel**:
A delivery target for a Notification (e.g. Bark). The system supports multiple Channels; Bark is the first.
_Avoid_: provider, sink (reserve "sink" for `clef.Bus` subscribers)

**Priority**:
The abstract urgency of a Notification — one of Low / Normal / High / Critical. Each Channel maps it onto its native urgency scale (Bark: passive / active / timeSensitive / critical).
_Avoid_: level (reserve "level" for the clef log-severity scale, which is a separate axis)

**Kind**:
A stable identifier of what a Notification is about (e.g. `apply.ok`), derived from a clef Event's `EventID`. Drives per-Kind configuration and Channel-side grouping/dedup.

**Catalog**:
The curated, code-defined set of Kinds that qualify for notification — and, per Kind, the title wording, body rendering, and Priority. A clef Event whose `EventID` is absent from the Catalog never becomes a Notification.

## Relationships

- A **clef Event** becomes a **Notification** only if its `EventID` is a **Kind** in the **Catalog**.
- A **Notification** is delivered to zero or more **Channels**.
- A **Notification** carries exactly one **Priority**; each **Channel** maps it onto its native urgency.
- **Priority** (Notification urgency) and clef log **level** (Event severity) are independent axes — an `Information`-level Event can still produce a `High`-priority Notification.
- The **Catalog** assigns each Kind its **Priority**; clef **level** plays no part in that assignment.

## Flagged ambiguities

- "状态变化" (state change) was used loosely to mean "anything worth telling the user about" — resolved: a Notification is produced only for **Kinds** in a curated catalog, not for every clef Event.
- "level" was overloaded — resolved: clef **level** = Event log severity; Notification **Priority** = a separate abstract urgency axis.

## Example dialogue

> **Dev:** "sing-box 子进程崩溃了，会推一条 Notification 吗？"
> **Maintainer:** "会 —— `child.crashed` 是 catalog 里的一个 Kind。但同一个崩溃在 clef 总线上是好几条 Event，只有被 catalog 收录的那条 EventID 才翻成 Notification。"
> **Dev:** "那它的 Priority 是 High 还是 Critical？"
> **Maintainer:** "看 catalog 怎么定 —— Priority 跟 clef level 没关系，由 Kind 决定。"
