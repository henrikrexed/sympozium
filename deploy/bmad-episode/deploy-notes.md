# Sympozium episode — deploy & Slack notes (for the README author, ISI-1387)

These are the exact, ordered steps the README/tutorial should walk through. Part
of the Sympozium episode (ISI-1384). Owner of this config: Observability Agent
(ISI-1386). Cluster bring-up (CNI/MetalLB/csi-driver-nfs) is the sibling ProxOps
issue **ISI-1385** — `deployment.sh` runs *after* that cluster is reachable.

> **Live-validated on 2026-06-23** against the rebuilt `observable-llm` workload
> cluster (ISI-1385). The control plane, `bmad` SkillPack, `bmad-ensemble`
> PersonaPack (3 Running instances), per-agent memory ConfigMaps, and Slack
> secret all deploy cleanly with `./deployment.sh --skip-platform`. Three fixes
> landed during that run (see **Gotchas** below). Still external at validation
> time: (1) the macstudio must actually serve Ollama with the two qwen models for
> live inference; (2) real Slack tokens are needed to move the channel from
> `Disconnected` to connected.

### Gotchas found during the live deploy (already fixed in this repo)

- **`--skip-platform` skips cert-manager too.** cert-manager is installed inside
  the platform step, but the Sympozium chart's admission webhook needs it
  (`certManager.enabled: true`). On a ProxOps-provisioned cluster that lacks
  cert-manager, install it first:
  `kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.1/cert-manager.yaml`
  then `kubectl -n cert-manager rollout status deploy/cert-manager-webhook`.
- **StorageClass name.** The rebuilt cluster's default SC is `truenas-nfs-csi`
  (not the episode's `nfs-csi`, which is only created when you run *with* the
  platform step). `values.yaml` now targets `truenas-nfs-csi` so the NATS PVC binds.
- **Image registry.** The fork (`ghcr.io/henrikrexed/sympozium`) publishes the
  Helm *chart* but not the control-plane *images*. Those live upstream
  (`ghcr.io/alexsjones/sympozium/{controller,apiserver,webhook}:v0.2.0`), which
  `values.yaml` now points at.
- **Chart namespace template.** `templates/namespace.yaml` is now gated behind
  `createNamespaceResource` so it no longer collides with `helm --create-namespace`.

## What gets deployed

| Layer | Component | How |
|-------|-----------|-----|
| Platform | MetalLB (+ address pool) | `deployment.sh` → manifest + `metallb-pool.yaml.tmpl` |
| Platform | csi-driver-nfs + default `nfs-csi` StorageClass | Helm + `nfs-storageclass.yaml.tmpl` |
| Platform | cert-manager (webhook TLS prereq) | `deployment.sh` |
| Platform | kgateway + Gateway API CRDs (optional) | `--with-kgateway` |
| Control plane | Sympozium (controller, apiserver, webhook, NATS, OTel collector) | Helm `charts/sympozium` + `values.yaml` |
| Skills | custom `bmad` SkillPack | `config/skills/bmad.yaml` |
| Ensemble | `bmad-ensemble` PersonaPack (3 agents) | `personapack-bmad-ensemble.yaml` |

## One-shot deploy

```bash
export KUBECONFIG=/path/to/observable-llm.kubeconfig   # the rebuilt workload cluster
export MACSTUDIO_IP=<mac-studio-ip>        # Mac Studio running Ollama (REQUIRED) — CONFIRM with the board
export METALLB_IP_RANGE=10.0.0.240-10.0.0.250
export NFS_SERVER=10.0.0.10
export NFS_SHARE=/export/sympozium
export SLACK_BOT_TOKEN=xoxb-...            # optional but needed for Slack
export SLACK_APP_TOKEN=xapp-...            # optional (Socket Mode)

./deployment.sh                            # full stack (greenfield cluster)
# ./deployment.sh --with-kgateway          # also install kgateway
```

**On a ProxOps-provisioned cluster (the normal episode path):** MetalLB,
csi-driver-nfs, and kgateway are already installed, so run with `--skip-platform`
— but install cert-manager first (see Gotchas above). Only `MACSTUDIO_IP` (and
the Slack tokens, if demoing Slack) are required in that mode:

```bash
export KUBECONFIG=/path/to/observable-llm.kubeconfig
export MACSTUDIO_IP=<mac-studio-ip>        # CONFIRM with the board; 10.0.0.185 (old) now overlaps the MetalLB pool
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.1/cert-manager.yaml
kubectl -n cert-manager rollout status deploy/cert-manager-webhook --timeout=180s
./deployment.sh --skip-platform
```

## The Ensemble (agents, models, flow)

> **Superseded — see `ensemble-bmad-crew.yaml`.** The live `observable-llm`
> deploy runs the **10-agent** v2.0.0 Ensemble (ISI-1394 migration), not the
> 3-agent design described below. The canonical manifest is
> `ensemble-bmad-crew.yaml` in this directory.
>
> **Models (ISI-1396):** all 10 agents now run on **`qwen3.6:latest`**. The
> heavyweight `qwen3.5:122b` was dropped from architect / code-reviewer /
> testing-architect / devops-engineer: on the Mac Studio its warm single-call
> latency (~428s) exceeds the agent-runner's hardcoded ~5-min per-request
> deadline, and its 81 GB footprint evicts/cold-reloads the lighter model,
> causing `context deadline exceeded` + `timeout` AgentRun failures. See the
> header of `ensemble-bmad-crew.yaml` for the full measurement + rationale.

Three agents collaborating via the **BMAD** workflow. Handoff is asynchronous —
agents never call each other; they pass work through GitHub PRs/issues, shared
`MEMORY.md`, and Slack messages.

| Agent | BMAD phase | Local model (macstudio) | Skills |
|-------|-----------|--------------------------|--------|
| **tech-lead** | 1–4: analysis → PRD → architecture → stories, + merge | `qwen3.6:latest` | bmad, github-gitops, code-review, software-dev |
| **coding** | 5: implementation | `qwen3.5:122b` (heavyweight) | bmad, software-dev, github-gitops, code-review |
| **code-review** | 6: adversarial review | `qwen3.6:latest` | bmad, code-review, github-gitops |

**Flow / handoff loop:**

```
tech-lead  ──(story w/ acceptance criteria)──▶  coding
coding     ──(PR + HANDOFF block)───────────▶  code-review
code-review ─(APPROVE)────────────────────────▶ tech-lead ──(merge)
           └(REQUEST_CHANGES)───────────────▶  coding   (loop)
```

Every handoff is posted as a PR/issue comment **and** mirrored to Slack so a human
can watch the loop live.

### Local models on the macstudio

Both models are served by **Ollama on the Mac Studio** (OpenAI-compatible API on
port 11434). On the macstudio:

```bash
OLLAMA_HOST=0.0.0.0 ollama serve     # bind to all interfaces so pods can reach it
ollama pull qwen3.6:latest
ollama pull qwen3.5:122b
ollama list                          # confirm both are present
```

> **Why a baseURL patch?** A Sympozium PersonaPack propagates each persona's
> `model` to the generated `SympoziumInstance`, but it does **not** carry
> `baseURL`. The macstudio is an external host (not a cluster node), so
> `deployment.sh` patches `spec.agents.default.baseURL =
> http://$MACSTUDIO_IP:11434/v1` onto each generated instance
> (`bmad-ensemble-tech-lead`, `bmad-ensemble-coding`, `bmad-ensemble-code-review`)
> after the pack activates. Egress to port 11434 is already allowed by the default
> network policies.

## Agent memory (showcase)

Persistent memory is enabled for all three agents. Each agent gets a
`<instance>-memory` ConfigMap holding `MEMORY.md`, mounted read-only into the pod,
prepended to context, and patched after each run. We seed it so the showcase has
content from run #1:

```bash
# View what an agent remembers
kubectl -n default get configmap bmad-ensemble-coding-memory \
  -o jsonpath='{.data.MEMORY\.md}'
```

Talking points for the episode: continuity across runs with **no external
database** (memory lives in etcd); seeds vs. learned memory; how the code-review
agent accumulates recurring-defect patterns over time.

## Slack integration (Socket Mode)

Adapted from the repo's Channels docs (`docs/concepts/channels.md`). Set up once:

1. Create a Slack app → enable **Socket Mode**.
2. **App Home → Messages Tab**: enable it and allow users to message the app.
3. **Event Subscriptions** (bot events): `message.im`, `message.channels`,
   `app_mention`.
4. Generate tokens: bot token `xoxb-...` (OAuth & Permissions) and app token
   `xapp-...` (Basic Information → App-Level Tokens, scope `connections:write`).
5. Reinstall the app after changing scopes/events.
6. Export `SLACK_BOT_TOKEN` and `SLACK_APP_TOKEN` before running `deployment.sh`
   — it provisions the `bmad-slack-tokens` secret and binds the `slack` channel
   to every agent.

> If `SLACK_APP_TOKEN` is omitted, Sympozium falls back to Events API mode, which
> needs a publicly reachable webhook URL — avoid that for the tutorial; use Socket
> Mode.

Verify the channel:

```bash
kubectl -n default get sympoziuminstance -l sympozium.ai/persona-pack=bmad-ensemble \
  -o custom-columns=NAME:.metadata.name,CHANNELS:.status.channels
kubectl -n default logs -l sympozium.ai/channel=slack -f
```

Then DM the bot (or @mention it in a channel) — it spawns an AgentRun and replies
in-thread.

## Observability (bonus for an IsItObservable episode)

The Helm values enable the built-in OTel collector and turn on instance-level
observability (`samplingRatio: 1.0`). Agent runs, the controller, apiserver, and
mcp-bridge all emit OTLP. Point the collector's exporter at your backend (e.g.
Dynatrace) to show end-to-end agent traces, or set `observability.endpoint` to an
external OTLP endpoint in `values.yaml`.

## Files in this episode bundle

```
deployment.sh                                  # end-to-end deploy orchestrator (repo root)
config/skills/bmad.yaml                         # custom BMAD SkillPack
deploy/bmad-episode/values.yaml                 # Helm values for the episode
deploy/bmad-episode/personapack-bmad-ensemble.yaml
deploy/bmad-episode/platform/metallb-pool.yaml.tmpl
deploy/bmad-episode/platform/nfs-storageclass.yaml.tmpl
deploy/bmad-episode/deploy-notes.md             # this file
```
