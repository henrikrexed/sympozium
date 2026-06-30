# ISI-1436 — Slack-triggered bmad work: k8s + Dynatrace investigation

**Date:** 2026-06-30 ~12:10Z · **Env:** observable-llm (release `isi1406-9a900dc`, helm rev 16) · **DT:** oat05854
**Board ask:** "i triggered some work by sending a slack message to the bmad crew. can we investigate on the data generated in k8s and in dynatrace?"

## TL;DR

The Slack message landed cleanly and produced **one well-correlated distributed trace** spanning
Slack inbound → channel routing → controller reconcile → agent run → 14 LLM calls → 25 tool calls →
12 skill execs → memory store+search. All three pillars correlate under a single `trace.id`. The run
**Succeeded**, was **not** duplicated, and carried its Slack reply context intact.

## What got triggered (k8s)

| Field | Value |
|---|---|
| AgentRun | `bmad-ensemble-o11y-engineer-ch-bwl4w` (ns `default`) |
| Created → Completed | `12:05:40Z` → `12:08:32Z` (**158s**, phase **Succeeded**) |
| Source | `sympozium.ai/source=channel`, `source-channel=slack` (the `-ch-` suffix = channel-triggered) |
| Routed to | **o11y-engineer** persona (correct `@`-resolution, not `primary` fallback) |
| Task | "make progress and start the implementation of `github.com/isItObservable/sympozium-todo-demo`" |
| Model | `qwen3.6:latest` via ollama, mode `task` |
| Skills loaded | `bmad`, `software-dev`, `github-gitops` (repo `isItObservable/sympozium-todo-demo`), `memory` |
| Dedup | `sympozium.ai/dedup-key=slack/Ev0BDPRV32LF` — **single run, no duplicates** (ISI-1427 holding) |
| Reply context | `reply-channel=slack`, `reply-chat-id=D0AHDBVFN07` (DM), thread+message-ts all present (ISI-1442 holding) |
| Token usage | 253,069 in / 7,683 out / **260,752 total**, **25 tool calls** |
| Successors | none — no `sequential-triggered` / `delegation-triggered` label. Single direct channel response; the model did the dev work itself and did not emit `delegate_to_persona`. |

## Telemetry (Dynatrace) — trace `4ef7dad8ead41c5ae9f24801dab1ef39`

Single trace, span-name breakdown:

| Span | Count | Meaning |
|---|---|---|
| `slack.message.received` | 1 | Slack inbound event captured |
| `channel_router.handle_inbound` | 1 | Routed to o11y-engineer |
| `agentrun.create_job` | 1 | Controller created the run |
| `agentrun.reconcile` | 44 | Reconcile loop |
| `sympozium.agent.run` | 1 | Root run span (158s — closed cleanly, ISI-1483) |
| `gen_ai.chat` | 14 | LLM calls — `qwen3.6:latest`, 253,069 in / 7,683 out (**matches k8s tokenUsage exactly**) |
| `gen_ai.execute_tool` | 25 | Tool calls (**matches k8s toolCalls=25**) |
| `sympozium.skill.exec` | 12 | Skill executions |
| `memory POST /store` | 1 | Memory write — correlated **in-trace** |
| `memory POST /search` | 1 | Memory read — correlated **in-trace** |
| `agentrun.extract_result` | 40 | Result extraction |
| `HTTP POST` | 2 | Outbound HTTP |

**Correlation verdict:** Slack → router → controller → run → LLM → tools → skills → memory all share one
`trace.id`. This is exactly the end-to-end correlation the ISI-1406/1482/1486 observability line was built for,
and it is now demonstrably working on a real Slack-originated stimulus. Three pillars in harmony. ✓

## Notes / honest caveats

- **`skill.exec` all bucket to `skill.name=command-executor`** (12/12). This is the known ISI-1461 limitation:
  only *targeted* `execute_command` (with a `target`) resolves to a named skill; generic command execution
  buckets to `command-executor`. Expected, not a regression.
- **Separate, unrelated traffic** in the same window (NOT this Slack message): a `schedule-1` batch and an earlier
  sequential chain (10:36–11:20Z) from readiness/scheduled stimuli, each with their own trace.id. Several of those
  scheduled runs **Failed** with `OpenAI API error: context deadline exceeded` (qwen timeouts under concurrent load).
  The Slack run itself was unaffected and succeeded. Worth a follow-up on scheduled-fan-out concurrency vs. ollama
  capacity, but out of scope for this investigation.

## Repro queries

```dql
# span breakdown (note: must use toString(trace.id) — typed field)
fetch spans, from:now()-90m
| filter toString(trace.id) == "4ef7dad8ead41c5ae9f24801dab1ef39"
| summarize spans=count(), by:{span.name} | sort spans desc
```
```bash
kubectl get agentrun -n default bmad-ensemble-o11y-engineer-ch-bwl4w -o jsonpath='{.status}'
```

---

## Follow-up: board says "we still have X times the message in the Slack message — ISI-1470 not resolved" (2026-06-30)

The board observed that duplicate Slack **replies** still appear. My original investigation only proved the
**inbound** run was not duplicated (dedup-key collapsed it to one AgentRun). It did **not** address the
**outbound** reply path, which is where "X identical messages" actually comes from. So I checked the live
deployed controller directly.

### Evidence — the deployed code posts each reply exactly once

Deployed image: `ghcr.io/henrikrexed/sympozium/controller:isi1406-9a900dc` (helm rev 16), **single** controller
replica. Commit `9a900dc` **includes** `1348e73` — ISI-1430's *durable queue-group consumer to collapse
ChannelRouter fan-out*.

```
# controller logs, full 12h window
$ kubectl logs deploy/sympozium-controller-manager --since=12h | grep -c 'Routed agent response to channel'
1
# the apiserver posts nothing to Slack (no second poster)
$ kubectl logs deploy/sympozium-apiserver --since=3h | grep -iE 'slack|routed.*channel'   # -> empty
```

For the Slack run `bmad-ensemble-o11y-engineer-ch-bwl4w` the controller logged **exactly**: 1× `Received channel
message`, 1× `Created AgentRun`, **1× `Routed agent response to channel` (responseLen 490)**. One outbound reply,
not X. Fan-out would have shown N outbounds for the one run; it showed one.

**Conclusion:** the duplicate-reply bug (ISI-1430 / ISI-1427) is **fixed and live** in the running deployment.
The duplicate messages still visible in Slack are **stale channel history** posted by *earlier* deploys that
predate the ISI-1430 queue-group fix — Slack retains them forever; they are not being newly generated.

### ISI-1470 *is* genuinely not deployed — but it is a different symptom

The board is correct that ISI-1470 is unresolved **in the deployed image**. The deployed self-heal stack carries
the **pre-ISI-1470 gate**: the `streamGen`-staleness check sits *inside* the `if err != nil` fetch branch
(`nats.go:~460`), exactly the unreachable-gate bug `bf69188` fixes by hoisting the check to the **top** of the
fetch loop. Confirmed: `git merge-base --is-ancestor bf69188 9a900dc` → **NO**.

**However ISI-1470's symptom is channel-routing *death* after a full-store NATS restart (Active Consumers stuck
at 0), not steady-state duplicate replies.** NATS currently shows **0 restarts / 4h19m uptime**, consistent with
the clean 12h outbound count. So the un-deployed ISI-1470 is a real resilience gap, but it is **not** the cause of
the duplicate replies the board sees.

### Why ISI-1470 can't be a one-line cherry-pick onto this branch

Attempting `git cherry-pick bf69188` onto `isi1436-integration-test` fails to compile:
`n.streamGen undefined (type *NATSEventBus has no field or method streamGen)`. This branch's eventbus self-heal is
the **earlier lineage** (`recreate()` + `streamHealer` + `healing atomic.Bool`); it never received ISI-1466's
`730b003`, which introduced the `streamGen` generation-tracking plumbing that `bf69188` builds on. **Deploying
ISI-1470 here requires porting the full `730b003 → bf69188` stack (plus the `nats_recreate_integration_test.go`
proof), not a single commit** — that is ISI-1470 / ProxOps deploy work, gated by the embedded-JetStream test.

### Disposition

- Duplicate-reply concern: **already resolved in the live deploy** (ISI-1430), proven by 1 outbound / 12h.
- ISI-1470: legitimately not deployed; tracked on its own ticket. Requires a proper port of the streamGen stack +
  CI build + helm tag-swap, owned by ProxOps. Not a regression introduced by this branch.
