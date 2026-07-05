# ISI-1495 — JetStream health / consumer-drain telemetry verdict

**Author:** Observability Agent · **Parent:** ISI-1436 · **Date:** 2026-07-01
**Inputs:** ProxOps snapshot `/mnt/nas/tmp/isi1436-incident-logs-20260701-0705/` (`nats-jsz.json`,
`nats-current-full.log`, `sys-sympozium-controller-manager-*.log`) + Dynatrace `dynatrace-dev` (oat05854).

## Verdict (answers ProxOps Q1/Q2/Q3)

**Q1 — Benign recovery lag or consumer-drain regression? → BENIGN. NOT a drain regression.**

The growing stream count (1,209 → 1,954) is **stream retention**, not undelivered backlog.
Decisive metric from `jsz`: **total `num_pending` across ALL 6 consumers = 8 messages** (not ~745).
The two real channel-router durable consumers are caught up on their filter subjects
(`num_pending = 0`, actively pulling, `num_waiting = 1`).

| consumer | kind | delivered.sseq | ack.sseq | num_pending | num_waiting | consumer_seq | state |
|---|---|---|---|---|---|---|---|
| `sympozium-workers-channel-router-…-agent-run-completed` | durable | 1954 | 1954 | **0** | 1 | 52 | ✅ caught up, pulling |
| `sympozium-workers-channel-router-…-channel-message-received` | durable | 1903 | 1903 | **0** | 1 | 4 | ✅ caught up on subject, pulling |
| `QUa3Y7Cm` | ephemeral (per-run) | 1201 | 1201 | 2 | **0** | 0 | ⚠️ orphaned |
| `dJ0Yiv2G` | ephemeral (per-run) | 1200 | 1200 | 2 | **0** | 0 | ⚠️ orphaned |
| `fkzNPlAc` | ephemeral (per-run) | 1201 | 1201 | 2 | **0** | 0 | ⚠️ orphaned |
| `y8fkzwTJ` | ephemeral (per-run) | 1203 | 1203 | 2 | **0** | 0 | ⚠️ orphaned |

The apparent "lag" of ~750 for the ephemerals (`last_seq 1954 − delivered_sseq ~1200`) is an
**artifact of their filter subject**: each matches only 2 stream messages (`num_pending = 2`), so
they are not holding a 750-message backlog. `consumer_seq = 0` = they never delivered a single
message to a client; `num_waiting = 0` = **no client is currently pulling them** → orphaned.

Stream `sympozium` is **Limits retention** (max_storage 1.09 TB, only 1.3 MB used, `first_seq = 1`
retained since 2026-06-30 12:50). Growth of ~745 msgs over ~7 h = ~106 msg/h of normal ensemble
publish traffic, all retained by design. Nothing is stuck behind the real consumers.

**Q2 — Which subjects/consumers?** No regression on the real subjects
(`sympozium.agent.run.completed` lag 0; `sympozium.channel.message.received` 0 pending).
The 4 orphans are **per-run ephemeral pull consumers**, subjects
`sympozium.agent.followup.<run>` and `sympozium.tool.exec.result.<run>` — evidenced by the boot log:

```
$JS.API.CONSUMER.CREATE.sympozium.ddHp7ZNB.sympozium.agent.followup.bmad-ensemble-product-manager-seq-45611
$JS.API.CONSUMER.CREATE.sympozium.kYbVWNhZ.sympozium.tool.exec.result.bmad-ensemble-product-manager-seq-45611
```

They are leftovers from runs whose pods were killed on the NotReady node (`vpfbs`). Boot
**recovered 21 consumers → jsz now shows 6**, so JetStream already reaped 15 stale ephemerals via
the inactivity threshold; the remaining 4 linger because their owning crew pods can't restart
(o11y memory PVC stuck `Terminating` → crew Pending). They carry 8 undelivered messages total and
will age out once the crew is recreated. This is the **ephemeral-consumer-leak** smell already
flagged as **M2 in the ISI-1468 review** of the eventbus self-heal — low harm here, but it
corroborates that abrupt node loss + high run churn leaves orphaned per-run ephemerals.

**Q3 — "NATS down" symptom → NotReady node `vpfbs`. Confirmed for the record.**
NATS/JetStream is healthy (0 restarts, `/healthz` ok, `api.errors: 0`). The eventbus self-heal
(ISI-1466/1470, live on `isi1406-70d677d`) **held through the node loss**: the controller log
(300 lines) has **zero** eventbus/nats/reconnect/re-establish lines, and Dynatrace shows agent
runs continued after the reschedule (see below) — so the router consumers kept delivering.

## Telemetry evidence (Dynatrace, dynatrace-dev)

`sympozium.eventbus.connected` gauge is Prometheus-only (not shipped to Dynatrace), so drain
health was confirmed indirectly via span continuity — `sympozium.agent.run` per hour (UTC):

```
00:00Z → 26 runs   01:00Z → 8 runs   06:00Z → 3 runs   (node reschedule ~22:38Z 06-30)
```

Runs completing after the node loss ⇒ `agent.run.completed` / `channel.message.received` events
were delivered ⇒ channel-router consumers drained normally post-incident.

## Slow-subscription warnings — explained (not a crash, not a drain regression)

`$JS.API.STREAM.UPDATE.sympozium took too long: 7.3–10.5s` and
`$JS.API.CONSUMER.CREATE.… took too long: 10.5s` (00:21 CEST) are **JetStream store contention on
the NFS-backed `/data/jetstream`** during the boot restore (11 s for 1,209 msgs + 21 consumers) and
a subsequent burst of per-run ephemeral consumer creation. A performance smell on NFS-backed
JetStream storage, not a crash and not related to consumer drain.

## Recommendations

1. **ProxOps (in-flight):** after clearing the o11y PVC finalizer and recreating the crew, confirm
   the 4 orphaned ephemerals (`QUa3Y7Cm`, `dJ0Yiv2G`, `fkzNPlAc`, `y8fkzwTJ`) age out via the
   inactivity threshold. No manual stream surgery needed — they self-reap.
2. **Follow-up (Architect/eventbus, corroborates ISI-1468 M2):** consider an explicit
   `InactiveThreshold` on per-run ephemeral consumers (`agent.followup.*`, `tool.exec.result.*`) so
   orphans from pod loss reap faster than the current default, and don't accumulate under repeated
   node churn.
3. **Infra:** the NFS `/data/jetstream` slow-subscription warnings recur under load — worth a
   separate look at JetStream store IOPS/latency (local PV vs truenas-nfs-csi) if boot-recovery
   time or `$JS.API` latency becomes user-visible. Not blocking.

**No consumer-drain regression. No action required on the eventbus code path.**
