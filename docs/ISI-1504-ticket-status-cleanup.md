# ISI-1504 — Ticket-status cleanup

**Date:** 2026-07-01 · **Owner:** BigBoss (CEO)

Board ask: *"go through the tickets and update the status … some were cancelled because managed by another contributor … lots of tickets, feels messy."*

## Starting state

1,497 total issues. **232 non-terminal** (the "mess"):

| status | count |
|---|---|
| in_review | 82 |
| blocked | 80 |
| backlog | 39 |
| todo | 25 |
| in_progress | 6 |

178 of the 232 had not been touched in >2 weeks; 101 were >30 days stale.

## Actions taken this heartbeat

### A. Closed directly (CEO boundary) — 30 tickets

**Marked `done` (work complete & live-verified):** ISI-1458 (deploy done, rev 9), ISI-1426 (dup-reply fixed live).

**Cancelled — Slack surface handed to upstream community (#245 @mvanhorn/@AlexsJones):** ISI-1409.

**Cancelled — Paperclip auto-generated productivity-review wrappers (system noise):** ISI-1233, ISI-1239, ISI-1245, ISI-1250, ISI-1254, ISI-1368.

**Cancelled — stale automated CI-failure bot notifications (81d):** ISI-395, ISI-396, ISI-397, ISI-398.

**Cancelled — empty/vague/ancient tickets with no active scope:** ISI-1405 (empty "test"), ISI-6, ISI-7, ISI-9, ISI-22, ISI-25, ISI-29, ISI-30 (91d project-inception), ISI-251, ISI-643, ISI-732, ISI-841, ISI-842, ISI-1122, ISI-1123, ISI-1129.

### B. Delegated to owning agents (outside CEO edit boundary — 403) — 3 child issues

The control plane blocks the CEO from editing issues assigned to other agents, so these went back to their owners with a decided disposition table (mechanical close-out, no new engineering):

- **ISI-1511 → ProxOps** — close 11 Slack/eventbus tickets: `done` for ISI-1403, ISI-1430, ISI-1431, ISI-1460 (all deployed & live-verified); `cancelled` for ISI-1411, ISI-1429, ISI-1442, ISI-1443, ISI-1454, ISI-1455, ISI-1456 (Slack → community, plan rev2).
- **ISI-1512 → Architect** — cancel ISI-1435, ISI-1449 (Slack attribution superseded by community #245).
- **ISI-1513 → ProxOps** — reconcile + close the 14-ticket VM-disk cleanup chain (ISI-848/849/859/860/863/877–885) + status on ISI-1113/1142/1389.

**Net effect once B lands: ~46 tickets cleared** (30 direct + 16 delegated).

## Remaining open — needs board direction (initiative go/no-go)

These are grouped by initiative because most are owned by other agents and only the board knows which initiatives are still alive. Once decided, closures will be delegated to owning agents.

| Initiative | Open tickets | Notes / recommendation |
|---|---|---|
| **Content: news shows** (Cloud Native Weekly/News + OTel News) | ~23 | Many are dated editions (Apr–Jun) long past due. **Recommend:** cancel all past-dated editions; keep only the current/latest edition + the show-format template if the series continues. |
| **Conference talks / CFP** (OSS Summit OpenClaw, ObsSummit "Zelda"/gatewayapiprocessor, Agent-Memory & Sandboxing CFPs) | ~20 | 55–70d for OSS Summit/Zelda. **Recommend:** confirm which talks are still being submitted; cancel the rest as subtrees. |
| **Livestreams/episodes** (Cedar Policy, GitHub Radar Ep 1, Observe&Resolve security) | ~15 | Cedar (39d) & Ep-1 (59d) appear dormant; Observe&Resolve (6d) is fresh. **Recommend:** kill Cedar + Ep-1 subtrees unless still planned. |
| **GitHub Radar / discovery** (V2 epics, Path A/B/C, Stage B/C) | ~22 | Large stalled program (15–88d). **Recommend:** board decides continue vs shelve; if shelve, cancel V2 epics + discovery experiments. |
| **Observability plugin / dashboards** | ~59 | Mixed: some active (ISI-1358/1483, 1–7d), many stale plugin-port tickets (74d). Needs a per-owner sweep. |
| **Paperclip platform / onboarding** | ~9 | Several BigBoss-owned & active-ish (upgrade fork, simplify onboarding). Keep the live ones; close duplicates. |
| **OTel Enterprise Book** | 2 | ISI-1345/1346, 13d — keep if book is active. |

## How to proceed

Board answers the initiative-level questions on ISI-1504 (interaction posted). For each initiative marked "kill/shelve", the CEO delegates a single subtree-close task to the owning agent (same mechanical pattern as ISI-1511/1512/1513). Active initiatives keep their tickets.
