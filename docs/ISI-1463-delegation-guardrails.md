# ISI-1463 — Guardrail the delegation controller executor

**Owner:** Architect (Winston) · **Date:** 2026-06-28 · Implements the guardrailed fix
diagnosed in [docs/ISI-1462-delegation-autonomy-diagnosis.md](./ISI-1462-delegation-autonomy-diagnosis.md).

Extends the `triggerDelegationSuccessors` controller executor (ISI-1436 / upstream #236)
so `delegation.controllerExecutor=true` can be enabled **without** re-triggering the
ISI-1458 ~44-run fan-out gridlock. All changes are additive; default-off behavior is
preserved bit-for-bit.

## Problem recap (grounded, observable-llm 2026-06-28)

`bmad-ensemble`'s delegation graph is **9 edges all sourced from `cto`** (out-degree 9).
`delegationEdgeActive()` only screens for *failure* keywords, so on a `cto` **success**
all 9 edges evaluate active. A naive flag flip ⇒ 9-way fan-out per completion, each
potentially fanning further ⇒ gridlock. The local model (qwen3.6) under-emits
`delegate_to_persona` (1 of 9 edges, then stops), so the chain stalls and a human must
nudge. The executor is the intended fallback but was unsafe as-is.

## Guardrails (all in `internal/controller/agentrun_controller.go`)

| # | Guardrail | Config | Default | Mechanism |
|---|-----------|--------|---------|-----------|
| 1 | **Per-ensemble in-flight concurrency cap** (PRIMARY) | `DelegationMaxInflight` / `delegation.maxInflight` | 3 | Before spawning, `List` non-terminal AgentRuns labeled `sympozium.ai/ensemble=<name>`; if `count >= cap`, `RequeueAfter` (15s) instead of `Create`. Bounds blast radius regardless of graph shape. |
| 2 | **Delegation depth cap** | `DelegationMaxDepth` / `delegation.maxDepth` | 1 | Children stamped `sympozium.ai/delegation-depth=<n>`; a run at/over the cap fires no further delegation successors. Prevents cascade. |
| 3 | **Condition-aware edge selection** | const `defaultDelegationTopK` | 1 | Score each candidate edge's free-text `condition` against the source run's `Status.Result`+`Task` (lexical token overlap); fire only the top-K match(es). A single candidate always fires (no fan-out risk); when several compete and none matches, fan out to **none**. |
| 4 | **Existing `alreadyDelegated` skip** (retained) | — | — | Edges the model DID emit at runtime (`Status.Delegates`) are never double-fired. |

### Why the concurrency cap gates the batch (not per-edge)

The cap is checked **once before** spawning the selected (≤K, default 1) batch. With K=1
this is identical to a per-successor check, and it guarantees a requeue never partially
spawns then double-spawns on retry. Steady-state in-flight is bounded by the cap; per-
completion fan is bounded by K.

## Wiring

- **Reconciler fields:** `DelegationMaxInflight`, `DelegationMaxDepth` (≤0 ⇒ built-in default).
- **`cmd/controller/main.go`:** flags `--delegation-max-inflight` / `--delegation-max-depth`
  and env `SYMPOZIUM_DELEGATION_MAX_INFLIGHT` / `SYMPOZIUM_DELEGATION_MAX_DEPTH`
  (mirror the existing `SYMPOZIUM_DELEGATION_CONTROLLER_EXECUTOR` pattern).
- **Chart:** `delegation.maxInflight` / `delegation.maxDepth` in `values.yaml`; the
  controller-deployment template emits the env vars **only when `controllerExecutor=true`**.

## Tests (`agentrun_delegation_executor_test.go`)

Existing default-off / spawn / idempotent / already-delegated tests retained (updated for
the new `(ctrl.Result, error)` signature). Added:

- `..._ConditionAwareRoutesOne` — 3-edge fan-out routes to the single best match.
- `..._NoMatchNoFanOut` — no condition match ⇒ zero children, parent not marked.
- `..._InflightCapRequeues` — cap hit ⇒ `RequeueAfter>0`, zero children, not marked.
- `..._DepthCapStopsCascade` — child stamped depth=1; a depth-1 source spawns nothing.
- `TestSelectDelegationEdges` — single-candidate fires, best-match wins, zero-match ⇒ none.

`go build`, `go vet`, full `internal/controller` package test, and `helm template`
(caps emitted only with executor on) all green.

## Observability instrumentation (ISI-1462 Phase-3 handoff)

The delegation lane previously emitted **zero** telemetry, so once the caps are enabled
there would be no way to prove they hold. Per the obs handoff (`docs/ISI-1462-phase3-delegation-autonomy-observability.md`),
the following were added inside `triggerDelegationSuccessors` while the code was open
(all additive, low cardinality, mirroring `sympozium.*` conventions):

- **I1** — `sympozium.handoff.latency_ms` now recorded in the delegation path with
  `lane=delegation`; the existing sequential site is tagged `lane=sequential` so the two
  lanes are separable in one histogram.
- **I2** — `sympozium.delegation.edges` counter, attr `decision` ∈
  {`fired`, `skipped_condition`, `skipped_already_delegated`, `skipped_inflight_cap`,
  `skipped_depth_cap`}, `ensemble`, `source`. A nonzero `skipped_inflight_cap`/`skipped_depth_cap`
  proves the cap *engaged*; `fired ≤ K` proves condition-aware routing (no 9-wide fan-out).
- **I3** — `sympozium.delegation.inflight_at_decision` histogram (`ensemble`) — the in-flight
  count already Listed for the concurrency cap, recorded at decision time. Histogram (not a
  gauge) so restarts/replicas can't double-count; `max() ≤ DelegationMaxInflight` proves the cap.
- **I4** — `sympozium.delegation.depth_observed` histogram (`ensemble`) — depth stamped on
  spawned children; `max() ≤ DelegationMaxDepth`.
- **I5** — `sympozium.delegation.tool_emitted` counter (`ensemble`, `source`) —
  `len(Status.Delegates)`, to quantify model under-emission vs executor backfill.

**Naming note for obs:** the new I2–I5 metrics use the bare `ensemble` attribute (matches the
Phase-3 DQL K2–K5). I1 keeps the existing handoff site's `sympozium.ensemble` attribute to avoid
breaking in-prod sequential dashboards; the Phase-3 K1 query groups by `lane`/`from`/`to` only,
so it is unaffected.

**Emission cadence caveat:** runs that pass the guards but fire nothing (no condition match) are
not marked `delegation-triggered`, and the in-flight-cap path returns `RequeueAfter`. Both re-run
on re-reconcile, so I3/I5 and the skip counters may re-record across reconciles. The Phase-3
queries use `max()` (I3/I4, idempotent to repeats) and rate/sum trends (I2/I5), so the cap-holds
assertions are robust to this; absolute counter totals should be read as trends, not exact tallies.

Verified by `agentrun_delegation_metrics_test.go` (`..._EmitsEdgeDecisionMetrics`): an SDK
manual-reader harness asserts the cto fan-out records `edges{decision=fired}=1` and
`edges{decision=skipped_condition}=2`.

## Deploy gate

Enabling autonomous delegation is a behavior change with prior-gridlock history.
**ProxOps deploys** `--set delegation.controllerExecutor=true` + caps under a separate
ISI-1458 deploy issue, **after** board/CTO sign-off on this guardrailed approach
(tracked on ISI-1462).
