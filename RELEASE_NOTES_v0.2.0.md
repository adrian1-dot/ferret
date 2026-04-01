# Ferret v0.2.0

## Summary

This release improves correctness, resilience, and day-to-day usability for GitHub repo and project workflows.

## Highlights

- Removed the hard dependency on `gh` for normal operation.
  `gh` is now optional and used as an authentication convenience when available.

- Improved repo-mode resilience when GitHub API behavior degrades.
  Ferret now preserves repo PR visibility more reliably instead of collapsing to issue-only output.

- Fixed `catch-up` integrity for mixed issue and pull-request discussion activity.
  Broad `catch-up` output no longer emits split records with missing title or URL metadata.

- Made watched items first-class command targets.
  Watched issue and PR aliases now work directly with `inspect`, `activity`, and `catch-up`.

- Improved watched-item hydration and event counting.
  Watched PRs now retain title, URL, state, and complete event aggregation even when broader repo fetches are degraded.

- Hardened config writes.
  Ferret now protects config updates against concurrent mutation loss.

- Clarified partial-result behavior under large scopes.
  Deep review expansion remains budgeted, with explicit warnings when truncation happens.

## User-Facing Improvements

- `watch issue` and `watch pr` accept both:
  - `OWNER/REPO NUMBER`
  - `OWNER/REPO#NUMBER`

- Watched items are deduplicated from shared `catch-up` and `activity` views when already covered by the main scope.

- Output path handling is stricter and safer under repo-local `.ferret/` roots by default.

- Help text and README now match shipped behavior more closely, including watched-item targeting and partial-result semantics.

## Notes

- Large repos may still produce partial `catch-up` output when deep review expansion hits safety budgets.
- Those budgets affect deep expansion order, not the base issue and pull-request facts included in output.

## Verification

Validated with:

- unit and integration tests via `go test ./...`
- live command reruns against current GitHub state for:
  - repo mode
  - project mode
  - watched-item targets
  - broad `catch-up`
