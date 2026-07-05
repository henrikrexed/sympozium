<!-- POST THIS AS A COMMENT ON https://github.com/sympozium-ai/sympozium/pull/256 -->

Thank you for the thorough review @AlexsJones. All 8 findings are now addressed — resolution map below.

**Finding 1 — Unbounded spawn / no depth cap**
Fixed by cherry-pick of `377e65b` (ISI-1463 guardrails). Guardrail 2 (`DelegationMaxDepth`, default 1): child runs are stamped with `sympozium.ai/delegation-depth`; the executor exits early when `depth >= maxDepth`. Proved by `TestTriggerDelegationSuccessors_DepthCapStopsCascade`.

**Finding 2 — Every matching edge fires / no inflight cap**
Fixed by `377e65b`. Guardrail 1 (`DelegationMaxInflight`, default 3): executor lists non-terminal ensemble runs before spawning; if `inflight >= cap` it RequeueAfters instead of creating. Guardrail 3 (K=1 edge selection): `selectDelegationEdges` picks only the top-scoring candidate so a 9-edge hub fans out to at most 1. Proved by `TestTriggerDelegationSuccessors_InflightCapRequeues` and `TestTriggerDelegationSuccessors_ConditionAwareRoutesOne`.

**Finding 3 — Non-atomic create + wall-clock idempotency key**
Fixed in `bbf4381`. Run name is now `<targetAgent>-deleg-<parentRunName>` (deterministic, based on parent run name) so `IsAlreadyExists` correctly dedupes across re-reconciles with no wall-clock component.

**Finding 4 — Marker semantics wrong (triggered only on successful Create)**
Fixed in `bbf4381`. `triggered = true` is set immediately before `r.Create(...)`, so a transient Create failure still marks the parent run and a full re-eval does not re-run every reconcile.

**Finding 5 — Condition evaluated as free-text substring blacklist**
Fixed in `bbf4381`. `AgentConfigRelationship` now carries a `Trigger` enum field (`Success | Failure | Always`). `delegationEdgeActive` prefers the structured field when set; the free-text `Condition` string remains for backwards compatibility.

**Finding 6 — Bypasses ensemble circuit breaker**
Fixed in `d516731`. `triggerDelegationSuccessors` now checks `ensemble.Status.CircuitBreakerOpen` immediately after fetching the ensemble — before candidate filtering or any spawn — mirroring the `spawn_router.go:146` gate. Proved by `TestTriggerDelegationSuccessors_CircuitBreakerBlocks`.

**Finding 7 — Child created without parent traceparent / handoff latency**
Fixed in `bbf4381`. The shared `anchorChildTrace` helper (ISI-1484/ISI-1488 contract) stamps `otel.dev/traceparent` on the child so delegation children join the parent trace. Handoff latency (`sympozium.handoff.latency_ms` with `lane=delegation`) is recorded via `5f9a139`. Proved by `TestTriggerDelegationSuccessors_StampsTraceparent`.

**Finding 8 — ~160-line verbatim copy of triggerSequentialSuccessors / drift**
Partially addressed: `anchorChildTrace` is now a shared helper used by both paths (sequential ~line 1184, delegation ~line 1473), which is where the traceparent drift originated. Full extraction of a common spawn-child helper is a worthwhile follow-up; guard logic is now correct and equivalent in both paths.

**Minor items (env override / Helm quoted-false / idempotency-label placement)**
Fixed in `bbf4381`: `strconv.ParseBool` replaces the literal-`"true"` check so `"1"`, `"True"`, `"t"` all work; the `{{- if ... }}` Helm conditional now emits `"true"` only when the feature is enabled; idempotency-label check is hoisted above both client Gets.

All 11 delegation-executor tests pass (`go test ./internal/controller/... -run TestTriggerDelegationSuccessors`).
