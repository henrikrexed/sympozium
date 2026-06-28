# ISI-1466 — Self-healing NATS event bus (controller channel routing resilience)

**Branch:** `isi1466-eventbus-resilience` (off `main`)
**Commit:** `5bc5bbb` (DCO-signed)
**Fork PR (open via compare):** https://github.com/henrikrexed/sympozium/compare/main...isi1466-eventbus-resilience

## Problem

A transient NATS outage at controller boot permanently disabled channel
routing for the whole pod lifetime:

- `cmd/controller/main.go` wired the `ChannelRouter` / `ScheduleRouter` /
  `SpawnRouter` **only if `NewNATSEventBus` succeeded at boot**. On failure it
  logged `unable to connect to NATS — channel routing disabled` and continued
  **without ever retrying**.
- `NewNATSEventBus` capped reconnects at 10 and returned a hard error if
  JetStream stream creation failed within ~20s.

Consequence (live, 2026-06-28): controller restarted at `00:34:22Z` during a
cluster blip, logged `creating JetStream stream after retries: nats: connection
closed`, and routing stayed dead. Inbound Slack messages were `accepted inbound`
by the channel pod and published to NATS, but **never consumed → zero
AgentRuns**, silently. Mitigated at the time by `kubectl rollout restart
deploy/sympozium-controller-manager`.

## Fix (durable)

All in `internal/eventbus/nats.go` + a 6-line wiring change in
`cmd/controller/main.go`:

1. **Infinite reconnect** — `nats.MaxReconnects(-1)` (was `10`). The connection
   survives broker restarts/blips at runtime.
2. **Background stream healing** — `NewNATSEventBus` still tries stream creation
   synchronously (happy path unchanged, 5 attempts) but, if NATS is unreachable,
   **returns a working-but-degraded bus and self-heals in the background**
   (`streamHealer`, capped 15s backoff) instead of returning an error. It now
   only errors for unrecoverable config (e.g. a malformed URL). Routing is
   therefore **always wired** when `NATS_URL` is set, and **self-enables once the
   broker is reachable — no pod restart required.**
3. **Consumer recovery** — `Subscribe` waits for stream readiness (honoring
   `ctx`) and **re-creates its consumer if it is lost across a reconnect**
   (ephemeral consumers are reaped by the server on disconnect), so the channel
   router recovers automatically rather than spinning on fetch errors.
4. **Fail-safe publish** — `Publish` returns an error when the bus is not ready
   instead of panicking on a nil JetStream handle or silently dropping.
5. **Surfaced health** — a `sympozium_eventbus_connected` gauge (1 = connected +
   stream ready, 0 = down) plus state-change logs (`NATS connection lost`,
   `NATS reconnected`, `JetStream stream ready`) make the outage **visible
   instead of silent**.

`WithLogger(...)` (variadic option) threads the controller logger into
connection/stream state changes. The signature stays backward-compatible, so the
apiserver / web-proxy / ipc-bridge / channel callers are unchanged but inherit
the same resilience (no more boot crash-loop on a broker blip).

### Design note — why not fail readiness?

The issue suggested optionally failing the controller's `readyz` when routing is
down. I deliberately chose **metric + logs** over failing readiness: the
controller also reconciles AgentRun/Agent/Model/etc., and marking the whole pod
unready during a NATS outage would disrupt those unrelated reconcilers. The
silent-failure bug is fixed by making the outage *observable*
(`sympozium_eventbus_connected == 0` + logs) and *self-recovering*, which is the
real requirement. A future alert can fire on the gauge.

## Verification

- `go build ./...` — green
- `go vet ./internal/eventbus/... ./cmd/controller/...` — green
- `go test -count=1 ./internal/eventbus/...` — green (new `nats_test.go`:
  publish-when-not-ready returns error, `waitReady` unblocks on ready,
  `waitReady` respects ctx cancellation, connected gauge default/transitions,
  collector registration)
- `go test ./internal/controller/...` — green (routers unchanged at call sites)

The self-heal-against-live-NATS path is best validated by a deploy drill on
`observable-llm` (kill NATS during controller boot, confirm routing enables once
NATS returns, with no pod restart) — see deploy handoff below.

## Handoff

- **Code review:** Code Reviewer (Amelia) — adversarial review of the
  reconnect/consumer-recovery logic and the `Publish` not-ready behavior.
- **Deploy + drill:** ProxOps — build fork image, deploy to `observable-llm`,
  and run the boot-blip drill; confirm `sympozium_eventbus_connected` flips
  1→0→1 across an induced NATS restart and that channel-driven AgentRuns resume
  without a controller restart.

Related: ISI-1462 (delegation autonomy — this is an upstream cause of the same
"messages don't progress" symptom), ISI-1430 (eventbus durable queue-group).
