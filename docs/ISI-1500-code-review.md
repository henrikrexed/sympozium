# ISI-1500 C3 — `@name` inbound → delegate to named agent — Code Review

**Reviewer:** Amelia (Code Reviewer) · **Date:** 2026-07-01
**Change under review:** commit `b9fd1a9` on `isi1436-integration-test`
**Files:** `internal/controller/channel_router.go` (+104), `internal/controller/channel_router_slack_routing_test.go` (±6)

## Verdict

**Changes requested — NOT approved.** 1 acceptance gap (needs board/dev decision), 1 high
correctness bug that can silently drop legitimate Slack messages. Build/vet clean, 13 routing
unit tests pass, but the highest-risk code paths are untested.

## Verification performed

- `go vet ./internal/controller/` — clean.
- `go test ./internal/controller/ -run 'Slack|Delegate|Receiver|Listener'` — PASS.
- `gofmt -l` — source file clean; **test file is NOT gofmt-clean** (struct alignment, see L1).
- Confirmed label key `sympozium.ai/ensemble` matches `ensemble_controller.go:605`.
- Confirmed delegate instance name `{ensemble}-{persona.Name}` matches `ensemble_controller.go:288`.

---

## Findings (triaged)

### A1 — ACCEPTANCE GAP (blocker until board/dev confirms): mechanism differs from approved Option 2

The issue specifies delegation *"via the already-deployed guardrailed delegation executor
(relationships + controllerExecutor, WorkflowType delegation) — receiver stays the front door."*

The implementation does **not** use the delegation executor. `handleInbound`
(`channel_router.go:351-401`) performs a **direct instance swap**: it reassigns `inst` to the
delegate's Agent CR, rewrites `msg.InstanceName`, and creates a normal `AgentRun` for the
delegate (`AgentRef: msg.InstanceName`). Consequences of the divergence:

- **No relationship enforcement.** Any persona reachable by `@name` is invoked regardless of
  whether the receiver has a declared delegation relationship to it. The guardrailed executor
  (ISI-1463) would enforce relationships + depth/in-flight caps; the direct swap bypasses all of it.
- **No delegation telemetry.** The `sympozium.delegation.edges` / handoff-latency instrumentation
  (ISI-1467) never sees these hops — they are plain channel AgentRuns, not delegation edges.
- **"Front door" is only partial.** The reply still posts to the receiver's channel/thread, but
  the run itself is owned by the delegate and outbound is attributed to the delegate
  (`agentDisplayName(inst=delegate)`, ISI-1501).

This may be an intentional simplification, but it is a citable divergence from the written AC and
the approved design. **The board/CTO must confirm** whether direct-swap satisfies Option 2 or
whether this must route through the delegation executor. Everything below assumes direct-swap stays.

### H1 — HIGH (correctness): `name:` / `@` prefix false-positives silently drop legitimate messages

`extractNameMention` (`channel_router.go:657-680`) treats **any** message beginning with
`word:` or `@word` as a persona mention. On no match, `handleInbound` posts a denial and
`return`s — **the message is dropped and never becomes an AgentRun**.

Real messages this swallows (each yields *"Sorry, I don't know who X is."* and is discarded):
- `Note: the build is broken` → mention `Note`
- `TODO: fix flaky test` → mention `TODO`
- `FYI: deploy at 5pm`, `Q: how do I…`, `Re: incident` → mentions `FYI`/`Q`/`Re`
- `https://example.com please look` → mention `https` (colon before `//`)
- `@here can someone review` / `@channel` → mention `here`/`channel`

Recommendation: on **no-match**, fall through to the receiver instead of dropping — at least for
the ambiguous `name:` form and for Slack keywords (`@here`, `@channel`, `@everyone`). Reserve the
friendly "unknown persona" note for an explicit, unambiguous `@persona` that clearly targeted a
persona. This bug alone will regress everyday Slack usage.

### M1 — MEDIUM: access-control / mute checks evaluated against the delegate, not the receiver

The `@name` redirect runs **before** `checkChannelAccess` and the stop/start mute triggers
(`channel_router.go:402+`). After the swap `inst = delegate`, so access control is evaluated
against the *delegate's* channel rules. A delegate persona without the originating channel in its
access config will have its redirected message denied — surprising for a "front door" model. Also,
the unknown-mention denial (H1 path) is sent even in a muted channel and before any access check.
Decide and document whether access is the receiver's responsibility (front door) or the delegate's.

### M2 — MEDIUM: highest-risk paths are untested

Unit tests cover `resolveSlackReceiver` and `resolveNamedDelegate` only. **`extractNameMention`
has zero tests** despite being the source of H1, and the `handleInbound` redirect / unknown-drop
wiring is untested. Add table tests for `extractNameMention` (including the H1 false-positives) and
an envtest/fake-client test for the redirect + unknown-drop branches.

### L1 — LOW: test file is not gofmt-clean

`gofmt -l` flags `channel_router_slack_routing_test.go` (struct-field alignment in
`TestResolveSlackReceiver`). CI gofmt gates will fail. Run `gofmt -w`.

### L2 — LOW: `resolveSlackReceiver` is dead code in production

Defined + tested but only referenced by tests; `handleInbound` never calls it (still uses the
name-match loop at `:340`). Harmless (Go allows unused funcs) but either wire it into receiver
selection or note it's staged for a sibling C-task to avoid drift.

### L3 — LOW: unknown-name reply enumerates all personas

The denial lists every `AgentConfig` display name indiscriminately, including non-Slack/internal
personas. Minor information exposure; consider listing only slack-reachable personas.

---

## What's correct (credit)

- Label key `sympozium.ai/ensemble` and delegate instance name `{ensemble}-{persona.Name}` both
  match the ensemble controller exactly.
- Case-insensitive match against **both** `Name` and `DisplayName` — satisfies the ISI-1443 gap.
- Safe fall-through when the Ensemble `Get` fails or the delegate Agent CR is missing (logs +
  stays on receiver) — no panic, no orphaned routing.
- Trace attributes stamped for delegate / mention / unknown_mention — good observability hooks.
