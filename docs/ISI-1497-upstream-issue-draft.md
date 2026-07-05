# Upstream issue draft — ISI-1497

**✅ OPENED:** https://github.com/sympozium-ai/sympozium/issues/257 (2026-07-01).
The implementation PR (from children C1–C3) must include `Closes sympozium-ai/sympozium#257`.

Copy the block below into a new GitHub issue on `sympozium-ai/sympozium`.
(Internal ticket refs and deployment names are intentionally omitted.)

---

## Title

```
[Feature] Ensemble: designate a Slack receiver persona and delegate on @mention
```

## Description

```markdown
### Problem

When an Ensemble is bound to Slack, an inbound message is currently routed to a
single agent using a hardcoded `AgentID: "primary"`
(`internal/controller/channel_router.go`). There is no declarative way to say
*which* persona in the crew should receive Slack messages, and there is no way
for a user to address a specific teammate from within a message. The result:
you can't tell who in the ensemble actually receives a message, and you can't
target the right agent for a given request.

### Proposed feature

Two additions, both building on primitives that already exist in the CRD:

**1. Designate the Slack receiver.**
Add an optional `slackListener: bool` field to `AgentConfigSpec`
(`api/v1alpha1/ensemble_types.go`). The persona with `slackListener: true` is
the default receiver for inbound Slack messages on the ensemble's Slack channel.

- Exactly one persona per ensemble should set it.
- If none set it, behaviour is unchanged (first Slack-bound persona), so this is
  backwards compatible.

**2. Delegate on `@mention`.**
Agents already carry `Name` and `DisplayName` (`AgentConfigSpec`). When an
inbound message contains `@<name>` (matching `Name` or `DisplayName`,
case-insensitive), the designated receiver **delegates** the task to that named
persona rather than handling it itself. The receiver stays the front door;
delegation flows through the existing delegation executor
(`Relationships` / `WorkflowType: delegation`), so the guardrails (in-flight and
depth caps) and delegation telemetry apply automatically. An unrecognised name
leaves the message with the receiver plus a friendly note.

### Example

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
spec:
  workflowType: delegation
  agentConfigs:
    - name: lead
      displayName: Lead
      slackListener: true          # <- receives inbound Slack messages
      channels: ["slack"]
    - name: researcher
      displayName: Researcher
      channels: ["slack"]
  relationships:
    - from: lead
      to: researcher
      type: delegation
```

A Slack message `"@Researcher can you dig into X?"` lands on `lead`, which
delegates the task to `researcher`.

### Existing building blocks (keeps this incremental)

- `AgentConfigSpec.Name` / `.DisplayName` already exist.
- The delegation executor (`Relationships`, `WorkflowType: delegation`) already
  runs, with in-flight/depth-cap guardrails.
- Prior unmerged work parses `@persona` / `persona:` in inbound channel messages
  and resolves the sibling Agent CR — reusable for the `@mention` parse/resolve
  step.

### Acceptance criteria

- [ ] `slackListener` field added to `AgentConfigSpec`; CRDs regenerated.
- [ ] Inbound Slack messages route to the `slackListener` persona; fallback
      preserved when unset.
- [ ] `@name` (Name or DisplayName) in an inbound message delegates from the
      receiver to the named persona via the delegation executor.
- [ ] Unknown `@name` → stays on receiver, no crash, user-visible note.
- [ ] Outbound replies from a delegated agent are attributed to that agent.
- [ ] Unit tests + docs (`docs/concepts/channels.md`) + a sample Ensemble YAML.

### Out of scope

- Multiple Slack workspaces/bots per ensemble (today: one ensemble → one Slack
  deployment).
- Per-persona Slack visual identity (icon/name), tracked separately.
- Non-Slack channels (Discord already supports per-channel routing via
  `AllowedChats`).
```
