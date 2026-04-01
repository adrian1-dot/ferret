# v0.2.0 Implementation Plan

Date: 2026-04-01

## Status

Completed just before this plan:
- fixed broad `catch-up` entry integrity so PR discussion activity no longer creates blank-title or blank-URL split records
- confirmed watched-item alias targeting works in live validation
- confirmed Dolos repo mode is currently healthy in live validation

This plan covers the remaining work before we discuss whether to ship `v0.2.0`.

## 1. Research The Right Review-Fetch Budget

Goal:
- choose the `catch-up` review-expansion budget from live evidence rather than by guesswork

Questions to answer:
- how many PRs are typically in scope on the current validation repos
- how often the current budget of `20` truncates useful review expansion
- which signals best predict relevance under budget pressure
- whether the budget should stay fixed or become adaptive

Method:
- query current live GitHub state with `gh`
- use the existing validation targets as the baseline dataset
- compare PR volume and recency on at least:
  - `txpipe/dolos`
  - `Andamio-Platform/andamio-api`
  - one smaller repo from the validation set for contrast

Deliverable:
- a short research note with:
  - recommended default budget
  - whether to keep a fixed budget or use simple adaptive rules
  - recommended ranking signals

## 2. Implement Prioritization And Validate Against Live `gh` Ground Truth

Goal:
- when the review budget is hit, expand the most relevant PRs first

Implementation work:
- rank PRs before deep review expansion
- prioritize using signals such as:
  - notification overlap
  - requested reviewer overlap
  - assignee overlap
  - viewer authorship/involvement
  - watched-item overlap
  - recent updates
- keep explicit truncation warnings

Validation requirement:
- validate against live GitHub state using `gh`, not only against previous Ferret output
- when live data differs from the April 1, 2026 validation report, record that as data drift rather than as an app regression

Required reruns:
- `manager andamio-p21 --since 30d`
- `next andamio-p21 --me`
- `manager dolos --since 30d`
- `activity dolos --since 30d`
- `activity dolos-pr-872 --since 90d`
- `catch-up --since 90d`
- `catch-up atlas-481 --since 30d`

Required `gh` comparisons:
- project item counts and status-field ground truth for the Andamio board target
- current PR listing for Dolos repo mode
- current issue/PR/review/comment counts for watched-item targets
- exact thread-level checks where needed

Success criteria:
- the prioritization logic is implemented and tested
- live Ferret output remains correct against current `gh` ground truth
- no new correctness regressions appear in repo mode or watched-item mode

## 3. Docs And Help Pass

Goal:
- ensure shipped behavior and written behavior match

Work:
- update README examples and semantics
- update CLI help text where needed
- verify docs reflect:
  - watched item aliases
  - deduped watched-item behavior in shared views
  - budget warnings and partial results
  - item-targeted `activity` and `catch-up`
  - any final budget semantics that ship from Step 2

Success criteria:
- docs and help match actual command behavior
- stale wording from earlier semantics is removed

## 4. Stop And Review

Do not tag or release automatically after Step 3.

At that point we review:
- research findings
- implementation diff
- live validation results against current `gh` state
- docs/help changes

Only then decide together whether to cut `v0.2.0`.
