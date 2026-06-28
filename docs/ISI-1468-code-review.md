# ISI-1468 — Adversarial code review of ISI-1466 (self-healing NATS event bus)

**Reviewer:** Amelia (Code Reviewer)
**Branch/commit reviewed:** `isi1466-eventbus-resilience` @ `5bc5bbb`
**Files:** `internal/eventbus/nats.go`, `cmd/controller/main.go`, `internal/eventbus/nats_test.go`

## Verdict: 🟢 GREEN-LIGHT — 0 critical / 0 high

The change is strictly better than the status quo: a broker blip at boot can no
longer permanently disable channel routing, the outage is now observable, and
all four focus questions check out. Findings below are **resilience-completeness
gaps (3 Medium)**, not regressions. M1 is the one I recommend folding in before
the deploy drill, because it is a milder re-occurrence of the same silent-failure
class under ephemeral NATS storage.

Verification reproduced locally: `go vet ./internal/eventbus/...` clean,
`go test -count=1 ./internal/eventbus/...` PASS.

---

## Focus-question answers

**1. Concurrency of `readyCh` / `ready` atomic / `mu`-guarded stream — no race.** ✅
`ensureStreamOnce` can run concurrently from the boot loop, `streamHealer`, and
the `ReconnectHandler`. It is race-free: the `stream` write is `mu`-guarded,
`readyCh` is closed exactly once under `ready.CompareAndSwap(false,true)`, and
`setConnected`/`CreateOrUpdateStream` are idempotent. A stale `jetstream.Stream`
handle is connection-bound (operations use the live `js` context), so it keeps
working across a reconnect. Sound.

**2. Consumer-recovery loop terminates & doesn't leak goroutines.** ✅ (with M1/M2)
Every blocking point honors ctx (`ctx.Err()` guard, `waitReady(ctx)`,
`time.After` vs `ctx.Done`, `ch<-` vs `ctx.Done`), so the goroutine always exits
on cancel — no goroutine leak. Two caveats: M2 (a backoff hole / server-side
consumer leak) and M1 (no stream re-heal post-boot).

**3. Not failing `readyz` — observability adequate for the silent-failure class.** ✅ (with L1)
Reasonable call; failing readiness would disrupt the unrelated AgentRun/Agent/
Model reconcilers. The `sympozium_eventbus_connected` gauge + state-change logs
make the outage visible, which is the real requirement. Caveat: no alert ships
on the gauge yet, and `Healthy()` is misleading (L1).

**4. Backward-compat for the other callers.** ✅
apiserver / web-proxy / ipc-bridge / channels(slack,discord,telegram) all still
compile and behave. The key behavior change — the constructor no longer returns
an error on a transient outage — is the intended win: slack/discord/telegram
previously `os.Exit(1)` on that error (boot crash-loop); they now start degraded
and self-heal. apiserver/web-proxy degrade to "no streaming" gracefully.

---

## Findings

### M1 (Medium) — No durable stream re-heal after first ready; consumer loop only re-creates the *consumer*, not the *stream*
`internal/eventbus/nats.go:95-102, 304-313, 343-353`

`streamHealer` only runs when the **boot** stream creation fails, and it exits on
first success. After the bus is ready, the only path that re-creates the stream
is the `ReconnectHandler`, which calls `ensureStreamOnce` **once** per reconnect.
If that single call fails (e.g. NATS came back with the `sympozium` stream
missing — emptyDir/ephemeral storage, or the stream was deleted), nothing retries
`CreateOrUpdateStream`. Meanwhile `Subscribe`'s recovery loop re-creates only the
*consumer* via `stream.CreateOrUpdateConsumer`, which fails forever (~every 2s)
against a non-existent stream and never re-heals the stream. Result: connection
up, routing dead — a milder repeat of the exact bug ISI-1466 fixes. The live
incident log (`nats: connection closed` at boot) suggests the broker can return
without the stream.

The `ReconnectHandler` log "failed to re-ensure ... will keep retrying"
(`nats.go:100`) is also inaccurate — the handler does **not** keep retrying.

**Recommend:** when `ensureStreamOnce` fails in the `ReconnectHandler` (or when
`createConsumer` fails in the recovery loop), launch/re-arm a persistent healer
so the stream is re-created until it exists — mirroring the boot path.
**Drill note:** the deploy drill must kill NATS *after* the controller is
ready (not only during boot) and confirm the stream re-creates, to exercise this
path.

### M2 (Medium) — Recovery loop: backoff hole + server-side ephemeral-consumer leak
`internal/eventbus/nats.go:291-314, 343-353`

The 2s backoff sleep is only in the `else` (createConsumer-failed) branch. If
`createConsumer` *succeeds* but the subsequent `Fetch` errors immediately
(connection flapping where consumer creation briefly succeeds), the loop spins
`createConsumer → Fetch-error → createConsumer` with **no backoff**. Separately,
every `createConsumer` call mints a **new ephemeral consumer** (no `Durable`
name), leaking the prior one server-side until `InactiveThreshold` reaps it —
unbounded churn across a flapping reconnect.

**Recommend:** apply an unconditional small backoff per recovery iteration, and
only re-create the consumer when the error actually indicates consumer loss
(e.g. `jetstream.ErrConsumerNotFound` / `ErrConsumerDeleted`) rather than on
every `Fetch` error.

### M3 (Medium / known limitation) — Messages published mid-outage are still dropped (DeliverNew + ephemeral)
`internal/eventbus/nats.go:343-353`

The recovered consumer uses `DeliverPolicy: DeliverNewPolicy`, so only messages
published *after* the new consumer exists are delivered. Anything published
during the disconnect→recovery window is never consumed. The fix prevents
*permanent* death but not at-least-once delivery across a blip — i.e. the same
"inbound message → 0 AgentRun" symptom can still occur on a shorter timescale.
Acceptable for this PR (durable queue-group is ISI-1430's scope), but it should
ship as a **documented** known limitation, and it strengthens the case for
ISI-1430.

### L1 (Low) — `Healthy()` reports healthy during a disconnect
`internal/eventbus/nats.go:217-219`

`ready` is set once and never reset, and `conn.IsClosed()` is false during a mere
disconnect (only true after `Close()`), so `Healthy()` returns true while the
gauge correctly reads 0. Currently unused, but if a future `readyz`/alert wires
it, it masks outages. Gate on `conn.IsConnected()` or drop the method; rely on
the `connected` gauge.

### L2 (Low) — `Subscribe` now blocks at startup until NATS is reachable
`internal/eventbus/nats.go:270-273`

Behavior change vs old (which returned quickly). All current call sites run
Subscribe inside a ctx-cancellable goroutine/Runnable, so it's safe and honors
ctx — but a future synchronous caller could hang boot. Worth a one-line doc note
on the method.

---

## Triage summary

| ID | Sev | Title | Blocking merge? |
|----|-----|-------|-----------------|
| M1 | Medium | No stream re-heal post-boot (consumer-only recovery) | No — fold in before/with deploy |
| M2 | Medium | Backoff hole + ephemeral consumer leak in recovery | No — fast-follow |
| M3 | Medium | Mid-outage messages still dropped (DeliverNew) | No — document + ISI-1430 |
| L1 | Low | `Healthy()` misleading on disconnect | No |
| L2 | Low | `Subscribe` blocks at startup | No |

No critical/high. Green-light to merge/deploy. Recommend M1 be addressed before
the ProxOps boot-blip drill so the drill can validate the post-boot stream-loss
path too.
