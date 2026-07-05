# ISI-1436 — Pre-PR trace validation (9:35 → now, 2026-07-01)

Board ask: *"validate the traces generated between 9:35 and now using dtctl; check the
structure is valid. I saw one trace with delegation `b6049a51...704024`, the other did
not have any delegation."*

Environment: `dynatrace-dev` (oat05854). Filter gotcha: `toString(trace.id) == "..."`.

## Verdict

- **Trace structure is VALID.** ✅
- **Correction:** `b6049a517afcf96a9f33513e7d704024` is a **SEQUENTIAL** handoff chain, **not**
  a controller-side delegation. There are **zero `sympozium.delegation.handoff` spans** in
  the whole window. The controller delegation executor (ISI-1436's subject) is deployed and
  active but **fired 0 edges** here.

## What the trace actually is (b6049…704024)

- Window: **07:45:29 → 07:55:08 UTC** (09:45 CEST) — in board's 9:35→now window.
- **404 spans, exactly 1 root** (`slack.message.received`, parent = null), **no dangling roots**.
- Nesting is well-formed:
  `slack.message.received` → `channel_router.handle_inbound` → `agentrun.reconcile` (×140)
  → `agentrun.create_job` (×3) → `sympozium.agent.run` (×3) → chained by
  `sympozium.sequential.handoff` (×2); plus `gen_ai.chat` (36), `gen_ai.execute_tool` (40),
  `sympozium.skill.exec` (33), memory `/store` `/search` `/list` ops — all correctly parented.
- **Cross-run stitching works** (ISI-1484/1488): 3 distinct agent runs
  (`story-writer-ch-8dhqj` → `code-reviewer-seq-18360` → `testing-architect-seq-23671`)
  share ONE `trace.id`.
- Handoff spans carry the full contract: `handoff.lane=sequential`, `source_persona`,
  `target_persona`, `parent_run`, `child_run`.

The second connected trace `1e4ffbf8…91095` is the same shape (also sequential:
story-writer → code-reviewer → testing-architect). The "traces without delegation" the
board saw are the 4 standalone single-`agent.run` traces (cto / devops / challenger channel
runs) with no successor edge — expected and valid.

## Why there is no delegation lane

`sympozium.delegation.edges` over the window (executor IS running):

| decision | count |
|---|---|
| fired | **0** |
| skipped_condition | 18 |
| skipped_depth_cap | 0 |
| skipped_inflight_cap | 0 |

The executor evaluated 18 delegation edges and **skipped every one on unmet `condition`** —
so no `sympozium.delegation.handoff` span was produced. This is correct, safe behavior
(guardrails from ISI-1463 holding; no runaway firing), but it means **we have not yet
captured a positive delegation trace** to validate the delegation lane's structure.

## Delegation lane exercised + validated (board chose `exercise_first`, 2026-07-01)

Created a `cto` probe run `bmad-ensemble-cto-delegprobe-1` (task loaded with
observability/OpenTelemetry keywords). It Succeeded in ~50s; the post-success reconcile ran
the controller delegation executor, which **fired one edge** and created child
`bmad-ensemble-product-manager-deleg-65245`.

**Positive delegation trace `1bfe891358a04985d82ffbdfe64069ad` — VALID:** ✅
- **255 spans, exactly 1 root** (`agentrun.reconcile` — the run was created directly via
  kubectl, so no Slack parent; single root, no dangling), **2 distinct agent runs**.
- `sympozium.delegation.handoff` span present with the full contract:
  `handoff.lane=delegation`, `source_persona=cto`, `target_persona=product-manager`,
  `parent_run=bmad-ensemble-cto-delegprobe-1`, `child_run=bmad-ensemble-product-manager-deleg-65245`.
- **Cross-run stitching works for the delegation lane** (mirrors sequential ISI-1484/1488):
  the child run's spans (55+) carry the **same trace.id** as the parent cto run (4 spans) —
  the child joined the parent trace via the anchored handoff.
- **Guardrails provable from telemetry** — `sympozium.delegation.edges` over the run:
  `fired=1`, `skipped_condition=8` (cto's 9 candidate edges narrowed to K=1),
  `skipped_depth_cap=0`, `skipped_inflight_cap=0`.

Note: the matcher selected `product-manager` (not the intended `o11y-engineer`) because the
model's result text scored that edge's tokens higher — expected behavior for the lexical
fallback matcher; irrelevant to trace-structure validity. Both lanes (sequential + delegation)
now have a captured, structurally-valid, cross-run-stitched trace.

**Gate cleared** — the delegation executor's traces are valid. Ready to open upstream PR #236.

DQL used:
```
fetch spans, from:now()-6h | filter toString(trace.id) == "b6049a517afcf96a9f33513e7d704024"
  | summarize started=min(start_time), ended=max(end_time), spans=count(),
              roots=countIf(isNull(toString(span.parent_id)))
fetch spans, from:now()-6h | filter contains(span.name,"handoff")
  | fields span.name, handoff.lane, handoff.source_persona, handoff.target_persona
timeseries s=sum(sympozium.delegation.edges), by:{decision}, from:now()-6h
```
