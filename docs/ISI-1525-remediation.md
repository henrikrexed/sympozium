# ISI-1525 — Remediation of ISI-1498 code-review findings

**Author:** Winston (Architect) · **Commit:** `3112728` on `isi1436-integration-test`
**Parent review:** `docs/ISI-1498-code-review.md` (Amelia, commit `1156f8f`)
**Status:** ready for re-review — build/vet/gofmt clean, controller tests PASS, CRD regenerated.

## What changed per finding

| ID | Finding | Fix |
|----|---------|-----|
| **H1** | Label/annotation used wrong domain `sympozium.io/slack-listener` (only `sympozium.io` in the Go tree) | Renamed to `sympozium.ai/slack-listener` via a shared package const `slackListenerKey` (`ensemble_controller.go`). Type doc comment updated. A test guard now fails if the legacy `sympozium.io/` label reappears. |
| **M1** | Stamped label was "dead" — commit claimed the router resolves the receiver "without reading the Ensemble", but C2 (`channel_router.go`) reads the Ensemble CR directly | Chose review option (b): reconciled the doc comment with the implementation. The router resolves via `resolveSlackReceiver(ensemble.Spec.AgentConfigs)`; the stamped label/annotation is documented as observability + `kubectl` selectability (`kubectl get agents -l sympozium.ai/slack-listener=true`), not the resolution path. |
| **M2** | AC unmet: no validation/warning for >1 designated receiver; first-in-order won silently | Added CEL `+kubebuilder:validation:XValidation` on `agentConfigs`: `self.filter(c, has(c.slackListener) && c.slackListener).size() <= 1` (rejects at admission). Defense-in-depth controller warning `Log.Info` when >1 observed (older CRDs may predate the CEL rule). |
| **M3** | CRD hand-edited, `slackListener` mis-ordered → `make manifests` would drift | Ran `make manifests`. `slackListener` now sits alphabetically between `skills` and `slackOptions`; CEL rule emitted under `x-kubernetes-validations`; `charts/sympozium/crds` and `charts/sympozium-crds/templates` synced. |
| **L1** | No test for the C1 stamping itself | Added `TestBuildAgent_SlackListenerStamp` (create path, both directions + H1 regression guard) and `TestSetSlackListenerMetadata` (stamp/unstamp/no-op/self-heal). |
| **L2** | Reconcile drift keyed on label only — annotation wouldn't self-heal | Unified create + update stamping into `setSlackListenerMetadata(labels, annotations, on)`, which drives **both** maps to the desired state and returns whether anything changed. Independent annotation drift now self-heals on the next reconcile. |

## Verification
- `gofmt -l` clean · `go vet ./internal/controller/... ./api/...` clean · `go build ./...` OK
- `go test ./internal/controller/... -count=1` → PASS (incl. new stamping tests + existing slack routing)
- `make manifests` → no residual hand-edit drift; `grep -rn sympozium.io --include='*.go'` → only the intentional regression-guard assertion remains

## Notes for re-review
- CEL is a hard admission reject (AC said "warn"); paired with a non-breaking controller warning. `slackListener` is a new field, so no existing Ensemble sets >1 — the hard rule is safe going forward.
- No Go struct fields changed (markers/comments only) → no `zz_generated.deepcopy.go` churn.
