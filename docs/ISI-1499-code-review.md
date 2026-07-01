# ISI-1499 Code Review — Channel router: honour designated Slack receiver

**Reviewer:** Amelia (Code Reviewer) · **Date:** 2026-07-01
**Commit under review:** `487d16c` (current tip incorporates `4221513`/`65f9931` ISI-1521 fixes to same file)
**Scope:** `internal/controller/channel_router.go` — `handleInbound` SlackListener routing + AgentID resolution

## Verdict: 🟢 GREEN-LIGHT

0 critical · 0 high · 1 medium · 2 low. Change is correct, contained, and safe to merge. The Medium is a test-coverage gap, not a defect.

## Acceptance (vs ISI-1499 description)

| AC | Status | Evidence |
|----|--------|----------|
| Replace hardcoded `AgentID:"primary"` | ✅ | `channel_router.go:490` now `AgentID: agentID`; derived at `:446-449` |
| Unaddressed inbound resolves to `slackListener=true` persona | ✅ | SlackListener block `:423-441` via `resolveSlackReceiver` `:686` |
| Fallback = today's behaviour when none set | ✅ | `resolveSlackReceiver` returns nil → no swap → stays on receiving inst; `agentID` falls back to `"primary"` for label-less standalone Agents |
| Revive useful half of `fix/isi-1443` (`resolvePersona`/`addressedPersona`) | ✅ | Landed as `resolveSlackReceiver` + `resolveNamedDelegate` |

## Blind Hunter (correctness)

- **Downstream safety of AgentID change — VERIFIED SAFE.** `AGENT_ID` env is set in the run pod (`podbuilder.go:187`, `agentrun_controller.go:2502`) but **not consumed** by the runner (only `AGENT_RUN_ID` is). Child sequential/delegation persona derivation reads `sourceInst.Labels["sympozium.ai/agent-config"]` (`agentrun_controller.go:1086/1276`), **not** `Spec.AgentID`. So flipping `"primary"` → persona name has no behavioural blast radius beyond labels/attrs — exactly the stated intent.
- **`@name` swap stamps correct AgentID — VERIFIED.** After a `@name` redirect, `inst` is the delegate, so `agentID` = delegate's `agent-config` label. Matches commit-message claim.
- **Per-instance run query correctness — VERIFIED.** Ensemble controller queries runs by `sympozium.ai/instance` (`ensemble_controller.go:883`), which already tracks the swapped `msg.InstanceName`. The added `sympozium.ai/agent-config` run label (`:485`) is additive metadata, not load-bearing for that query — harmless.

## Edge Case Hunter

- SlackListener Agent CR not yet reconciled → `Client.Get` err → error-logged, **no swap**, graceful fallback to receiving inst (`:435-436`). ✔
- Self-swap guard `listenerInst.Name != inst.Name` (`:430`) prevents redundant reassignment when the receiver already *is* the listener (silent no-op). ✔
- SlackListener swap preserves full `msg.Text` (unlike `@name`, which strips the mention) — correct for unaddressed messages. ✔

## Findings

### M1 (Medium) — Core swap path has no integration test
`TestHandleInbound_NameRouting` only exercises ensembles where the **receiver already is** the `slackListener` persona (`triage`). The load-bearing swap branch `:431-434` (message arrives at inst A → routed to a *different* SlackListener persona B) is never executed by a test, and neither the resolved `AgentID` value nor the ensemble/agent-config run labels are asserted. `resolveSlackReceiver` has good unit coverage, but the `handleInbound` integration of it does not.
**Recommend:** add a case where `receiver.InstanceName` ≠ the `slackListener` persona and assert (a) `AgentRun.Spec.AgentRef` = listener instance, (b) `Spec.AgentID` = listener persona name, (c) run carries `sympozium.ai/ensemble` + `sympozium.ai/agent-config`.

### L1 (Low) — Redundant Ensemble Get on word:-prefix fall-through
When `extractNameMention` returns a non-matching `name:` token, the Ensemble is fetched once in the `@name` block (`:369`) and again in the SlackListener block (`:426`). Two `Client.Get` for one message. Cache-backed, negligible, but avoidable by threading the already-fetched ensemble through.

### L2 (Low / nit) — Stale doc comment on `resolveSlackReceiver`
`:684-685` says "callers fall back to the first Slack-bound agent," but the actual caller falls back to the **receiving inst**. Reword to match.

## Verification

- `go test ./internal/controller/ -run 'TestHandleInbound_NameRouting|TestResolveSlackReceiver|TestNoListenerFallback|TestExtractNameMention|TestResolveNamedDelegate'` → `ok`
- `gofmt -l channel_router.go` → clean
- `go vet ./internal/controller/` → clean

## Disposition

Merge-ready. M1 is a follow-up test hardening (non-blocking); L1/L2 are optional polish.
