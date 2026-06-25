# Channels

Channels connect Sympozium to external messaging platforms. Each channel runs as a dedicated Kubernetes Deployment. Messages flow through NATS JetStream and are routed to AgentRuns by the channel router.

## Supported Channels

| Channel | Protocol | Self-chat | Status |
|---------|----------|-----------|--------|
| **WhatsApp** | WhatsApp Web (multidevice) via `whatsmeow` | Owner can message themselves to interact with agents | **Stable** |
| **Telegram** | Bot API (`tgbotapi`) | Owner can message themselves to interact with agents | **Stable** |
| **Discord** | Gateway WebSocket (`discordgo`) | — | **Alpha** |
| **Slack** | Socket Mode (`slack-go`) | — | **Alpha** |

!!! info
    **Stable** — tested and actively used. **Alpha** — implemented but not yet production-tested.

Channels are optional. You can always interact through the TUI, web dashboard, or by creating AgentRun CRs directly with kubectl.

## Connecting Channels

Connect channels during onboarding or via the TUI edit modal:

| Channel | How to connect |
|---------|----------------|
| **Telegram** | Create a bot with [@BotFather](https://t.me/BotFather), get the token, pass it during onboarding or set it in the Agent channel config |
| **Slack** | Create a Slack app with Socket Mode enabled, add the bot/app token during onboarding |
| **Discord** | Create a Discord bot, grab the token, and connect it during onboarding |
| **WhatsApp** | Use the WhatsApp Business API — Sympozium displays a QR code in the TUI for pairing |

## Slack Setup (Socket Mode)

For reliable Slack connectivity, configure your Slack app with both tokens and required app settings:

- Provide both secrets in the channel secret:
    - `SLACK_BOT_TOKEN` (`xoxb-...`)
    - `SLACK_APP_TOKEN` (`xapp-...`)
- Enable **App Home → Messages Tab** and allow users to message the app
- Enable **Socket Mode**
- Add bot event subscriptions:
    - `message.im`
    - `message.channels`
    - `app_mention`
- Add bot OAuth scopes:
    - `chat:write` — post messages
    - `chat:write.customize` — **required** to post each message under a per-agent name and icon (see below)
- Reinstall the app after changing scopes or event subscriptions

!!! warning
    If `SLACK_APP_TOKEN` is omitted, Sympozium falls back to Slack Events API mode, which requires a publicly reachable webhook URL.

## Per-Agent Identity (Name & Icon)

An Ensemble shares a single Slack app/bot token, so by default every agent would
post under the same bot name and avatar. Sympozium overrides the sender on each
outbound message so a reply from *Winston (Architect)* looks different from one by
*John (PM)* in the same channel. This requires the `chat:write.customize` bot
scope above.

Each outbound message can carry three optional attribution fields
(`OutboundMessage` in `internal/channel/types.go`, mirrored on the IPC
`protocol.go`):

| Field | Slack payload key | Meaning |
|-------|-------------------|---------|
| `Username` | `username` | Display name shown as the sender |
| `IconURL` | `icon_url` | Avatar image URL (e.g. `https://…/winston.png`) |
| `IconEmoji` | `icon_emoji` | Avatar emoji shortcode (e.g. `:robot_face:`) |

`icon_url` and `icon_emoji` are mutually exclusive in the Slack API; when both are
set the URL wins (`buildPostMessagePayload` in `channels/slack/main.go`).

### Name — automatic

The sender **name** is populated automatically. Each Slack channel pod receives
`AGENT_DISPLAY_NAME`, sourced from the agent's `displayName`
(`Ensemble.spec.agentConfigs[].displayName`); if unset it falls back to the
instance name. When a message carries no explicit `Username`, the pod's own
display name is used, so an Ensemble's shared bot still posts as the right agent
with no extra configuration.

### Icon — how to import a per-agent avatar

!!! note "Current state"
    The Slack transport **honors** a per-message `icon_url`/`icon_emoji`, but no
    controller field populates the icon from agent config yet — only the *name*
    is wired end-to-end. Per-agent icons are the planned follow-up tracked in
    [ISI-1449](https://github.com/) (producer-side population), upstream of the
    name attribution adopted from `sympozium-ai/sympozium#245`.

To give each agent its own avatar, the icon must travel the same path the name
already does — from agent config → pod env → onto each `OutboundMessage`. The
design:

1. **Source of truth — agent config.** Add an optional avatar to each agent in
   the Ensemble spec, alongside `displayName`:

    ```yaml
    apiVersion: sympozium.ai/v1alpha1
    kind: Ensemble
    spec:
      agentConfigs:
        - name: architect
          displayName: "Winston (Architect)"
          iconEmoji: ":triangular_ruler:"     # or:
          # iconUrl: "https://example.com/avatars/winston.png"
    ```

    Use `iconEmoji` for a Slack emoji shortcode (simplest — no hosting), or
    `iconUrl` for a hosted image (square PNG, ≥512×512 recommended). If both are
    set, `iconUrl` takes precedence.

2. **Carry it to the pod.** The controller injects the resolved value as
   `AGENT_ICON_URL` / `AGENT_ICON_EMOJI` env on the Slack channel Deployment,
   exactly as it already injects `AGENT_DISPLAY_NAME`.

3. **Stamp each message.** The channel router sets `OutboundMessage.IconURL` /
   `IconEmoji` (per-message override), falling back to the pod's own env default
   when a message carries none — mirroring the existing `Username` fallback.

Until step 1–2 ship, an icon will only appear if a producer sets `IconURL`/
`IconEmoji` on the message directly; there is no user-facing knob for it on the
Ensemble line today.

!!! tip "Emoji vs. URL"
    Prefer `iconEmoji` for getting started — it needs no image hosting and
    renders consistently. Switch to `iconUrl` when you want real brand/agent
    avatars. Both require the `chat:write.customize` scope.
