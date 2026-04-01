# Ferret

Ferret is a Go CLI that answers factual questions about GitHub repos and projects from the command line.

Each command answers one question:

- `catch-up` — what changed since I last looked?
- `activity` — what events actually happened in this repo?
- `manager` — what is the current state of this repo or project?
- `next` — what open work is waiting for action?

It is designed to shrink the amount of GitHub data you need to hand to AI.

## First Use

Build it:

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
```

Watched items appear in a separate "Watched Items" section in `catch-up` and `activity` output.

Add a GitHub Project later if you want board-aware summaries:

```bash
./ferret watch project OWNER/NUMBER --alias my-board --link-repo my-repo
./ferret inspect project my-board
./ferret next my-board
```

## Concepts

- **watched repo**: a repo Ferret tracks by alias — the primary scope for all commands
- **watched project**: a GitHub Project v2 tracked by alias — adds board items to `manager` and `next`
- **watched item**: a single issue or PR tracked by alias — always included in `catch-up` and `activity` regardless of scope
- `catch-up`: changed items only, good for daily review; cursor-backed so it picks up where it left off
- `activity`: raw chronological events in the scope; cursor-backed
- `manager`: broad factual snapshot — issues, PRs, review status, workflow runs
- `next`: open work grouped by action readiness

## Requirements

Ferret needs a GitHub token with the `repo` and `read:org` scopes. Add the `project` scope for GitHub Projects support.

Token resolution order:

1. Stored token in `~/.ferret/auth.yaml` (set by `ferret init`)
2. `GITHUB_TOKEN` environment variable
3. Token from the GitHub CLI (`gh auth token`) if `gh` is installed
4. Interactive PAT prompt

`gh` is not required. Run `ferret init` on first use — it walks you through token setup.

GitHub API rate limits apply; very large scopes or deep time windows may produce partial results.

Run `ferret doctor` to verify your local setup.

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
ferret watch pr OWNER/REPO NUMBER --alias my-pr
ferret watch list

ferret unwatch repo my-repo
ferret unwatch project my-board
ferret unwatch issue my-issue
ferret unwatch pr my-pr
```

Inspect:

```bash
ferret inspect repo <owner>/<repo>|<alias>
ferret inspect project <owner>/<number>|<alias>
```

Daily use:

```bash
ferret catch-up my-repo --me
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

`manager` and `next` do not use cursors.

## Output Formats

All report commands support `--out` and `--format`:

```bash
ferret catch-up my-repo --out catchup.md        # writes markdown
ferret catch-up my-repo --out catchup.json       # writes JSON (from extension)
ferret manager my-repo --format json             # JSON to stdout
```

The `.json` file extension selects JSON format automatically. Any other extension
(or no extension) produces markdown. An explicit `--format` flag always wins.

## Rate Limits

Ferret makes one or more GitHub API calls per command. For `catch-up`:

- Review fetches scale with the number of open PRs in scope
- When more than 20 PRs are in scope, a warning is printed to stderr
- Rate-limit errors from GitHub surface as partial-report warnings — the command
  does not crash; results up to the limit are still returned

There is no internal request cap. GitHub's rate limit is the real backstop.

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
