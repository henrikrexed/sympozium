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
- Add bot token (OAuth) scopes:
    - `chat:write` — post replies
    - `reactions:write` — add the acknowledgement reactions
    - `app_mentions:read`, `channels:history`, `im:history` — read the events above
    - `chat:write.customize` — **optional**, only needed for per-agent sender attribution (see below)
- Reinstall the app after changing scopes or event subscriptions

!!! warning
    If `SLACK_APP_TOKEN` is omitted, Sympozium falls back to Slack Events API mode, which requires a publicly reachable webhook URL.

### Per-agent sender attribution (optional)

In a multi-agent Ensemble, each persona runs as its own Slack channel pod (it
knows which agent it is via `INSTANCE_NAME`). By default every pod posts under
the single bot identity. To make each persona's replies appear under its own
display name and icon, set these env vars on the persona's Slack pod:

| Env var | Maps to `chat.postMessage` | Example |
|---------|----------------------------|---------|
| `SLACK_DISPLAY_NAME` | `username` | `Winston (Architect)` |
| `SLACK_ICON_URL` | `icon_url` | `https://example.com/winston.png` |
| `SLACK_ICON_EMOJI` | `icon_emoji` | `:bricks:` |

`icon_url` and `icon_emoji` are mutually exclusive — a URL wins if both are set.
This requires the **`chat:write.customize`** bot scope. The fields are also
exposed on `channel.OutboundMessage` (`username` / `iconUrl` / `iconEmoji`) so a
caller can override the identity per message. When none of them are set the
`chat.postMessage` payload is identical to a plain single-identity bot post, so
existing setups are unaffected.
