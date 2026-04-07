# Ferret

Ferret is a Go CLI that answers factual questions about GitHub repos and projects from the command line.

Each command answers one question:

- `catch-up` — what changed since I last looked?
- `activity` — what events actually happened in this repo?
- `manager` — what is the current state of this repo or project?
- `next` — what open work is waiting for action?

It is designed to shrink the amount of GitHub data you need to hand to AI.

## Install

Recommended:

1. Download the archive for your platform from the latest GitHub release.
2. Extract it.
3. Put the `ferret` binary somewhere on your `PATH`.

Fallback:

```bash
go install github.com/adrian1-dot/ferret/cmd/ferret@latest
```

Current release artifacts are published for:
- `darwin/amd64`
- `darwin/arm64`
- `linux/amd64`
- `linux/arm64`
- `windows/amd64`

Homebrew is not a supported install path today.

## First Use

If you installed from source instead of a release binary, build it with:

```bash
go build ./cmd/ferret
```

Check requirements:

```bash
./ferret doctor
```

Initialize config:

```bash
./ferret init
```

Start repo-first — this is the fastest path to value:

```bash
./ferret watch repo OWNER/REPO --alias my-repo
./ferret catch-up my-repo --me
./ferret activity my-repo
./ferret manager my-repo
```

Watch specific issues or PRs you care about regardless of repo scope:

```bash
./ferret watch issue OWNER/REPO NUMBER --alias my-issue
./ferret watch pr OWNER/REPO NUMBER --alias my-pr
./ferret watch pr OWNER/REPO#NUMBER --alias my-pr
```

Watched items appear in a separate "Watched Items" section in `catch-up` and `activity` output only when they are not already covered by the main scoped results.

Add a GitHub Project later if you want board-aware summaries:

```bash
./ferret watch project OWNER/NUMBER --alias my-board --link-repo my-repo
./ferret inspect project my-board
./ferret next my-board
```

## Concepts

- **watched repo**: a repo Ferret tracks by alias — the primary scope for all commands
- **watched project**: a GitHub Project v2 tracked by alias — adds board items to `manager` and `next`
- **watched item**: a single issue or PR tracked by alias; it can be targeted directly in `inspect`, `activity`, and `catch-up`
- `catch-up`: changed items only, good for daily review; cursor-backed so it picks up where it left off
- `activity`: raw chronological events in the scope; cursor-backed
- `manager`: broad factual snapshot — issues, PRs, review status, workflow runs; in project mode with `status_field` configured, board counts are rendered as `board_*`, and the board item set is always a current snapshot
- `next`: open work grouped by action readiness, not a board-ground-truth report; in project mode the board item set is still a current snapshot

## Requirements

Ferret needs a GitHub token with the `repo` and `read:org` scopes. Add the `project` scope for GitHub Projects support.

Token resolution order:

1. Stored token in `~/.ferret/auth.yaml` (set by `ferret init`)
2. `GITHUB_TOKEN` environment variable
3. Token from the GitHub CLI (`gh auth token`) if `gh` is installed
4. Interactive PAT prompt

`gh` is not required. Run `ferret init` on first use — it walks you through token setup.

GitHub API rate limits apply; very large scopes or deep time windows may produce partial results.

Run `ferret doctor` to verify your local setup, active token source, and available scopes.

Pre-built releases are published through GitHub Actions and Goreleaser.

## Recommended Workflow

For most new users:

1. `ferret init`
2. `ferret doctor`
3. `ferret watch repo OWNER/REPO --alias my-repo`
4. `ferret catch-up my-repo --me`
5. `ferret activity my-repo`
6. `ferret manager my-repo`
7. Watch specific items you care about: `ferret watch issue OWNER/REPO NUMBER --alias label`
8. Add a project later if needed

Use project mode when:

- your team works from GitHub Projects v2
- board fields and linked repos matter
- you want a factual project snapshot

Use repo mode when:

- you mainly care about issues, PRs, comments, and review flow
- you want value quickly with very little config

## Config

Ferret stores config in:

```text
.ferret/config.yaml
```

State (cursors) is stored in:

```text
.ferret/state.yaml
```

Project-based `next` reports default to:

```text
.ferret/plans/<alias>.md
```

Paths passed via `--config`, `--out`, `--plan-file`, and configured plan/output paths expand `~`.

By default, Ferret only writes inside repo-local `.ferret/` roots:
- config and report outputs stay under `.ferret/`
- plan files stay under `.ferret/plans/`

Use `--allow-outside-workspace` only when you intentionally want config or output paths outside those roots.

Example catch-up defaults:

```yaml
defaults:
  catch_up:
    expand_order: balanced
    review_budget: 10
```

`defaults.catch_up.review_budget` sets the default number of PRs that receive deep
review-thread expansion. `0` keeps the built-in default budget. The CLI flag
`--review-budget` overrides the config value for a single run.

## Commands

Setup:

```bash
ferret init
ferret doctor
```

Watch management:

```bash
ferret watch repo OWNER/REPO --alias my-repo
ferret watch project OWNER/NUMBER --alias my-board --link-repo my-repo
ferret watch issue OWNER/REPO NUMBER --alias my-issue
ferret watch issue OWNER/REPO#NUMBER --alias my-issue
ferret watch pr OWNER/REPO NUMBER --alias my-pr
ferret watch pr OWNER/REPO#NUMBER --alias my-pr
ferret watch list

ferret unwatch repo my-repo
ferret unwatch project my-board
ferret unwatch issue my-issue
ferret unwatch issue OWNER/REPO#NUMBER
ferret unwatch pr my-pr
ferret unwatch pr OWNER/REPO#NUMBER
```

Inspect:

```bash
ferret inspect repo <owner>/<repo>|<alias>
ferret inspect project <owner>/<number>|<alias>
ferret inspect issue <owner>/<repo>#<number>|<alias>
ferret inspect pr <owner>/<repo>#<number>|<alias>
```

Watched item targeting:

```bash
ferret inspect issue atlas-481
ferret inspect pr txpipe/dolos#872
ferret activity dolos-pr-872 --since 90d
ferret catch-up atlas-481 --since 30d
```

Daily use:

```bash
ferret catch-up my-repo --me
ferret catch-up my-repo --expand-order recency
ferret activity my-repo
ferret manager my-repo
ferret next my-repo
```

Across all watched repos:

```bash
ferret catch-up --all --me
ferret activity --all
ferret manager --all
ferret next --all
```

Omitting an alias defaults to `--all`:

```bash
ferret catch-up --me
ferret activity
ferret manager
ferret next
```

## Time Window Behavior

`catch-up` and `activity` are cursor-backed by default.

That means:
- if you do not pass `--since`, Ferret uses the last successful run time for that command and scope
- on the first run, Ferret falls back to the last `24h`
- after a successful run, the cursor advances to `now`
- explicit `--since` overrides the cursor and does not advance it

Examples:

```bash
ferret catch-up my-repo --me
ferret activity my-repo
ferret catch-up --since 1d --me
ferret activity my-repo --since 3d
```

`manager` does not use cursors.

`next` does not use cursors and defaults to the last `30d` when no explicit or per-alias `since` value is set.

## Output Formats

All report commands support `--out` and `--format`:

```bash
ferret catch-up my-repo --out .ferret/catchup.md # writes markdown
ferret catch-up my-repo --out .ferret/catchup.json # writes JSON (from extension)
ferret manager my-repo --format json             # JSON to stdout
```

The `.json` file extension selects JSON format automatically. Any other extension
(or no extension) produces markdown. An explicit `--format` flag always wins.

## Rate Limits

Ferret makes one or more GitHub API calls per command. For `catch-up`:

- Open PR count still affects how much review-related work is in scope
- Review expansion is capped to a safe default budget; when truncated, Ferret emits an explicit warning
- That budget only affects deep review-thread expansion, not the base issue/PR facts returned by the command
- The default expansion order is `balanced`, which uses already-fetched GitHub signals such as review requests, notifications, direct involvement, and recency
- Use `--expand-order recency` or set `defaults.catch_up.expand_order: recency` to prefer a more neutral updated-time order
- Rate-limit errors from GitHub surface as partial-report warnings — the command
  does not crash; results up to the limit are still returned
- Preview recovery is also budgeted and warns when deeper recovery is skipped

GitHub's rate limit remains the backstop, but Ferret now bounds the deepest `catch-up` fetches before they fan out too far.

## Example Output

`catch-up`:

```text
Repo: my-repo
Generated: 2026-03-31 08:00:00Z
Since: 2026-03-30T08:00:00Z

Changed Items:

Needs Attention (1):
- api#142 Fix auth middleware [review_requested by @github]
  url: https://github.com/acme/api/pull/142
  at: 2026-03-30 09:02:00Z
  involvement: assignee | notifications: review_requested
```

`activity`:

```text
Repo: my-repo
Generated: 2026-03-31 08:00:00Z
Since: 2026-03-30T08:00:00Z

Recent Activity:
- #142 Fix auth middleware (pull_request)
  url: https://github.com/acme/api/pull/142
  commented by @alice
  at: 2026-03-30 09:15:00Z
```

`manager`:

```text
Repo: my-repo
Generated: 2026-03-31 08:00:00Z

Summary:
- total: 5
- issues: 3
- prs: 2
- review_needed: 1
```

## Current Status

Ferret is usable and tested. It is being actively developed.
