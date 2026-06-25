# ISI-1449 — Align fork Slack attribution (item 1) with upstream PR #245

**Status:** item 1 blocked on upstream merge; items 2 + 3 prepared (this doc).
**Date:** 2026-06-25 · **Owner:** ProxOps

Follow-up from ISI-1409. Community contributor **@mvanhorn** opened upstream
**[sympozium-ai/sympozium#245](https://github.com/sympozium-ai/sympozium/pull/245)**
implementing item-1 transport attribution; maintainer **@AlexsJones** approved the
approach on issue #235.

## State of play (verified 2026-06-25)

| Surface | `Username`/`IconURL`/`IconEmoji` data model | Slack transport reads them | Producer-side `Username` population |
|---|---|---|---|
| upstream `main` (`897c5f0` … `318b1a2`) | ❌ not yet (in open PR #245) | ❌ | ❌ |
| upstream **PR #245** (open, `mergeable`, +159/-0) | ✅ adds to `OutboundMessage` | ✅ `sendMessage` | ❌ explicitly deferred |
| our `integration` | ✅ identical fields | ✅ `buildPostMessagePayload` | ✅ but via **retired** PersonaPack/SympoziumInstance CRDs |
| our `isi1406-fork-build` (canonical Ensemble line) | ❌ | ❌ | ❌ |

Confirmed byte-identical data model between #245 and `integration`:

```go
Username  string `json:"username,omitempty"`
IconURL   string `json:"iconUrl,omitempty"`
IconEmoji string `json:"iconEmoji,omitempty"`
```

Key consequence: **attribution does not exist on the Ensemble line at all.** The
canonical line is `isi1406-fork-build` (ISI-1414). `integration`'s producer side is
wired through PersonaPack/SympoziumInstance controllers, which the board retired —
so we cannot port `integration` onto the Ensemble line; we adopt #245 via sync and
re-express the producer side against the Ensemble model.

---

## Item 1 — Sync #245 onto `isi1406-fork-build` (BLOCKED on upstream merge)

#245 is **open**, not merged. Until upstream `main` carries it, there is nothing to
sync. Unblock trigger: **#245 merges to upstream `main`.**

Prepared, ready-to-run once that happens (the fields land only on upstream after
merge; `isi1406-fork-build` has none, so there is **no duplicate-add conflict on our
side** — the "take one copy" note in the issue applies only if we had already
hand-ported the fields, which we deliberately have not):

```sh
git fetch upstream
git checkout isi1406-fork-build && git pull
git checkout -b fix/isi-1449-attribution-sync
# Cherry-pick the #245 merge commit (squash-merge → single SHA),
# or merge upstream/main if a broader sync is wanted:
git cherry-pick <pr245-merge-sha>     # touches: internal/channel/types.go,
                                      #          channels/slack/main.go,
                                      #          channels/slack/main_test.go,
                                      #          docs/concepts/channels.md
# Resolve only if isi1406 has since touched those files (currently it has not):
#   types.go OutboundMessage — keep one copy of each field, omitempty tags intact.
go build ./... && go test ./internal/channel/... ./channels/slack/...
git push origin fix/isi-1449-attribution-sync   # DCO: rebase --signoff first
```

Mirror in `internal/ipc/protocol.go` if #245 did not (the IPC `OutboundMessage`
must carry the same three fields end-to-end controller→channel-pod; on `integration`
both `types.go` and `protocol.go` carry them).

DCO + DCO-signoff required on the fork PR (see `sympozium_fork_pr_dco` memory).
Merge to a fork build branch needs no board code-review.

---

## Item 2 — Producer-side `displayName → Username` (upstream follow-up, prepared)

This is the differentiator we already have on `integration` and the natural
follow-up @mvanhorn/@AlexsJones flagged as deferred in #245. It must be
**re-expressed against the Ensemble model** (not ported from PersonaPack).

### Where it attaches on the Ensemble line

The single outbound producer point is `channel_router.go:492`
(`routeAgentResultToChannel`), where the completed `AgentRun`'s response is turned
into an `OutboundMessage`:

```go
outMsg := channelpkg.OutboundMessage{
    Channel:  replyChannel,
    ChatID:   replyChatID,
    ThreadID: replyThreadID,
    Text:     responseText,
}
```

The display name is already resolvable here: `run` is the AgentRun; the same
chain `injectRelationshipContext` uses (`agentrun_controller.go:2708-2724`) gets us
from AgentRun → Agent CR (`run.Spec.AgentRef`) → `sympozium.ai/agent-config` label →
matching `Ensemble.Spec.AgentConfigs[].DisplayName`.

### Proposed diff sketch (after #245 fields exist on the line)

```go
// in routeAgentResultToChannel, after building outMsg:
if dn := cr.resolveAgentDisplayName(ctx, run); dn != "" {
    outMsg.Username = dn
}
```

```go
// new helper on ChannelRouter — mirrors injectRelationshipContext resolution:
func (cr *ChannelRouter) resolveAgentDisplayName(ctx context.Context, run *v1alpha1.AgentRun) string {
    if run.Spec.AgentRef == "" {
        return ""
    }
    var inst v1alpha1.Agent
    if err := cr.Get(ctx, types.NamespacedName{Name: run.Spec.AgentRef, Namespace: run.Namespace}, &inst); err != nil {
        return ""
    }
    cfgName := inst.Labels["sympozium.ai/agent-config"]
    packName := run.Labels["sympozium.ai/ensemble"]
    if cfgName == "" || packName == "" {
        return ""
    }
    var pack v1alpha1.Ensemble
    if err := cr.Get(ctx, types.NamespacedName{Name: packName, Namespace: run.Namespace}, &pack); err != nil {
        return ""
    }
    for _, ac := range pack.Spec.AgentConfigs {
        if ac.Name == cfgName {
            return ac.DisplayName   // empty DisplayName → falls back to no attribution
        }
    }
    return ""
}
```

Why this shape:
- **Per-message** attribution (not per-pod): the Ensemble shares one Slack bot
  across many agents, so the identity must travel on each message. #245's transport
  already prefers `msg.Username`; this populates it. (The per-pod
  `AGENT_DISPLAY_NAME` defaultUsername fallback from `integration` does **not** fit
  the shared-bot Ensemble — one pod can't have one display name.)
- Empty `DisplayName` → no `Username` set → #245 transport sends without the
  `username` override → backward-compatible, identical to today.
- Optional `IconURL`/`IconEmoji` can be sourced the same way if/when
  `AgentConfig` gains icon fields; out of scope for the first follow-up.

### Upstream contribution form

Because this builds on #245's fields, it can only be a PR **after #245 merges**.
The upstream-friendly version targets the Ensemble controller path (the producer is
controller-side in upstream too). Open as a separate upstream PR titled e.g.
*"feat(channels): populate Slack Username from agent displayName (follow-up to #245)"*.
Fork PAT cannot POST upstream PRs (401) — **Henrik opens via the compare URL**
(see ISI-1435/1436 pattern).

---

## Item 3 — GitHub reply for the board to post (DRAFT)

Outward-facing as project owner, to post on #245 (or issue #235). Henrik posts
manually (gh unavailable to the agent).

> Thanks @mvanhorn — this matches what we landed internally on our fork
> byte-for-byte (`Username` / `IconURL` / `IconEmoji` on `OutboundMessage`, with
> `icon_url` taking precedence over `icon_emoji` in the Slack payload), so we're
> happy to adopt #245 as the canonical implementation rather than carry our own.
> Approving the data-model + transport split is the right call.
>
> On the deferred producer-side piece: we've already run this in production — the
> controller populates `Username` per-message from each agent's `displayName` so a
> single shared Slack bot presents distinct identities across a multi-agent
> Ensemble. Happy to send that as a follow-up PR once #245 is in `main`; it's a
> small controller-side change resolving AgentRun → Agent → display name at the
> outbound point. @AlexsJones — want it as a separate PR on top of #245, or folded
> into the docs note here?

---

## Disposition

- **Item 1:** blocked on #245 merging to upstream `main`. Unblock owner: upstream
  maintainers (@AlexsJones / @mvanhorn). Unblock action: run the prepared sync above.
- **Item 2:** design + diff sketch prepared and grounded in current Ensemble code;
  PR-able only after #245 merges; Henrik opens the upstream PR.
- **Item 3:** reply drafted above for Henrik to post.

No code committed deliberately — adopting via sync (not porting) is the ISI-1410
divergence-hygiene lesson. Re-check #245 status on next touch of this issue.

---

## Board request (2026-06-25): Slack channel config docs + "how do you import the icon for each agent?"

**Delivered:** user-facing docs in `docs/concepts/channels.md` — new **"Per-Agent
Identity (Name & Icon)"** section plus the missing `chat:write` / `chat:write.customize`
bot scopes in the Slack Setup list.

### Architecture answer (Winston)

There are two attribution layers, and they are at *different* maturity:

| Layer | Field(s) | Transport honors it? | Auto-populated from agent config? |
|-------|----------|----------------------|-----------------------------------|
| **Name** | `Username` → `username` | yes — `buildPostMessagePayload` | yes — via `AGENT_DISPLAY_NAME` ← `displayName` (falls back to instance name) |
| **Icon** | `IconURL`/`IconEmoji` → `icon_url`/`icon_emoji` | yes (URL wins over emoji) | **no controller wiring yet** |

So *today* you cannot "import" a per-agent icon through config on the Ensemble
line — the Slack code will render `icon_url`/`icon_emoji` if a producer sets them
on the message, but nothing resolves them from agent config. (The name path is
fully wired; the icon path is the natural symmetric follow-up.)

**Proposed mechanism (documented in channels.md), mirrors the name path exactly:**

1. Add optional `iconEmoji` / `iconUrl` to `Ensemble.spec.agentConfigs[]`
   (`api/v1alpha1/ensemble_types.go`, beside `DisplayName`).
2. Controller injects `AGENT_ICON_EMOJI` / `AGENT_ICON_URL` env onto the Slack
   channel Deployment (mirrors `AGENT_DISPLAY_NAME` injection).
3. Channel router stamps `OutboundMessage.IconURL`/`IconEmoji`, with pod-env
   default fallback — same shape as the existing `Username` fallback.

`iconEmoji` (Slack shortcode, e.g. `:triangular_ruler:`) is the recommended
default — no image hosting; `iconUrl` (square PNG ≥512²) for real avatars. Both
need the `chat:write.customize` scope. This folds into Item 2 (producer-side
population) as an upstream follow-up on top of #245 — not shipped on the Ensemble
line until then.
