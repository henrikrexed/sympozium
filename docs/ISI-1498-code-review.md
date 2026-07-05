# ISI-1498 Code Review — Ensemble CRD `slackListener` designated-receiver flag

**Reviewer:** Amelia (Code Reviewer) · **Commit:** `1156f8f` on `isi1436-integration-test`
**Verdict:** 🟡 CHANGES REQUESTED — 1 High, 3 Medium, 2 Low. No crash/critical bugs; build/vet/tests green.

## Scope reviewed
- `api/v1alpha1/ensemble_types.go` — `AgentConfigSpec.SlackListener bool`
- `internal/controller/ensemble_controller.go` — `buildAgent` (create) + `reconcileAgentConfig` (update) label/annotation stamping
- `config/crd/bases/sympozium.ai_ensembles.yaml` — CRD field
- Cross-checked against already-landed C2 consumer `internal/controller/channel_router.go` (ISI-1499, commit `487d16c`)

## Verification
- `gofmt -l` clean · `go vet ./internal/controller/...` clean
- `go test ./internal/controller -run 'SlackRouting|SlackListener|SlackReceiver'` → PASS

## Findings

### H1 — Wrong label/annotation domain: `sympozium.io/` should be `sympozium.ai/`
`ensemble_controller.go:331,612,617` (+ doc comment `ensemble_types.go:296`) use key
`sympozium.io/slack-listener`. This is the **only** `sympozium.io` string in the entire Go
source tree — every other label/annotation and the CRD API group itself use `sympozium.ai/`
(`sympozium.ai/ensemble`, `sympozium.ai/agent-config`, `sympozium.ai/provider`,
`sympozium.ai/role`, `sympozium.ai/disable-sa-token`, …). This is a convention break and
almost certainly an unintended typo. Any operator or future code that selects by the
conventional `sympozium.ai/slack-listener` will silently miss.
**Fix:** rename to `sympozium.ai/slack-listener` in the const, both `buildAgent` sites, and the type doc comment.

### M1 — Stamped label/annotation is currently dead: C2 does not read it
The commit's stated rationale is *"so the channel router can resolve the receiver without
reading the Ensemble."* The already-landed C2 (`channel_router.go:423-441`) does the
opposite: it `Get`s the Ensemble CR and calls `resolveSlackReceiver(ensemble.Spec.AgentConfigs)`
(`:427`, `:686`) reading the CR field directly, then resolves the Agent **by name**
(`ensembleName + "-" + receiver.Name`). Nothing queries the `slack-listener` label/annotation.
So the stamped metadata is unused today — which is also why the H1 typo went unnoticed.
**Fix (pick one):** (a) wire C2 to a label selector so the rationale is real and the two
concerns decouple, or (b) drop the "without reading the Ensemble" claim and keep the
label/annotation explicitly for observability/`kubectl` selectability. Either way, reconcile
intent with implementation.

### M2 — Missing the AC's validation for >1 designated receiver
Issue AC: *"Kubebuilder validation should warn if >1 persona per ensemble sets it."* There is
no `+kubebuilder:validation:XValidation` (CEL) on the `agentConfigs` list and no controller
warning log. `resolveSlackReceiver` (`channel_router.go:686`) silently returns the **first**
persona with `SlackListener==true` in declaration order — the exact misconfiguration the AC
wants surfaced is accepted silently.
**Fix:** add a CEL rule on the `agentConfigs` list (`self.filter(c, c.slackListener).size() <= 1`)
or, if a hard reject is too strict, emit `Log.Info`/an Event when the controller observes >1.

### M3 — CRD not regenerated via controller-gen (hand-edited)
In `sympozium.ai_ensembles.yaml`, `slackListener` sits at line 538 between `runTimeout` and
`schedule`. controller-gen emits properties **alphabetically**; the correct slot is between
`skills` (577) and `slackOptions` (583). The out-of-order placement means the file was edited
by hand, not produced by `make manifests`. A CI drift check (`make manifests && git diff
--exit-code`) will fail, and future regen will churn this line.
**Fix:** run `make manifests` and commit the generated output.

### L1 — No test for the C1 stamping itself
C2 resolution is covered (`channel_router_slack_routing_test.go`) but nothing asserts
`buildAgent`/`reconcileAgentConfig` actually stamp (and unstamp) the label/annotation.
**Fix:** add a table test over both the create and the flag-flip update paths.

### L2 — Reconcile drift keyed on label only
`reconcileAgentConfig:332` computes `haveSlackListener` from the **label** only. If the
annotation drifts independently (present/absent) while the label is correct, the annotation is
never corrected. Minor because both are written together, but a divergent-state reconcile
won't self-heal the annotation.

## Disposition
No failing tests, no critical/crash finding — but H1 (wrong domain) and M2/M3 (unmet AC:
validation + regenerated CRD) are real gaps against the issue's acceptance, so this is not a
green-light. Remediation is small and owned by the Developer.
