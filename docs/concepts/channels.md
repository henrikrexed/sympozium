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
| **Telegram** | Create a bot with [@BotFather](https://t.me/BotFather), get the token, pass it during onboarding or set it in the SympoziumInstance channel config |
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
    - `message.groups` (private channels)
    - `app_mention`
- Add the `chat:write.customize` bot scope (in addition to `chat:write`) — this
  lets a single bot post each message under a distinct `username`/`icon`. It is
  what powers **per-agent attribution** in an Ensemble, where every persona
  shares one Slack app but should appear as its own sender. The display name is
  taken from the persona's `displayName` (falling back to the instance name).
- Add the `channels:join` bot scope — the channel pod self-heals membership by
  calling `conversations.join` on a public channel the first time a post is
  rejected with `not_in_channel` / `channel_not_found`, then retries the post
  (ISI-1411).
- Reinstall the app after changing scopes or event subscriptions

### Channel membership

Slack only delivers `message.channels` events for, and only accepts
`chat.postMessage` to, channels the bot is **a member of**. A bot that is not in
the target channel both fails to receive inbound messages and gets
`channel_not_found` on outbound replies.

- **Public channels** are joined automatically on first send (requires the
  `channels:join` scope above). You can also `/invite @your-bot` proactively so
  it starts receiving inbound events before the first reply.
- **Private channels and DMs** cannot be auto-joined; `/invite @your-bot` (or
  opening a DM) is required. The pod does not attempt a join that would fail.

!!! warning
    If `SLACK_APP_TOKEN` is omitted, Sympozium falls back to Slack Events API mode, which requires a publicly reachable webhook URL.
