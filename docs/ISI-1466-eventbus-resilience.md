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

## Post-review hardening (ISI-1468 findings folded in)

Amelia's adversarial review (`docs/ISI-1468-code-review.md`) green-lit the change
(0 critical / 0 high) and surfaced resilience-completeness gaps. Resolved on this
branch:

- **M1 — durable stream re-heal post-boot.** The background healer is now
  re-armable via `startHealer` (atomic guard → single healer at a time) and is
  triggered from the `ReconnectHandler` and from the Subscribe recovery loop when
  the consumer can't be re-created. So if NATS returns *without* the `sympozium`
  stream (ephemeral storage / deleted stream), the stream is re-created until it
  exists instead of the consumer loop spinning forever against a missing stream.
  The inaccurate "will keep retrying" reconnect log is gone.
- **M2 — recovery backoff + ephemeral-consumer leak.** The Subscribe fetch loop
  now applies an **unconditional** `recoveryBackoff` (2s) on any fetch error
  (closing the spin hole where consumer-create succeeded but Fetch errored), and
  only re-creates the consumer when the error actually indicates consumer loss
  (`consumerLost`: `ErrConsumerNotFound` / `ErrConsumerDeleted` /
  `ErrConsumerDoesNotExist`) — transient connection errors keep the existing
  connection-bound consumer handle, so we no longer mint (and leak) a new
  ephemeral consumer on every blip.
- **M3 — at-most-once across an outage (documented limitation).** The recovered
  consumer uses `DeliverNew`, so messages published during the
  disconnect→recovery window are not redelivered. This fix prevents *permanent*
  routing death, not at-least-once delivery across a blip; durable queue-group
  delivery is **ISI-1430**'s scope. Documented on `Subscribe`.
- **L1 — `Healthy()`** now gates on `conn.IsConnected()` (not `!IsClosed()`), so
  a live disconnect reads unhealthy, consistent with the `connected` gauge.
- **L2 — `Subscribe` blocking** at startup is documented; all call sites already
  run it in a ctx-cancellable Runnable.

New tests: `TestConsumerLostClassification`, `TestStartHealerGuardIsReArmable`.

**Drill addendum for ProxOps (ISI-1469):** kill NATS *after* the controller is
ready (not only during boot) and confirm the stream re-creates and routing
resumes — this exercises the M1 post-boot path, not just the boot path.

## Post-drill hardening (ISI-1469 residual — child 25d47671)

ProxOps deployed `isi1406-f4dd85c` and confirmed the boot-blip and the realistic
(persistent-PVC) NATS-bounce paths recover with no controller restart. The
*full-store-loss* drill (wiping the JetStream store so NATS returns empty)
surfaced a residual gap: **M1 re-creates the stream and the gauge returns to 1,
but a running subscriber did not re-establish its JetStream consumer** — the
stream showed 0 consumers and a probe sat undelivered until a controller restart.

Root cause: a deleted-and-recreated stream leaves the old ephemeral consumer
gone, but `Fetch` against the stale handle fails with a no-responders/timeout
error — *not* `ErrConsumerNotFound` — so the `consumerLost`-only recovery never
re-subscribed. (Low risk on observable-llm, which uses a persistent PVC; it
matters for ephemeral/`emptyDir` NATS, which is exactly the M1 target.)

**Fix:** track a `streamGen` counter bumped only when a *genuinely new* stream is
established, detected by comparing the stream's `Created` timestamp from the
CreateOrUpdate response (`streamRecreated`). A normal reconnect where the stream
survives keeps the same timestamp and does **not** bump the generation, so
transient blips still don't churn/leak consumers (preserves M2). The Subscribe
fetch loop now re-establishes its consumer when `consumerLost(err)` **or** the
generation advanced since the consumer was created — so a full stream re-heal
re-subscribes the running router automatically, no restart.

New test: `TestStreamRecreatedDetection`. The ephemeral-NATS full-store-loss
path should be re-drilled by ProxOps to confirm consumers re-establish live
(tracked in child `25d47671`).

## Handoff

- **Code review:** Code Reviewer (Amelia) — adversarial review of the
  reconnect/consumer-recovery logic and the `Publish` not-ready behavior.
- **Deploy + drill:** ProxOps — build fork image, deploy to `observable-llm`,
  and run the boot-blip drill; confirm `sympozium_eventbus_connected` flips
  1→0→1 across an induced NATS restart and that channel-driven AgentRuns resume
  without a controller restart.

Related: ISI-1462 (delegation autonomy — this is an upstream cause of the same
"messages don't progress" symptom), ISI-1430 (eventbus durable queue-group).
