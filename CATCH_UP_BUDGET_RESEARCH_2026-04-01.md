# Catch-Up Budget Research

Date: 2026-04-01

## Goal

Choose the `catch-up` PR review-expansion strategy for `v0.2.0` using current live GitHub data rather than intuition.

## Current Default

From the code today:
- `MaxPRReviewFetches: 20`
- `MaxRecoveryFetches: 10`

## Live `gh` Findings

Query date:
- 2026-04-01

Query basis:
- live `gh api` queries against the validation repos
- PR volume measured with `updated:>=2026-01-01`

Results:
- `txpipe/dolos`: 164 PRs in scope
- `Andamio-Platform/andamio-api`: 133 PRs in scope
- `Andamio-Platform/andamio-dev-kit-internal`: 6 PRs in scope

Observations:
- large repos exceed the current review budget by a wide margin
- small repos already fit comfortably under the budget
- raising the default budget materially would multiply per-PR review thread fetches on the largest targets
- recent PR listings show that only a minority of current top PRs have explicit reviewer-request or other strong direct-involvement signals

## Recommendation For `v0.2.0`

Do not increase the default review budget yet.

Ship `v0.2.0` with:
- the default PR review-expansion budget still at `20`
- better prioritization ahead of budget truncation
- explicit warnings when truncation occurs

## Reasoning

Why keep `20` for now:
- `20` is already enough for smaller repos
- on large repos, the bigger problem is fetch ordering, not only raw budget size
- moving from `20` to a much larger fixed number would increase GitHub API cost quickly on repos like Dolos and Andamio API
- the release goal is trustworthy truncation, not full expansion of every large repo

Why prioritize first:
- direct user relevance should beat pure recency
- the highest-signal PRs are usually the ones with:
  - review-request notifications
  - mention/team-mention notifications
  - assignee overlap
  - reviewer overlap
  - authorship overlap
  - recent updates as a tie-breaker

## Proposed Shipping Rule

For `v0.2.0`:
- keep a fixed review budget of `20`
- prioritize before truncating
- defer adaptive budgeting until there is more live usage evidence

## Follow-Up Question After `v0.2.0`

If future validation shows that the top `20` still misses too many relevant PRs even after prioritization, evaluate:
- a small adaptive tier by repo size
- or a user-configurable budget

That should be a later change, not part of this release unless live validation still shows poor result quality after prioritization.
