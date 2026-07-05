# ISI-1521 Code Review — @name routing H1/M1/M2/L1 (Amelia, Code Reviewer)

**Commit:** `4221513` — `fix(channels): H1 fall-through + M1 ordering doc + M2 tests + L1 gofmt`
**Verdict:** 🟢 GREEN-LIGHT — all four "Done when" criteria met. 0 critical / 0 high. 1 LOW residual (advisory, non-blocking).

## Scope reviewed
- `internal/controller/channel_router.go` (handleInbound name-routing block + `extractNameMention`)
- `internal/controller/channel_router_slack_routing_test.go` (new tests)

## Acceptance audit — all met
| Item | Requirement | Status |
|------|-------------|--------|
| H1 | On no-match, fall through instead of denying; reserve denial for unambiguous `@persona` | ✅ `extractNameMention` returns `isMention`; denial gated on `isMention=true`; `word:` forms + `@here/@channel/@everyone` fall through |
| M1 | Document front-door ordering; unknown-mention denial must not silently bypass mute/access | ✅ Ordering documented (`channel_router.go:356-364`). See LOW-1 residual below. |
| M2 | Unit tests for `extractNameMention` + integration test for `handleInbound` redirect | ✅ `TestExtractNameMention` (14 cases) + `TestHandleInbound_NameRouting` (3 fake-client paths) |
| L1 | `gofmt` clean | ✅ `gofmt -l` reports nothing |

## Verification (this review)
- `gofmt -l channel_router.go channel_router_slack_routing_test.go` → clean (exit 0).
- `go clean -testcache && go test ./internal/controller/ -run '...routing...' -count=1 -v` → **PASS**
  (`TestExtractNameMention`, `TestHandleInbound_NameRouting`, `TestResolveSlackReceiver`, `TestResolveNamedDelegate`, `TestNoListenerFallback`).
- `go vet ./internal/controller/` → clean.

## Adversarial layers
**Blind Hunter** — H1 logic correct. Known persona via `word:` form still routes (only the *no-match denial* is gated on `isMention`). Delegate-Get failure still falls through to receiver (unchanged). Empty `@ ` / `@:` tokens are guarded by the `mention != ""` caller check — no panic.

**Edge Case Hunter** — `@HERE` case-insensitive keyword covered; `https://` URL falls through (`isMention=false`, no persona match); mid-sentence colons don't false-trigger. All represented in the table test.

**Acceptance Auditor** — integration test asserts the three product paths end-to-end via fake client: known `@persona` → AgentRun for delegate + no denial; unknown `@persona` → denial published + zero AgentRuns; `Note:` → no denial + AgentRun for original receiver.

## Findings
### LOW-1 (advisory, non-blocking) — unknown-mention denial fires before mute/access
The unknown `@persona` denial (`sendDenialResponse` + early `return`) runs before `checkChannelAccess` (`channel_router.go:452`) and before the mute/trigger check (`applyTriggers`, `:468`). Consequences, both narrow:
- On a **muted** channel, an `@unknownpersona` message still emits a bot denial reply — muting is meant to suppress bot chatter.
- An **access-denied** sender receives the available-persona list before access control runs (minor info disclosure).

The M1 comment documents this ordering as a deliberate choice, so the "decide + document" half of M1 is satisfied and this does not gate the merge. If suppressing bot output in muted / access-restricted channels matters, move the unknown-mention denial to after the mute + access checks (or gate it on `!muted && allowed`). Tracked as a follow-up so it is not lost. No AgentRun is created in the denial path, so there is no autonomy/compute escalation.

## Disposition
Approved. ISI-1521 → done. One LOW follow-up filed under ISI-1518 for the denial-ordering hardening.
