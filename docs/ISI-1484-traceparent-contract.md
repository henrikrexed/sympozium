# ISI-1484 — Cross-run traceparent: controller contract (Workstream A)

**Parent:** ISI-1482 (cross-run trace fragmentation). **Pairs with:** Workstream B
(agent-runner adoption, Observability Agent).
**Branch:** `fix/isi-1484-cross-run-traceparent` (off `feat/isi-1436-delegation-executor`
= upstream PR #236, off `main@897c5f0`).

## Problem
Per-run `sympozium.agent.run` traces are richly multi-span, but **cross-run linkage is
broken**: when a run spawns a delegation/sequential successor, the child AgentRun is created
with **no traceparent annotation**, so the child's reconcile (and its runner root span) start a
**fresh `trace.id`** and stamp a `span.parent_id` that resolves to nothing. A cto→successors
chain fragments into N disconnected single-span-looking traces (board's ISI-1461 e3ccf5da).

This is the cross-RUN analogue of ISI-1406 gap-#2 (`947cc10`), which fixed traceparent
propagation only **within** a single run (controller→runner→memory).

## The contract (what B consumes)
- **Annotation key:** `otel.dev/traceparent` — **reused, not a new key.** The task description
  said "e.g. `sympozium.io/traceparent`", but the established within-run contract is already
  `otel.dev/traceparent` → injected to the **`TRACEPARENT`** env var by
  `buildObservabilityEnv` → adopted by the runner (947cc10). Introducing a second key would
  require a runner change and fork the plumbing. Boring-technology choice: extend the existing
  contract to the cross-run case.
- **Value format:** W3C traceparent `00-<32-hex trace-id>-<16-hex span-id>-<flags>`
  (`formatTraceparent`). Flags `01` when sampled.
- **Anchor span:** the span id in the traceparent is a **dedicated, exported** per-hop handoff
  span — `sympozium.delegation.handoff` or `sympozium.sequential.handoff` — started as a child
  of the parent run's `agentrun.reconcile` span and `End()`-ed immediately, so it is always
  exported and **resolvable** in the backend (never synthesized/dangling). Attributes:
  `handoff.lane` (`delegation`|`sequential`), `handoff.source_persona`,
  `handoff.target_persona`, `handoff.parent_run`, `handoff.child_run`.

### Workstream B's job (agent-runner / Observability Agent)
The runner already reads `TRACEPARENT` (947cc10). B must ensure the runner's **root**
`sympozium.agent.run` span adopts it as the **remote parent** (via
`trace.ContextWithRemoteSpanContext`) so the child inherits the parent's `trace.id`, rather than
only parenting nested spans off it while the root stays a fresh trace. If the within-run path
already routes `TRACEPARENT` into the root span, B may reduce to a verification + the DQL
acceptance below. **No annotation-key change is required of B.**

## Spawn sites
| Lane | Site | Status after A |
|------|------|----------------|
| Delegation | `triggerDelegationSuccessors` (`agentrun_controller.go`) | **fixed** — `anchorChildTrace` before `Create` |
| Sequential | `triggerSequentialSuccessors` | **fixed** — `anchorChildTrace` before `Create` |
| Channel→run | `channel_router.go:312` (`handleInbound`) | **already compliant** — stamps `otel.dev/traceparent` from the exported `channel_router.handle_inbound` span; no change needed |

## Implementation (A)
`anchorChildTrace(ctx, child, spanName, attrs...)` helper in `agentrun_controller.go`:
starts the handoff span on the parent reconcile ctx, ends it (export), and stamps
`formatTraceparent(handoff.SpanContext())` onto `child.Annotations["otel.dev/traceparent"]`.
Called at the delegation and sequential spawn sites before `r.Create`.

## Tests
`agentrun_traceparent_test.go` — installs an always-sampling SDK TracerProvider, starts a parent
span, drives `triggerDelegationSuccessors` / `triggerSequentialSuccessors`, and asserts the
spawned child:
1. carries a non-empty `otel.dev/traceparent` annotation;
2. its trace-id **equals the parent's** (joined trace, not a fresh root);
3. its parent-span id **differs** from the reconcile span id (a distinct exported handoff span).
`go build ./...`, `go vet`, full `internal/controller` suite green.

## Acceptance
- **A (this PR, unit):** child AgentRun carries a non-empty traceparent; trace-id == parent's. ✅
- **A+B (live, post combined deploy):** the referenced parent span id resolves to an exported
  span on oat05854, and over one delegation session
  `fetch spans | filter span.name=="sympozium.agent.run" | summarize traces=countDistinct(trace.id), runs=countDistinct(sympozium.agent_run.id)`
  shows **traces << runs**. Owned by Workstream B + ProxOps/Obs post-deploy.
