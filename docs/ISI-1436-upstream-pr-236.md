# Upstream PR #236 — title + description (ready to paste)

- **Head:** `henrikrexed:upstream/isi-1436-delegation-executor`
- **Base:** `sympozium-ai/sympozium:main` (`501ac16`)
- **Commits:** 3, all DCO signed-off (Henrik Rexed) · +1207 / −10 · 7 files
- **Closes:** #236

---

## Title

```
feat(controller): opt-in controller-side delegation executor with guardrails + telemetry (#236)
```

---

## Description (Markdown)

```markdown
## Summary

Adds an **opt-in, controller-side delegation executor** — a fallback that follows an
ensemble's `delegation` relationship edges after a run succeeds, for models that never emit
the `delegate_to_persona` tool call. Per the maintainer green-light on #236, it is **strictly
opt-in and the tool-driven path stays the default**: with the flag unset there is **zero
behavior change**.

Mirrors the existing `triggerSequentialSuccessors` post-success reconcile path.

Closes #236.

## What's included (3 commits)

1. **`feat(controller): opt-in controller-side delegation executor (#236)`**
   New `triggerDelegationSuccessors` in the post-success reconcile path, gated behind
   `SYMPOZIUM_DELEGATION_CONTROLLER_EXECUTOR` (default **false**). Iterates
   `ensemble.Spec.Relationships` for `type == "delegation"` && `source == <succeeded persona>`,
   evaluates each edge's free-text `condition`, and creates the target persona's child
   AgentRun via the same builder, carrying a structured handoff card. Idempotent: marks the
   parent with `sympozium.ai/delegation-triggered` (mirrors `sympozium.ai/sequential-triggered`)
   so a re-reconcile never double-spawns.

2. **`feat(controller): guardrail delegation executor (ISI-1463)`**
   Three guardrails make it safe to enable:
   - **Concurrency cap** (`maxInflight`, default 3) — per-ensemble in-flight ceiling; a
     successor is requeued rather than spawned when the ensemble is saturated (prevents the
     fan-out gridlock).
   - **Depth cap** (`maxDepth`, default 1) — a controller-spawned child at/over this depth
     fires no further delegation successors (no unbounded cascade).
   - **Condition-aware K=1 edge selection** — scores each edge's condition against the run
     result/task and fires only the top match (default K=1); when several edges compete and
     none matches, it fans out to **none** rather than the whole team.

3. **`feat(obs): instrument delegation executor for cap-verification telemetry (ISI-1462)`**
   OpenTelemetry so the caps are provable from data: `sympozium.delegation.edges` (counter,
   by `decision`: fired / skipped_condition / skipped_inflight_cap / skipped_depth_cap /
   skipped_already_delegated), `sympozium.delegation.inflight_at_decision` and
   `sympozium.delegation.depth_observed` (histograms), `sympozium.delegation.tool_emitted`
   (counter), and `sympozium.handoff.latency_ms` tagged `lane=delegation` (the sequential
   site is tagged `lane=sequential`). Emits a `sympozium.delegation.handoff` span
   (`handoff.lane=delegation`) anchored into the parent run's trace, so a delegation child
   joins its parent's trace exactly like a sequential handoff.

## Default-off guarantee

Flag unset ⇒ no code path change; models that emit `delegate_to_persona` keep driving
delegation as today. The executor is purely a fallback for models that never emit the tool
call.

## Configuration surface

| Env | Chart value | Default | Effect |
|---|---|---|---|
| `SYMPOZIUM_DELEGATION_CONTROLLER_EXECUTOR` | `delegation.controllerExecutor` | `false` | master switch |
| `SYMPOZIUM_DELEGATION_MAX_INFLIGHT` | `delegation.maxInflight` | `3` | per-ensemble concurrency cap |
| `SYMPOZIUM_DELEGATION_MAX_DEPTH` | `delegation.maxDepth` | `1` | controller-spawned chain depth cap |

## Testing

- **Unit:** `agentrun_delegation_executor_test.go` (edge selection, condition evaluation,
  idempotency label, concurrency/depth caps, no-match ⇒ no fan-out) and
  `agentrun_delegation_metrics_test.go` (SDK ManualReader asserts a cto fan-out records
  `fired=1` + the skip decisions). `go vet` + package tests green.
- **Validated live** on a real ensemble: a `cto` run succeeded → the executor fired exactly
  one edge (`cto → product-manager`), created the child run, and produced a single connected
  trace (1 root, 2 agent runs stitched by the `sympozium.delegation.handoff` span, child
  adopting the parent's `trace.id`). Telemetry for the run: `fired=1`, `skipped_condition=8`
  (the source's 9 candidate edges narrowed to K=1), depth/inflight caps `0` — i.e. the
  guardrails are observable end-to-end.

## Backward compatibility

No migration and no behavior change when the flag is off (the shipped default). Enabling it
is a pure opt-in fallback bounded by the caps above.

Signed-off-by: Henrik Rexed <henrik.rexed@gmail.com>
```
