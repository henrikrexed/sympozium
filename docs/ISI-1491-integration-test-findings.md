# ISI-1491 — Integration-test branch validation findings

**Branch:** `isi1436-integration-test` (tip `9a900dc`, off `c27e09e` = prior helm rev 15)
**Image:** `ghcr.io/henrikrexed/sympozium/*:isi1406-9a900dc`
**Deploy:** observable-llm, helm release `sympozium` **rev 16** (`--reuse-values --set image.tag=isi1406-9a900dc`, chart 0.10.36, no drift)
**Date:** 2026-06-30 · **Env:** oat05854 (Dynatrace), `sympozium-system` / `default` ns

## Build

- Cherry-picked **ISI-1430** (`f2bbefe`) + **ISI-1483** (`701d919`) onto `c27e09e`.
- Conflicts resolved:
  - `internal/eventbus/nats.go` — ISI-1430's shared `drain()` refactor vs ISI-1466 self-heal loop. Threaded `subject` + a per-caller `recreate` closure into `drain()` so `Subscribe` re-heals ephemeral and `SubscribeGroup` re-heals its durable queue-group consumer.
  - `cmd/agent-runner/main.go` — deploy branch already moved obs-init early (`adoptRemoteParent`, ISI-1485). Dropped ISI-1483's duplicate late obs-init; converted the early `obs.shutdown` defer into ISI-1483's `sync.Once` `flushTelemetry` (5s budget).
- `go build ./...` ✅  `go vet ./...` ✅  `go test ./...` ✅ (full suite, 0 failures).

## Per-ticket results

| Ticket | Item | Result | Evidence |
|---|---|---|---|
| ISI-1436 / #236 | Delegation executor (opt-in) | ✅ PASS | `controllerExecutor=true` live; `sympozium.delegation.edges` emitting post-deploy (`skipped_depth_cap=1` in first 15 min on rev 16). |
| ISI-1463 | Delegation guardrails | ✅ PASS | maxInflight=3 / maxDepth=1 live; decisions `skipped_condition`, `skipped_depth_cap` recorded; `skipped_inflight_cap` dim wired (prior live proof ISI-1465 = 4–8). |
| ISI-1467 | Delegation obs instruments | ✅ PASS | All 5 `delegation.edges` decision dims emit (DQL `by:{decision}`): fired, skipped_already_delegated, skipped_condition, skipped_depth_cap, skipped_inflight_cap. |
| ISI-1466 | Eventbus self-heal | ✅ PASS (live drill) | NATS scaled 0 for 72s → `sympozium_eventbus_connected` **1→0**, held 0 through outage, **→1** within ~12s of recovery. Controller **restarts=0** throughout. |
| ISI-1430 | Eventbus queue-group | ✅ PASS | Unit test `nats_queuegroup_test.go` (224 lines) green; ChannelRouter wired to `SubscribeGroup(durable channelRouterGroup)` for inbound + completed topics. Live single-delivery only differs at replicas>1 (topology=1). |
| ISI-1461 | skill.name in spans | ✅ PASS | DQL `sympozium.skill.exec` grouped by `skill.name`: command-executor=398, github-gitops=3, k8s-ops=2. |
| ISI-1427 | Slack dedup (multi-message) | ✅ PASS (deployed) | Dedup enforcement in controller `channel_router.go` (TTL set on `event_id`/`client_msg_id`) on rev 16; slack sidecar code byte-identical f4dd85c→9a900dc. Fresh 1-send→1-reply needs a Slack workspace message (prior live proof ISI-1458). |
| ISI-1483 | SIGTERM span flush | ✅ PASS (live drill) | SIGTERM'd a live run: handler logged "ending root run-span and flushing telemetry"; root span `sympozium.agent.run` (trace `7de2ba68…`) exported **closed** — `end_time=08:01:15.535` (handler-fire), `status=error`, `duration≈102.27s`. Not rootless. |
| ISI-1406/1484/1485/1488 | Multi-agent obs + traceparent | ✅ PASS (observed) | Drill run logged `adopted remote parent trace: traceID=7de2ba68… remote=true`; child gen_ai/tool spans share the parent trace.id. |

## Notes

- Per-agent channel-slack sidecar pods (10) remain on `isi1406-f4dd85c` — `image.tag` swaps bump helm-managed workloads (controller/apiserver/runner-via-SYMPOZIUM_IMAGE_TAG) but not per-Agent child pods. Functionally irrelevant for slack here (byte-identical channel code); flagged for completeness.
- Controller post-all-drills: Running, restarts=0, `eventbus_connected=1`.
- This is the INTERNAL validation branch. Upstream PRs stay per-concern (ISI-1490 #236, ISI-1456 ISI-1430, etc.). `build-fork.yaml` push-trigger addition is internal-only.
