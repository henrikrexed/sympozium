# ISI-1497 — Live Slack routing trace validation (2026-07-01, 16:22 CEST test batch)

Board (BigBoss) ran a live Slack validation of the shipped ISI-1497 feature and asked:
*"it should be the cto responding… but the actual slack channel workload receiving the
message is a bit random… can you check the traces are correct? … [`0bd88b…`] correspond to
message sent to the architect (with @architect) — is it the expected behavior?"*

Environment: `dynatrace-dev` (oat05854), cluster `observable-llm`. DQL gotcha: `toString(trace.id) == "…"`.

## TL;DR

- **All three traces are structurally valid** ✅ (1 root each, no dangling spans, well-formed
  nesting, cross-run stitching intact).
- **The "random receiving workload" is EXPECTED and cosmetic.** In a shared-app Ensemble every
  persona runs its own Slack Socket-Mode pod; Slack delivers the `event_callback` to whichever
  connection it picks, so the `slack.message.received` root lands on a *random* persona pod
  (`testing-architect`, `architect`, …). That pod does **no work** — the single-leader
  `ChannelRouter` immediately re-routes to the **designated `slackListener` (CTO)**. Confirmed in
  all three traces (`sympozium.slack.routing.slack_listener = cto`, run created for
  `bmad-ensemble-cto-*`). This is the intended Option-2 design.
- **The `@architect` message was NOT routed to the architect — this is a real gap, not a trace
  error.** The message text was `"1. @architect @winston please revew this PR 15"`. The current
  `@name` parser (`extractNameMention`, C3 / ISI-1500) only recognizes a mention when it is the
  **first token** of the message. Because the text led with `"1. "`, `@name` routing never fired
  and it fell through to the `slackListener` (CTO). So BigBoss's PR-review request reached the CTO
  instead of the Architect.

## The three traces

| trace.id | inbound text | receiving pod (cosmetic) | router decision | ran | verdict |
|---|---|---|---|---|---|
| `3044f679c82562e2d8f87ce89c140d43` | `Can you make progress and start the implementation of <https://…>` | `bmad-ensemble-architect` | `slack_listener=cto` | **CTO** (`…cto-ch-2rbvp`) | ✅ correct (plain msg → CTO) |
| `45bc7305c625bd04abc1b7e24f6bb840` | `this is the repo: <https://github.com/isItObservable/sympozium-todo-demo>` | `bmad-ensemble-testing-architect` | `slack_listener=cto` | **CTO** (`…cto-ch-wght2`), then delegated `cto→brainstormer` | ✅ correct (plain msg → CTO; autonomous delegation lane) |
| `0bd88b46f3697048f1984a9c998da84b` | `1. @architect @winston please revew this PR 15` | `bmad-ensemble-testing-architect` | `slack_listener=cto`, **no `mention`/`delegate` attr** | **CTO** (`…cto-ch-fvv4k`) | ⚠️ gap — `@architect` ignored, should have gone to Architect |

Structure per trace (all `roots=1`, no dangling):

- `3044f679…` — 751 spans, 1 run (CTO).
- `45bc7305…` — 590 spans, **2 runs** sharing one trace.id; carries a valid
  `sympozium.delegation.handoff` (`lane=delegation`, `source=cto`, `target=brainstormer`) — the
  delegation lane and cross-run stitching (ISI-1484/1488) are proven live.
- `0bd88b46…` — 243 spans, 1 run (CTO). Nesting: `slack.message.received` (on the
  testing-architect pod) → `channel_router.handle_inbound` (controller, `slack_listener=cto`) →
  `agentrun.create_job` → `agentrun.reconcile ×122` → `gen_ai.chat ×9` / `gen_ai.execute_tool ×8`
  / `sympozium.skill.exec ×7` / `memory POST /search`. All correctly parented.

## Root cause of the `@architect` miss

`internal/controller/channel_router.go :: extractNameMention` matches only:

1. a **leading** `@name` (`strings.HasPrefix(t, "@")`), or
2. a **leading** `name:` prefix.

`"1. @architect @winston please revew this PR 15"` starts with `"1. "`, so neither branch matches
→ `extractNameMention` returns `("", text, false)` → `@name` routing is skipped → the message
falls through to `resolveSlackReceiver` → CTO. Confirmed in the controller log:

```
14:42:31Z channel-router Received channel message  instance=bmad-ensemble-testing-architect
          text="1. @architect @winston please revew this PR 15"
14:42:31Z channel-router Routing to SlackListener persona  listener=bmad-ensemble-cto
```

Secondary gap: even with mid-message parsing, `@winston` would not resolve — `resolveNamedDelegate`
exact-matches the token against `Name` / `DisplayName`, and the architect's `DisplayName` is
`"Architect (Winston)"`, not `"Winston"`. Only the `@architect` token (matching config `Name`)
would resolve today.

## Is this the expected behavior?

- **Receiving-pod randomness:** yes, expected & harmless — the SlackListener re-route makes the
  receiving pod irrelevant; work always lands on the CTO for un-addressed messages.
- **`@architect` → CTO:** working *as coded* in the C3 slice (leading-token only), but **not what
  ISI-1497 asked for.** The epic requirement is *"when … the message **contains** @&lt;name&gt; …
  delegate to this agent"* — i.e. mention-anywhere. The shipped parser is narrower. Filed as a
  follow-up child under ISI-1497 (owner: CTO Alfred) to extend `extractNameMention` to detect a
  persona `@mention` anywhere in the message (while still ignoring `@here/@channel/@everyone`,
  emails, and `word:` prefixes), plus optional display-name-token matching so `@winston` resolves.

## DQL used

```
fetch spans, from:now()-8h
| filter in(toString(trace.id), array("45bc…","3044…","0bd88b…"))
| summarize spans=count(), roots=countIf(isNull(toString(span.parent_id))),
            runs=countDistinct(sympozium.agent_run.id), by:{trace.id}

fetch spans, from:now()-8h | filter … | fields span.name, sympozium.instance,
  sympozium.slack.routing.slack_listener, sympozium.slack.routing.delegate,
  sympozium.slack.routing.mention, handoff.lane, handoff.source_persona, handoff.target_persona
```
