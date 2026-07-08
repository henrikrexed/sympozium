# ISI-1625 — Dashboards updated for `gen_ai.*` semantic convention (coalesce old+new)

**Date:** 2026-07-08 · **Env:** oat05854 (`dynatrace-dev`) · **Tool:** dtctl (`get`/`query`/`apply`) · **Agent:** Observability Agent
**Parent:** ISI-1624 (board ask, Henrik) — coalesce so dashboards stay meaningful across the ISI-1590 GenAI-semconv rollout.

## Coalesce contract (old ← new, per board directive)

Standard OTel GenAI semantic-convention renames bridged with `coalesce(new, old)`:

| Concept | New (gen_ai.* / ISI-1590) | Legacy fallback |
|---|---|---|
| Provider / framework | `gen_ai.provider.name` | `gen_ai.system` |
| Input tokens | `gen_ai.usage.input_tokens` | `gen_ai.usage.prompt_tokens` |
| Output tokens | `gen_ai.usage.output_tokens` | `gen_ai.usage.completion_tokens` |
| Model | `gen_ai.request.model` | `gen_ai.response.model` |
| LLM operation | `gen_ai.operation.name` incl. `"chat"` | `"generate_content"` (ADK) |

## Live-data grounding (why coalesce is the right call)

Probed spans on oat05854 (14–30d). The `gen_ai.system` → `gen_ai.provider.name` migration is **live and mixed** — exactly the rollout the ticket describes:

| service | provider split (old `gen_ai.system` / new `gen_ai.provider.name`) | tokens |
|---|---|---|
| `sympozium-agent-runner` | 6025 old / 1012 new · agent.name & traceloop.span.kind partial (42) | input/output only, no legacy |
| `paperclip` | 2636 old / 188 new | input/output only |
| `kagent` agents (k8s_agent, bmad_*, helm_agent, promql_agent) | old `gen_ai.system`; **no** prompt/completion legacy, **no** chat/traceloop.span.kind | input/output only |

No service emits `gen_ai.usage.prompt_tokens`/`completion_tokens` today → token-rename branch is **defensive/forward-looking** (survives collector/SDK version drift). Provider + model coalesce is **actively needed** now.

## Dashboard inventory + disposition

| Dashboard | ID | GenAI tiles | Action |
|---|---|---|---|
| **kagent — Agentic Efficiency** | `dfe6dfc4-…` | 13 | ✅ coalesced + **deployed live** (23/23 tiles validated) |
| **kagent — Health & Availability** | `4b46b649-…` | 1 | ✅ coalesced + **deployed live** (23/23 tiles validated) |
| **Paperclip / Sympozium Observability** | `4e1f7843-…` | ~7 | ✅ already coalesced under ISI-1590 (model coalesce, sympozium scope, +SLO +SRG) — see `ISI-1590-dashboard-slo-srg-coalesce.md` |
| **CrewAI — Agentic Efficiency** | repo (isItObservable/CrewAI) | 23 | ✅ coalesced + syntax-validated; ⛔ repo-commit blocked (403) — local commit `9073bb1` ready to push; no live cluster (ISI-1588 not launched) |
| **CrewAI — Health** | repo | 10 | ✅ coalesced + syntax-validated; ⛔ same repo blocker |
| **Agent Benchmark (kagent vs Sympozium vs HolmesGPT)** | `920fd31f-…` | frozen | ⏸️ out of scope — fixed March time-range snapshot + static lookup; span-name based, not a live-rollout dashboard |

## Verification

- **kagent (live):** both dashboards redeployed via `dt-app-dashboards` validated deploy script — all 23 tiles each `query=✓ data=✓ viz=✓`. All 14 changed queries run clean and return **identical** results to pre-change (regression-safe: no legacy/chat data exists to diverge, coalesce only adds fallback branches).
- **CrewAI:** all 33 changed queries syntax-validated against oat05854 (return 0 rows — no `observable-crewai` cluster live — but prove valid DQL). Live-render verification deferred until ISI-1588 cluster launch.

## Residual blocker

Repo mirrors for `isItObservable/Kagent` (kagent JSON) and `isItObservable/CrewAI` (CrewAI JSON) need repo-write creds — same durable gate as **ISI-1622** (owner: Henrik; fine-grained PAT). Live kagent dashboards are already the source-of-truth deployment; Sympozium repo record committed here.
