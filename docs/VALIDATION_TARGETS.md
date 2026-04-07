# Validation Targets

Updated: 2026-04-07

This file defines the public targets Ferret should use for future validation loops.
The goal is not "largest repos possible". The goal is a balanced matrix:

- at least one clean high-volume repo
- at least one clean medium-volume repo
- several deliberate stress targets that trigger partial or rate-limited behavior
- at least one public project target
- at least one public project target that can legitimately return sparse or empty sections

## Recommended Default Matrix

Use these on most `v0.4.x` validation passes.

### Clean Repo Targets

- `cli/cli`
  - Why: already proven useful for repo-mode correctness and broad public activity.
  - Current role: baseline repo target.
  - Good commands:
    - `ferret manager cli --since 30d`
    - `ferret catch-up cli --since 30d --review-budget 5`

- `txpipe/dolos`
  - Why: strong PR-heavy catch-up target with discussion and notification behavior.
  - Current role: baseline catch-up target.
  - Good commands:
    - `ferret manager dolos --since 30d`
    - `ferret catch-up dolos --since 90d --review-budget 5`

- `microsoft/vscode`
  - Why: very large public repo that still completed cleanly in this pass.
  - Current observed result on 2026-04-07:
    - `manager --since 30d`: `partial: false`
    - summary: `issues: 12038`, `prs: 4348`, `workflow_runs: 10`
  - Current role: clean high-volume stress target.
  - Good commands:
    - `ferret manager vscode --since 30d`

- `streamlit/streamlit`
  - Why: active but not pathological; good medium-volume public target.
  - Current observed result on 2026-04-07:
    - `manager --since 30d`: `partial: false`
    - summary: `issues: 169`, `prs: 440`, `workflow_runs: 10`
  - Current role: clean medium-volume target.
  - Good commands:
    - `ferret manager streamlit --since 30d`

### Public Project Targets

- `github/12731` (`GitHub OSPO OSS Board`)
  - Why: already validated and useful for public project-mode checks.
  - Current role: primary public project target.
  - Good commands:
    - `ferret manager github-ospo --since 30d`
    - `ferret next github-ospo --since 30d`

- `github/4247` (`GitHub Public Roadmap`)
  - Why: clearly public and useful as a sparse-project target.
  - Current observed result on 2026-04-07:
    - `next --since 30d`: empty `sections`
  - Current role: project-mode empty/sparse-result target.
  - Good commands:
    - `ferret next github-roadmap --since 30d`

## Extended Stress Matrix

Use these periodically, not on every loop. They are useful precisely because they tend to trigger degradation.

- `grafana/grafana`
  - Current observed result on 2026-04-07:
    - `manager --since 30d`: `partial: true`
    - PR fetch hit GitHub secondary rate limit
  - Current role: deliberate secondary-rate-limit repo target.

- `kubernetes/kubernetes`
  - Current observed result on 2026-04-07:
    - `manager --since 30d`: `partial: true`
    - PR fetch hit GitHub secondary rate limit
  - Current role: deliberate secondary-rate-limit repo target.

- `NixOS/nixpkgs`
  - Current observed result on 2026-04-07:
    - `manager --since 30d`: `partial: true`
    - PR fetch hit GitHub secondary rate limit
  - Current role: very-large-repo rate-limit target.

- `zed-industries/zed`
  - Current observed result on 2026-04-07:
    - `manager --since 30d`: `partial: true`
    - PR fetch hit GitHub secondary rate limit
  - Current role: active product repo stress target.

- `openai/codex`
  - Current observed result on 2026-04-07:
    - `manager --since 30d`: `partial: true`
    - PR fetch hit GitHub secondary rate limit
  - Current role: active toolchain repo stress target.

## Targets To Avoid As Default Baselines

These are not bad targets. They are just not the right default choice for ordinary regression loops.

- any repo that reliably rate-limits on a simple `manager --since 30d`
  - Reason: useful for resilience checks, noisy for baseline correctness checks.

- generic search-discovered repos with suspiciously low stars or low issue counts despite matching broad topic filters
  - Reason: search quality is noisy and easy to poison.

## Suggested Future Validation Tiers

### Tier 1: Every Significant Loop

- `cli/cli`
- `txpipe/dolos`
- `microsoft/vscode`
- `streamlit/streamlit`
- `github/12731`
- `github/4247`

### Tier 2: Before Cutting A Release Candidate

- Tier 1 plus:
- `grafana/grafana`
- `kubernetes/kubernetes`
- `NixOS/nixpkgs`

### Tier 3: Resilience-Focused Work Only

- `zed-industries/zed`
- `openai/codex`
- any repo currently provoking secondary-rate-limit or truncation-path behavior

## Suggested Commands

Minimal repo matrix:

```bash
ferret manager cli --since 30d
ferret manager dolos --since 30d
ferret manager vscode --since 30d
ferret manager streamlit --since 30d
ferret catch-up dolos --since 90d --review-budget 5
ferret catch-up cli --since 30d --review-budget 5
```

Project matrix:

```bash
ferret manager github-ospo --since 30d
ferret next github-ospo --since 30d
ferret next github-roadmap --since 30d
```

Resilience matrix:

```bash
ferret manager grafana --since 30d
ferret manager k8s --since 30d
ferret manager nixpkgs --since 30d
```

Diagnostics checks:

```bash
ferret catch-up dolos --since 90d --review-budget 5 --format json
ferret catch-up dolos --since 90d --review-budget 5 --diagnostics --format json
ferret catch-up cli --since 30d --review-budget 5 --format text
ferret catch-up cli --since 30d --review-budget 5 --diagnostics --format text
```

## Notes

- Re-run this list occasionally. Public repos drift.
- Keep baseline and stress targets separate.
- Prefer targets that tell us something different:
  - clean high-volume
  - clean medium-volume
  - sparse project
  - active public project
  - rate-limit stress
