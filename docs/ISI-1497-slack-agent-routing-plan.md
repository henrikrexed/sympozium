# ISI-1497 — Involve the right agent when sending a Slack message

**Status:** Draft for board approval · **Owner (scoping):** BigBoss (CEO) · **Impl owner:** CTO
**Date:** 2026-07-01 · Revision: r1

## 1. What the board asked for

> When we send a Slack message we don't know who in the ensemble crew actually
> receives it. Add (a) a new **Slack-listener** flag on the Ensemble CRD that
> defines **which agent receives** the message and involves the right agent, and
> (b) **names on agents** so that a message containing `@<name of agent>` makes
> the **main receiver delegate** the task to that named agent. Estimate the work;
> all implementation tickets go under this ticket.

Two capabilities:
- **A. Designated Slack receiver** — a declarative "this persona is the one that
  listens on Slack" so inbound messages land on a known agent instead of a
  hardcoded default.
- **B. `@name` → delegate** — the receiver parses `@<name>` and delegates the
  task to the addressed persona (main receiver stays the front door).

## 2. Current state (verified in code, 2026-07-01)

Good news: a large fraction of this already exists or is written-but-unmerged.

| Piece | State | Evidence |
|---|---|---|
| Agents have **names** | ✅ Exists | `AgentConfigSpec.Name` + `.DisplayName` — `api/v1alpha1/ensemble_types.go:174-179` |
| Per-agent Slack config | ✅ Exists | `AgentConfigSpec.SlackOptions`, `.Channels`, `.ChannelAccessControl` — `ensemble_types.go:229,245,258` |
| Delegation edges + runtime executor | ✅ Exists & **deployed** | `Relationships`/`WorkflowType="delegation"` (`ensemble_types.go:147,156`); guardrailed controller executor live in observable-llm (ISI-1462/1463/1465) |
| `@persona` / `persona:` **inbound routing** | 🟡 Written, **unmerged** | branch `fix/isi-1443-persona-routing` commit `5153fff` — `resolvePersona()`/`addressedPersona()` route an addressed inbound msg to the sibling Agent CR. Never merged to the deployed line. |
| Designated Slack **receiver** flag | ❌ Missing | inbound routing hardcodes `AgentID:"primary"` and matches by instance name only — `internal/controller/channel_router.go:372` |
| Outbound **per-persona attribution** | 🟡 Partial | send is fan-out filtered by `instanceName` only (ISI-1436), no per-persona sender identity — `internal/channel/types.go:97-119` |

**Net:** names ✅, delegation engine ✅, `@name` parsing 🟡 (exists on a shelf),
receiver flag ❌, per-persona reply identity 🟡.

## 3. Key design decision for the board

The request says the main receiver should **delegate** to the `@named` agent.
Two ways to implement the "@name → right agent" behaviour, and they differ:

- **Option 1 — Direct re-route (what ISI-1443 already built).** The channel
  router swaps the target Agent CR so the run executes *as* the addressed
  persona. Simplest, cheapest (code exists), but the "main receiver" is bypassed
  — no delegation edge, no supervisor in the loop.
- **Option 2 — Delegate through the main receiver (matches the ask literally).**
  Inbound always lands on the designated receiver; `@name` is turned into a
  delegation to that persona via the **already-deployed delegation executor**.
  The receiver stays the front door, delegation is telemetry-visible
  (`sympozium.delegation.edges`), and it reuses the guardrails (in-flight/depth
  caps) shipped in ISI-1463/1465.

**Recommendation: Option 2**, reusing ISI-1443's parse/resolve layer for the
`@name` detection and feeding it into the delegation path rather than a re-route.
This honours "main receiver delegates," gives observability for free, and keeps
one delegation mechanism instead of two routing paths. Estimate below assumes
Option 2 (a note marks the delta if the board prefers Option 1).

## 4. Proposed CRD shape

Add to `AgentConfigSpec` (`ensemble_types.go`):

```go
// SlackListener designates this persona as the default receiver for inbound
// Slack messages on the ensemble's Slack channel. Exactly one persona per
// ensemble should set this; if none do, the controller keeps today's behaviour
// (first Slack-bound persona). Addressed messages (@name) are delegated from
// this receiver to the named persona.
// +optional
SlackListener bool `json:"slackListener,omitempty"`
```

`@name` matching resolves against both `Name` and `DisplayName`
(case-insensitive), extending ISI-1443's `agent-config`-label match which today
only keys off `Name`.

## 5. Ticket breakdown (children of ISI-1497) + estimate

All implementation tickets are created under ISI-1497 and assigned to CTO.

| # | Ticket | Scope | Est. |
|---|---|---|---|
| C1 | **CRD: `slackListener` designated receiver** | Add field, kubebuilder validation (warn if >1 per ensemble), CRD regen, ensemble-controller wires the flag onto the generated Agent labels/annotations. | S (0.5–1d) |
| C2 | **Router: honour designated receiver** | Replace hardcoded `AgentID:"primary"`; inbound resolves to the `slackListener` persona (fallback = today's behaviour). Revive + rebase the useful half of ISI-1443 `5153fff`. | S (1d) |
| C3 | **`@name` → delegation** | Parse `@name`/`name:` (match Name+DisplayName), turn into a delegation from the receiver to the named persona via the deployed delegation executor; guardrails apply. Unknown name → stay on receiver + friendly note. | M (2–3d) |
| C4 | **Outbound per-persona attribution** | Stamp the acting persona on `channel.message.send` so replies from a delegated agent show the right identity (builds on ISI-1436). | M (1.5–2d) |
| C5 | **Tests + docs** | Unit tests (receiver selection, @name delegation, unknown-name fallback, no-listener fallback), `docs/concepts/channels.md` update, sample Ensemble YAML. | S (1d) |
| C6 | **Deploy + live validation** | Build fork image, roll to observable-llm, drive a real Slack thread: message → receiver → `@name` → delegated run, verify trace + delegation edge in Dynatrace. | S (1d) |

**Total: ~7–9 engineering days** (Option 2). Option 1 (direct re-route, no
delegation) drops C3/C4 to ~1d combined → **~4–5 days** but does not match the
"main receiver delegates" wording. Much of C2's code already exists on the
ISI-1443 branch, so C2 is closer to a rebase than a rewrite.

## 6. Sequencing

C1 → C2 → C3 (C3 depends on C1's flag + C2's receiver). C4 parallel to C3. C5
tracks each. C6 gates on C2+C3 merged. No hard external blockers — the delegation
executor is already live in observable-llm.

## 7. Out of scope (call out, don't silently drop)

- Multiple Slack workspaces/bots per ensemble (today: 1 ensemble → 1 Slack
  deployment). Per-agent Slack **identity** (icon/name per persona) is tracked
  separately under ISI-1449.
- Non-Slack channels (Discord already has `AllowedChats` routing; this ticket is
  Slack-scoped per the request).
