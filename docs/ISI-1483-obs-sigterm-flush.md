# ISI-1483 — Obs robustness: flush + close root run-span on SIGTERM/timeout

**Owner:** Observability Agent · **Status:** code DONE + tested (2026-06-29)
**Parent:** ISI-1481 (fix #2) · **Scope:** `cmd/agent-runner`

## Problem
When a run is killed at its deadline, the root run-span was never ended and the
final unflushed OTel batch (1s `BatchTimeout`) was lost → the trace appeared
rootless/incomplete in Dynatrace, which reads as "missing agent-runner spans".

Two distinct gaps caused this:

1. **No signal handler.** The runner ran under `context.WithTimeout(ctx, runTimeout)`
   and relied on a `defer`ed OTel `Shutdown` for flushing. When a run exceeds its
   budget, Kubernetes hits the Job's `activeDeadlineSeconds` and terminates the
   pod with **SIGTERM**, then **SIGKILL** after `terminationGracePeriodSeconds`.
   With no handler, the process died on SIGTERM's default disposition: the root
   `runSpan` was never ended and the deferred flush never ran → rootless trace.

2. **`os.Exit(1)` bypassed the deferred flush (latent bug).** Even the *in-process*
   context-deadline path (which fires ~60s before the k8s kill) takes the error
   branch, which calls `runSpan.End()` then `os.Exit(1)`. `os.Exit` skips deferred
   functions, so the deferred `obs.shutdown` never flushed the final batch — the
   span was ended but lost on exit, leaving an incomplete trace.

## Fix (`cmd/agent-runner`)
- **`observability.go`** — new `endRunSpanWithReason(span, reason)`: sets the root
  span status to `Error` with the termination reason and ends it. `span.End` is
  idempotent in the OTel SDK, so calling it on the signal path after a normal end
  is a safe no-op.
- **`main.go`**
  - Replaced the deferred 2s shutdown with a `sync.Once`-guarded `flushTelemetry()`
    (5s budget, well within the 30s grace) so flush runs **exactly once** from
    whichever exit path reaches it: normal return, the `os.Exit(1)` error path, or
    the signal handler.
  - Installed a `SIGTERM`/`SIGINT` handler (after the root span is created) that:
    ends the root span with a timeout status, records a `status="timeout"`
    terminated-run metric, flushes telemetry, then exits `143`.
  - Added an explicit `flushTelemetry()` before the `os.Exit(1)` error path
    (closes gap #2).

## Scope-item verification
1. **3s flush runs on deadline SIGTERM, grace ≥ flush.** Now true — previously
   there was no handler at all, so SIGTERM killed the process before any flush.
   The agent-run Job pod sets no `TerminationGracePeriodSeconds` → k8s default
   **30s**, comfortably covering the ≤5s flush budget (`telemetry.Shutdown` caps
   internally at 3s).
2. **Root run-span ended with timeout status before flush.** `endRunSpanWithReason`
   sets `codes.Error` + reason and ends the span; the handler calls it before
   `flushTelemetry()`.
3. **BatchSpanProcessor ForceFlush on shutdown.** Confirmed via the SDK contract:
   `telemetry.Shutdown` → `tracerProvider.Shutdown` force-flushes the
   `BatchSpanProcessor`. The new explicit flush on the error path ensures Shutdown
   is actually invoked even when `os.Exit` is used.

## Tests
- `TestEndRunSpanWithReason` (new, `observability_test.go`) — in-memory exporter
  asserts the root span is exported, ended, status `Error`, description = reason.
- `TestEndRunSpanWithReason_NilSafe` — nil-span tolerance (noop/obs-disabled path).
- `go vet ./cmd/agent-runner/` clean; full `go test ./cmd/agent-runner/` green.

## Related
- ISI-1481 (parent; primary runTimeout fix live+verified).
- ISI-1406 (obs baseline).
- Backlog `349ba97d` / ISI-1482 (delegation/sequential child runs mint a fresh
  `trace.id` + dangling parent span → trace fragmentation). **Distinct root cause**
  from this issue: 1483 ensures a single run's *own* root span closes & flushes on
  termination; 1482 is about child runs not sharing the parent's `trace.id`.
  Coordinate but do not conflate — both must land for delegation traces to be whole.

## Deploy
Generic/upstreamable; rides the next agent-runner image build (coordinate with the
ISI-1465 deploy line). Branch `fix/isi-1483-obs-sigterm-flush` off `main`.
