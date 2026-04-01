package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/adrian1-dot/ferret/internal/auth"
	"github.com/adrian1-dot/ferret/internal/config"
	"github.com/adrian1-dot/ferret/internal/domain"
	"github.com/adrian1-dot/ferret/internal/fsutil"
	"github.com/adrian1-dot/ferret/internal/github"
	"github.com/adrian1-dot/ferret/internal/render"

	"github.com/spf13/cobra"
)

type rootOptions struct {
	configPath string
	format     string
}

type app struct {
	store    config.Store
	state    config.StateStore
	backend  github.Backend
	renderer render.Renderer
}

type doctorReport struct {
	TokenSource  string
	Authenticated bool
	Login        string
	RepoScope    bool
	ProjectScope bool
	Warning      string
}

func NewRootCmd() *cobra.Command {
	opts := &rootOptions{}
	cmd := &cobra.Command{
		Use:   "ferret",
		Short: "Answer factual questions about GitHub repos and projects from the command line",
		Long: `Ferret turns GitHub data into focused CLI reports — no dashboards, no noise.

Each command answers one factual question:

  catch-up   What changed since I last looked?
  activity   What events actually happened in this repo?
  manager    What is the current state of this repo or project?
  next       What open work is waiting for action?

Quick start (repo-first, no project needed):

  ferret init
  ferret watch repo OWNER/REPO
  ferret catch-up REPO --me
  ferret activity REPO
  ferret manager REPO

Add a GitHub Project later for board-aware summaries:

  ferret watch project OWNER/NUMBER --alias my-board --link-repo my-repo
  ferret inspect project my-board
  ferret next my-board

Run ferret doctor to verify your local setup.`,
	}
	cmd.PersistentFlags().StringVar(&opts.configPath, "config", config.DefaultConfigPath, "config path")
	cmd.PersistentFlags().StringVar(&opts.format, "format", "text", "output format: text|json|markdown")

	cmd.AddCommand(newAuthCmd(opts))
	cmd.AddCommand(newInspectCmd(opts))
	cmd.AddCommand(newDoctorCmd(opts))
	cmd.AddCommand(newInitCmd(opts))
	cmd.AddCommand(newWatchCmd(opts))
	cmd.AddCommand(newUnwatchCmd(opts))
	cmd.AddCommand(newManagerCmd(opts))
	cmd.AddCommand(newNextCmd(opts))
	cmd.AddCommand(newCatchUpCmd(opts))
	cmd.AddCommand(newActivityCmd(opts))
	cmd.AddCommand(newCompletionCmd())
	return cmd
}

func newDoctorCmd(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check GitHub authentication and required token scopes",
		RunE: func(cmd *cobra.Command, args []string) error {
			res := auth.Check(cmd.Context())
			report, err := runDoctor(cmd.Context(), res)
			printDoctorReport(cmd.OutOrStdout(), report)
			return err
		},
	}
	return cmd
}

func newInitCmd(opts *rootOptions) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Authenticate with GitHub, create a starter config, and print first-run steps",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			app, err := newApp(opts, cmd)
			if err != nil {
				return err
			}
			login, err := app.backend.GetViewer(ctx)
			if err != nil {
				return fmt.Errorf("GitHub connection failed: %w", err)
			}
			path := opts.configPath
			if path == "" {
				path = config.DefaultConfigPath
			}
			if !force {
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("config already exists at %s; use --force to overwrite", path)
				}
			}
			cfg := config.Default()
			if err := app.store.Save(ctx, cfg); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Authenticated as @%s\n", login)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Initialized Ferret config at %s\n\n", path)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Recommended first steps:")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "1. Watch a repo (run without args to see recently active repos)")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "   ferret watch repo OWNER/REPO")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "2. See what changed")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "   ferret catch-up my-repo --since 7d --me")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "3. Get a broad summary")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "   ferret manager my-repo --since 7d")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "4. Add a project later for board-aware summaries")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "   ferret watch project OWNER/NUMBER --alias my-board --link-repo my-repo")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "5. Inspect the board structure")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "   ferret inspect project my-board")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	return cmd
}

func newAuthCmd(_ *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage GitHub authentication",
	}

	loginCmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with GitHub (re-runs the token setup flow)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := auth.Logout(); err != nil {
				return err
			}
			res, err := auth.Resolve(cmd.Context(), cmd.OutOrStdout(), cmd.InOrStdin())
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Authenticated (source: %s)\n", res.Source)
			return nil
		},
	}

	logoutCmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored GitHub token from ~/.ferret/auth.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := auth.Logout(); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Logged out. Token removed from ~/.ferret/auth.yaml")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Run any ferret command to authenticate again.")
			return nil
		},
	}

	cmd.AddCommand(loginCmd, logoutCmd)
	return cmd
}

func newInspectCmd(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Show the raw structure of a watched repo or GitHub Project",
	}
	projectCmd := &cobra.Command{
		Use:   "project <owner>/<number>|<alias>",
		Short: "Show the fields and options of a GitHub Project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			app, err := newApp(opts, cmd)
			if err != nil {
				return err
			}
			cfg, err := app.store.Load(ctx)
			if err != nil {
				return err
			}
			owner, number, alias, err := resolveProjectRef(cfg, args[0])
			if err != nil {
				return err
			}
			schema, err := app.backend.GetProjectSchema(ctx, owner, number)
			if err != nil {
				return err
			}
			_ = alias
			return app.renderer.RenderProjectInspect(cmd.OutOrStdout(), schema)
		},
	}

	repoCmd := &cobra.Command{
		Use:   "repo <owner>/<repo>|<alias>",
		Short: "Show metadata about a watched or unwatched GitHub repo",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			app, err := newApp(opts, cmd)
			if err != nil {
				return err
			}
			cfg, err := app.store.Load(ctx)
			if err != nil {
				return err
			}
			owner, repo, err := resolveRepoRef(cfg, args[0])
			if err != nil {
				return err
			}
			snapshot, err := app.backend.GetRepo(ctx, owner, repo)
			if err != nil {
				return err
			}
			return app.renderer.RenderRepoInspect(cmd.OutOrStdout(), snapshot)
		},
	}
	cmd.AddCommand(projectCmd, repoCmd)
	return cmd
}

func newWatchCmd(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Add a repo, project, issue, or PR to the Ferret watch list",
	}
	var alias string
	var linkedRepos []string
	var planFile string
	var statusField string
	projectCmd := &cobra.Command{
		Use:   "project <owner>/<number>",
		Short: "Watch a GitHub Project by owner and project number",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if alias == "" {
				return fmt.Errorf("--alias is required")
			}
			owner, number, err := splitProjectRef(args[0])
			if err != nil {
				return err
			}
			app, err := newApp(opts, cmd)
			if err != nil {
				return err
			}
			cfg, err := app.store.Load(ctx)
			if err != nil {
				return err
			}
			if _, err := app.backend.GetProjectSchema(ctx, owner, number); err != nil {
				return err
			}
			if planFile == "" {
				planFile = filepath.Join(cfg.Defaults.PlanDir, alias+".md")
			}
			if err := config.AddProjectWatch(cfg, config.ProjectWatch{
				Alias: alias, Owner: owner, Number: number, LinkedRepos: linkedRepos,
				StatusField: statusField, Output: config.ProjectOutput{PlanFile: planFile},
			}); err != nil {
				return err
			}
			if err := app.store.Save(ctx, cfg); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Watching project %s/%d as %q\n", owner, number, alias)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Plan file: %s\n", planFile)
			if statusField != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Status field: %s\n", statusField)
			}
			return nil
		},
	}
	projectCmd.Flags().StringVar(&alias, "alias", "", "local alias")
	projectCmd.Flags().StringSliceVar(&linkedRepos, "link-repo", nil, "linked repo aliases")
	projectCmd.Flags().StringVar(&planFile, "plan-file", "", "plan markdown path")
	projectCmd.Flags().StringVar(&statusField, "status-field", "", "project field name to use as board status (e.g. \"Status\")")

	var repoAlias string
	repoCmd := &cobra.Command{
		Use:   "repo [owner/repo]",
		Args:  cobra.MaximumNArgs(1),
		Short: "Watch a GitHub repo",
		Long:  "Watch a GitHub repo. If --alias is omitted the repo name is used. Run without arguments to see recently active repos.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			app, err := newApp(opts, cmd)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				suggestions, err := app.backend.ListUserRecentRepos(ctx, 10)
				if err != nil {
					return fmt.Errorf("fetching recent repos: %w", err)
				}
				if len(suggestions) == 0 {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No recently active repos found.")
					return nil
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Recently active repos (pick one to watch):")
				for _, r := range suggestions {
					visibility := "public"
					if r.IsPrivate {
						visibility = "private"
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s/%s  (%s, pushed %s)\n", r.Owner, r.Name, visibility, r.UpdatedAt.Format("2006-01-02"))
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\nRun: ferret watch repo OWNER/REPO")
				return nil
			}
			owner, repo, err := splitRepoRef(args[0])
			if err != nil {
				return err
			}
			effectiveAlias := repoAlias
			if effectiveAlias == "" {
				effectiveAlias = repo
			}
			cfg, err := app.store.Load(ctx)
			if err != nil {
				return err
			}
			if err := config.AddRepoWatch(cfg, config.RepoWatch{Alias: effectiveAlias, Owner: owner, Name: repo}); err != nil {
				return err
			}
			if err := app.store.Save(ctx, cfg); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Watching repo %s/%s as %q\n", owner, repo, effectiveAlias)
			return nil
		},
	}
	repoCmd.Flags().StringVar(&repoAlias, "alias", "", "local alias (defaults to repo name)")
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List watched repos and projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(opts, cmd)
			if err != nil {
				return err
			}
			cfg, err := app.store.Load(cmd.Context())
			if err != nil {
				return err
			}
			return renderWatchList(cmd.OutOrStdout(), cfg)
		},
	}
	issueCmd := newWatchItemCmd(opts, "issue")
	prCmd := newWatchItemCmd(opts, "pr")
	cmd.AddCommand(projectCmd, repoCmd, listCmd, issueCmd, prCmd)
	return cmd
}

// newWatchItemCmd returns a cobra.Command for "watch issue" or "watch pr".
func newWatchItemCmd(opts *rootOptions, kind string) *cobra.Command {
	var itemAlias string
	cmd := &cobra.Command{
		Use:   kind + " OWNER/REPO NUMBER [--alias name]",
		Short: fmt.Sprintf("Watch a specific %s by OWNER/REPO and NUMBER", kind),
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			owner, repo, err := splitRepoRef(args[0])
			if err != nil {
				return err
			}
			number, err := strconv.Atoi(args[1])
			if err != nil || number <= 0 {
				return fmt.Errorf("NUMBER must be a positive integer, got %q", args[1])
			}
			if itemAlias == "" {
				itemAlias = fmt.Sprintf("%s-%s-%d", owner, repo, number)
			}
			app, err := newApp(opts, cmd)
			if err != nil {
				return err
			}
			cfg, err := app.store.Load(ctx)
			if err != nil {
				return err
			}
			if err := config.AddItemWatch(cfg, config.ItemWatch{
				Alias: itemAlias, Owner: owner, Repo: repo, Number: number, Kind: kind,
			}); err != nil {
				return err
			}
			if err := app.store.Save(ctx, cfg); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Watching %s %s/%s#%d as %q\n", kind, owner, repo, number, itemAlias)
			return nil
		},
	}
	cmd.Flags().StringVar(&itemAlias, "alias", "", "local alias (auto-generated from OWNER/REPO/NUMBER if omitted)")
	return cmd
}

// newUnwatchItemRunE returns a RunE for "unwatch issue|pr" that accepts
// either a single alias or two args: OWNER/REPO NUMBER.
func newUnwatchItemRunE(opts *rootOptions, kind string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		app, err := newApp(opts, cmd)
		if err != nil {
			return err
		}
		cfg, err := app.store.Load(ctx)
		if err != nil {
			return err
		}
		if len(args) == 1 {
			// Single arg: treat as alias.
			if err := config.RemoveItemWatch(cfg, args[0]); err != nil {
				return err
			}
		} else {
			// Two args: OWNER/REPO NUMBER.
			owner, repo, err := splitRepoRef(args[0])
			if err != nil {
				return err
			}
			number, err := strconv.Atoi(args[1])
			if err != nil || number <= 0 {
				return fmt.Errorf("NUMBER must be a positive integer, got %q", args[1])
			}
			if err := config.RemoveItemWatchByOwnerRepoNumber(cfg, owner, repo, number, kind); err != nil {
				return err
			}
		}
		return app.store.Save(ctx, cfg)
	}
}

func newUnwatchCmd(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unwatch",
		Short: "Remove a repo, project, issue, or PR from the Ferret watch list",
	}
	projectCmd := &cobra.Command{
		Use:   "project <alias> [alias...]",
		Short: "Stop watching one or more GitHub Projects",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(opts, cmd)
			if err != nil {
				return err
			}
			cfg, err := app.store.Load(cmd.Context())
			if err != nil {
				return err
			}
			for _, alias := range args {
				if err := config.RemoveProjectWatch(cfg, alias); err != nil {
					return err
				}
			}
			return app.store.Save(cmd.Context(), cfg)
		},
	}
	repoCmd := &cobra.Command{
		Use:   "repo <alias> [alias...]",
		Short: "Stop watching one or more repos",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(opts, cmd)
			if err != nil {
				return err
			}
			cfg, err := app.store.Load(cmd.Context())
			if err != nil {
				return err
			}
			for _, alias := range args {
				if err := config.RemoveRepoWatch(cfg, alias); err != nil {
					return err
				}
			}
			return app.store.Save(cmd.Context(), cfg)
		},
	}
	unwatchIssueCmd := &cobra.Command{
		Use:   "issue <alias> | OWNER/REPO NUMBER",
		Short: "Stop watching a specific issue",
		Args:  cobra.RangeArgs(1, 2),
		RunE:  newUnwatchItemRunE(opts, "issue"),
	}
	unwatchPRCmd := &cobra.Command{
		Use:   "pr <alias> | OWNER/REPO NUMBER",
		Short: "Stop watching a specific pull request",
		Args:  cobra.RangeArgs(1, 2),
		RunE:  newUnwatchItemRunE(opts, "pr"),
	}
	cmd.AddCommand(projectCmd, repoCmd, unwatchIssueCmd, unwatchPRCmd)
	return cmd
}

func newManagerCmd(opts *rootOptions) *cobra.Command {
	var since string
	var filter string
	var out string
	var all bool
	cmd := &cobra.Command{
		Use:   "manager [alias]",
		Short: "Show the current state of a repo or project: open issues, PRs, review status, and workflow runs",
		Long: `Show a factual snapshot of a watched repo or GitHub Project.

Answers: what is the current state of this scope right now?

The output includes open issues, open PRs (with review status), any PRs
needing review, and recent workflow runs. When scoped to a project, project
board items are included.

Scoping:

  manager               runs across all watched repos
  manager <alias>       scopes to one repo or project alias
  manager --all         explicit all-repos mode

Output:

  --out results.md      writes markdown to a file
  --out results.json    writes JSON (extension determines format)
  --format json         override format explicitly

Examples:

  ferret manager my-repo
  ferret manager my-board --since 7d
  ferret manager --all --out summary.md`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if out != "" && !cmd.Flag("format").Changed {
				switch strings.ToLower(filepath.Ext(out)) {
				case ".json":
					opts.format = "json"
				default:
					opts.format = "markdown"
				}
			}
			out = resolveOutputPath(out, opts.format)
			app, err := newApp(opts, cmd)
			if err != nil {
				return err
			}
			cfg, err := app.store.Load(ctx)
			if err != nil {
				return err
			}
			viewer, viewerErr := app.backend.GetViewer(ctx)
			if viewerErr != nil {
				viewer = ""
			}
			ref := ""
			if len(args) > 0 {
				ref = args[0]
			}
			if all && ref != "" {
				return fmt.Errorf("use either an alias or --all")
			}
			if ref == "" {
				all = true
			}
			effectiveSince, err := resolveSince(firstNonEmpty(since, defaultSinceForAlias(cfg, ref)))
			if err != nil {
				return err
			}
			if all {
				report, err := buildAllReposManagerReport(ctx, app, cfg, effectiveSince, cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				if viewerErr != nil {
					report.Partial = true
					report.Warnings = append(report.Warnings, "viewer lookup failed; assigned-to-me filtering may be incomplete")
				}
				return renderToOutput(out, cmd.OutOrStdout(), func(w io.Writer) error {
					return app.renderer.RenderManager(w, report)
				})
			}
			if project, err := config.ResolveProject(cfg, ref); err == nil {
				report := domain.ManagerReport{
					GeneratedAt: time.Now().UTC(),
					Target:      project.Alias,
					TargetKind:  "project",
					Since:       effectiveSince,
				}
				items, err := app.backend.ListProjectItems(ctx, project.Owner, project.Number, github.ProjectItemsQuery{Limit: 100, Since: effectiveSince})
				if err != nil {
					return err
				}
				boardLookup := buildBoardStatusLookup(project, items)
				for i := range items {
					items[i] = normalizeProjectItem(items[i], viewer)
					if project.StatusField != "" {
						items[i].BoardStatus = items[i].FieldValues[project.StatusField]
					}
				}
				items = filterItems(items, filter)
				report.Items = items
				report.Summary = summarizeItems(items)
				for _, alias := range project.LinkedRepos {
					repo, err := config.ResolveRepo(cfg, alias)
					if err != nil {
						report.Partial = true
						report.Warnings = append(report.Warnings, err.Error())
						continue
					}
					progressf(cmd.ErrOrStderr(), "fetching repo %s/%s", repo.Owner, repo.Name)
					appendRepoData(ctx, app, repo.Owner, repo.Name, effectiveSince, &report, cmd.ErrOrStderr())
				}
				if len(boardLookup) > 0 {
					applyBoardStatusToIssues(report.Issues, boardLookup)
					applyBoardStatusToPRs(report.PRs, boardLookup)
					applyBoardStatusToPRs(report.ReviewNeeded, boardLookup)
				}
				if viewerErr != nil {
					report.Partial = true
					report.Warnings = append(report.Warnings, "viewer lookup failed; assigned-to-me filtering may be incomplete")
				}
				return renderToOutput(out, cmd.OutOrStdout(), func(w io.Writer) error {
					return app.renderer.RenderManager(w, report)
				})
			}
			repo, err := config.ResolveRepo(cfg, ref)
			if err != nil {
				return err
			}
			report := buildRepoManagerReport(ctx, app, repo.Owner, repo.Name, repo.Alias, effectiveSince, cmd.ErrOrStderr())
			if viewerErr != nil {
				report.Partial = true
				report.Warnings = append(report.Warnings, "viewer lookup failed; assigned-to-me filtering may be incomplete")
			}
			return renderToOutput(out, cmd.OutOrStdout(), func(w io.Writer) error {
				return app.renderer.RenderManager(w, report)
			})
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "lookback duration or timestamp (e.g. 24h, 7d, 2w, 2026-01-15); no cursor — always required to limit scope")
	cmd.Flags().StringVar(&filter, "filter", "", "filter preset")
	cmd.Flags().StringVar(&out, "out", "", "write output to file (default format: markdown)")
	cmd.Flags().BoolVar(&all, "all", false, "run across all watched repos")
	return cmd
}

func newNextCmd(opts *rootOptions) *cobra.Command {
	var since string
	var filter string
	var out string
	var all bool
	cmd := &cobra.Command{
		Use:   "next [alias]",
		Short: "Show open work grouped for action",
		Long: `Show open work items grouped for action across one or more repos and projects.

When an alias is provided the command scopes output to that single repo or
project — no --all flag is required. Omitting an alias (or passing --all)
runs across every watched repo.

Filter presets:

  open             All items that are not closed or done
  assigned-to-me   Only items assigned to the current viewer
  actionable       Items that are not blocked, done, or in an unknown state
  recently-closed  Only items that are closed or merged

Examples:

  ferret next my-repo
  ferret next my-board --filter assigned-to-me
  ferret next --all --filter actionable
  ferret next my-repo --out next.md`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			app, err := newApp(opts, cmd)
			if err != nil {
				return err
			}
			cfg, err := app.store.Load(ctx)
			if err != nil {
				return err
			}
			viewer, viewerErr := app.backend.GetViewer(ctx)
			if viewerErr != nil {
				viewer = ""
			}
			ref := ""
			if len(args) > 0 {
				ref = args[0]
			}
			if all && ref != "" {
				return fmt.Errorf("use either an alias or --all")
			}
			if ref == "" {
				all = true
			}
			effectiveSince, err := resolveSince(firstNonEmpty(since, defaultSinceForAlias(cfg, ref)))
			if err != nil {
				return err
			}
			if all {
				report, err := buildAllReposNextReport(ctx, app, cfg, effectiveSince, filter, viewer, cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				if viewerErr != nil {
					report.Warnings = append(report.Warnings, "viewer lookup failed; assigned-to-me grouping may be incomplete")
				}
				if err := syncNextFile(out, report); err != nil {
					return err
				}
				return app.renderer.RenderNext(cmd.OutOrStdout(), report)
			}
			if project, err := config.ResolveProject(cfg, ref); err == nil {
				items, err := app.backend.ListProjectItems(ctx, project.Owner, project.Number, github.ProjectItemsQuery{Limit: 100, Since: effectiveSince})
				if err != nil {
					return err
				}
				for i := range items {
					items[i] = normalizeProjectItem(items[i], viewer)
				}
				report := buildProjectNextReport(project.Alias, items, effectiveSince, firstNonEmpty(filter, "actionable"))
				report.TargetKind = "project"
				if viewerErr != nil {
					report.Warnings = append(report.Warnings, "viewer lookup failed; assigned-to-me grouping may be incomplete")
				}
				if out == "" {
					out = project.Output.PlanFile
				}
				if err := syncNextFile(out, report); err != nil {
					return err
				}
				return app.renderer.RenderNext(cmd.OutOrStdout(), report)
			}
			repo, err := config.ResolveRepo(cfg, ref)
			if err != nil {
				return err
			}
			report := buildRepoNextReport(ctx, app, repo.Owner, repo.Name, repo.Alias, effectiveSince, filter, viewer)
			if viewerErr != nil {
				report.Warnings = append(report.Warnings, "viewer lookup failed; assigned-to-me grouping may be incomplete")
			}
			if out == "" {
				out = filepath.Join(cfg.Defaults.PlanDir, repo.Alias+".md")
			}
			if err := syncNextFile(out, report); err != nil {
				return err
			}
			return app.renderer.RenderNext(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "lookback duration or timestamp (e.g. 24h, 7d, 2w, 2026-01-15); no cursor — always required to limit scope")
	cmd.Flags().StringVar(&filter, "filter", "", "filter preset: open, assigned-to-me, actionable, recently-closed")
	cmd.Flags().StringVar(&out, "out", "", "markdown output path")
	cmd.Flags().BoolVar(&all, "all", false, "run across all watched repos")
	return cmd
}

func newCatchUpCmd(opts *rootOptions) *cobra.Command {
	var since string
	var me bool
	var closedOnly bool
	var openOnly bool
	var commentsOnly bool
	var reviewOnly bool
	var out string
	var all bool
	cmd := &cobra.Command{
		Use:   "catch-up [alias]",
		Short: "Show what changed in watched repos since the last run (or since a given time)",
		Long: `Show items that changed since you last ran catch-up.

Answers: what changed since I last looked?

On the first run Ferret looks back 24h. After a successful run the cursor
advances to now, so the next run starts exactly where the last one ended.
Pass --since to override the cursor for a one-off look.

Scoping:

  catch-up               runs across all watched repos
  catch-up <alias>       scopes to one repo or project alias
  catch-up --all         explicit all-repos mode

Filtering:

  --me                 only show items you are involved in
  --closed-only        only show recently merged or closed items
  --comments-only      only show items with comments or reviews

Output:

  --out results.md      writes markdown to a file
  --out results.json    writes JSON (extension determines format)
  --format json         override format explicitly

Note: review fetches scale with the number of open PRs. When more than 20
PRs are in scope, rate-limit warnings may appear in the output. GitHub's
API rate limit is the real backstop — there is no internal cap.

Examples:

  ferret catch-up my-repo --me
  ferret catch-up --since 7d
  ferret catch-up --all --me --out catchup.md`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if out != "" && !cmd.Flag("format").Changed {
				switch strings.ToLower(filepath.Ext(out)) {
				case ".json":
					opts.format = "json"
				default:
					opts.format = "markdown"
				}
			}
			out = resolveOutputPath(out, opts.format)
			app, err := newApp(opts, cmd)
			if err != nil {
				return err
			}
			cfg, err := app.store.Load(ctx)
			if err != nil {
				return err
			}
			viewer, viewerErr := app.backend.GetViewer(ctx)
			if viewerErr != nil {
				viewer = ""
			}
			ref := ""
			if len(args) > 0 {
				ref = args[0]
			}
			if all && ref != "" {
				return fmt.Errorf("use either an alias or --all")
			}
			if ref == "" {
				all = true
			}
			effectiveSince, usedCursor, err := resolveStatefulSince(ctx, app.state, "catch-up", defaultCursorScope(all, ref), since, defaultSinceForAlias(cfg, ref), "24h")
			if err != nil {
				return err
			}
			var report domain.CatchUpReport
			if all {
				report, err = buildAllReposCatchUpReport(ctx, app, cfg, effectiveSince, viewer, me, closedOnly, openOnly, commentsOnly, reviewOnly, cmd.ErrOrStderr())
				if err != nil {
					return err
				}
			} else if project, err := config.ResolveProject(cfg, ref); err == nil {
				report = domain.CatchUpReport{
					GeneratedAt: time.Now().UTC(),
					Target:      project.Alias,
					TargetKind:  "project",
					Since:       effectiveSince,
				}
				var projectBoardLookup map[string]string
				if project.StatusField != "" {
					progressf(cmd.ErrOrStderr(), "fetching project items for board status")
					projectItems, err := app.backend.ListProjectItems(ctx, project.Owner, project.Number, github.ProjectItemsQuery{Limit: 200})
					if err != nil {
						report.Warnings = append(report.Warnings, fmt.Sprintf("board status lookup: %v", err))
					} else {
						projectBoardLookup = buildBoardStatusLookup(project, projectItems)
					}
				}
				for _, alias := range project.LinkedRepos {
					repo, err := config.ResolveRepo(cfg, alias)
					if err != nil {
						report.Partial = true
						report.Warnings = append(report.Warnings, err.Error())
						continue
					}
					progressf(cmd.ErrOrStderr(), "fetching repo %s/%s", repo.Owner, repo.Name)
					entries, warnings := collectCatchUpForRepo(ctx, app, repo.Owner, repo.Name, effectiveSince, viewer, me, closedOnly, openOnly, commentsOnly, reviewOnly, cmd.ErrOrStderr())
					report.Entries = append(report.Entries, entries...)
					if len(warnings) > 0 {
						report.Partial = true
						report.Warnings = append(report.Warnings, warnings...)
					}
				}
				if len(projectBoardLookup) > 0 {
					applyBoardStatusToCatchUp(report.Entries, projectBoardLookup)
				}
			} else {
				repo, err := config.ResolveRepo(cfg, ref)
				if err != nil {
					return err
				}
				report = domain.CatchUpReport{
					GeneratedAt: time.Now().UTC(),
					Target:      repo.Alias,
					TargetKind:  "repo",
					Since:       effectiveSince,
				}
				entries, warnings := collectCatchUpForRepo(ctx, app, repo.Owner, repo.Name, effectiveSince, viewer, me, closedOnly, openOnly, commentsOnly, reviewOnly, cmd.ErrOrStderr())
				report.Entries = entries
				if len(warnings) > 0 {
					report.Partial = true
					report.Warnings = warnings
				}
			}
			if viewerErr != nil && me {
				report.Partial = true
				report.Warnings = append(report.Warnings, "viewer lookup failed; --me filtering may be incomplete")
			}
			// Collect watched items separately — always included regardless of scope.
			if len(cfg.Watch.Items) > 0 {
				progressf(cmd.ErrOrStderr(), "fetching watched items")
				watchedEntries, watchedWarnings := collectWatchedItems(ctx, app, cfg, effectiveSince, cmd.ErrOrStderr())
				report.WatchedItems = watchedEntries
				if len(watchedWarnings) > 0 {
					report.Partial = true
					report.Warnings = append(report.Warnings, watchedWarnings...)
				}
			}
			sortCatchUpEntries(report.Entries)
			if err := renderToOutput(out, cmd.OutOrStdout(), func(w io.Writer) error {
				return app.renderer.RenderCatchUp(w, report)
			}); err != nil {
				return err
			}
			if usedCursor {
				return advanceCursor(ctx, app.state, "catch-up", defaultCursorScope(all, ref))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "lookback duration or timestamp (e.g. 24h, 7d, 2w, 2026-01-15); overrides the per-alias cursor for a one-off look without advancing it")
	cmd.Flags().BoolVar(&me, "me", false, "show items involving the current viewer")
	cmd.Flags().BoolVar(&closedOnly, "closed-only", false, "show only recently closed or merged items")
	cmd.Flags().BoolVar(&openOnly, "open-only", false, "show only open (non-terminal) items")
	cmd.Flags().BoolVar(&commentsOnly, "comments-only", false, "show only comment and review activity")
	cmd.Flags().BoolVar(&reviewOnly, "review-only", false, "show only items where a review was requested")
	cmd.Flags().StringVar(&out, "out", "", "write output to file (default format: markdown)")
	cmd.Flags().BoolVar(&all, "all", false, "run across all watched repos")
	return cmd
}

func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion <shell>",
		Short: "Generate shell completion script",
		Long: `Generate a shell completion script for ferret and load it into your shell.

Supported shells: bash, zsh, fish, powershell

Quick setup (one-liner per shell):

  bash:       source <(ferret completion bash)
  zsh:        source <(ferret completion zsh)
  fish:       ferret completion fish | source
  powershell: ferret completion powershell | Out-String | Invoke-Expression

To make it permanent, add the source line to your shell profile
(~/.bashrc, ~/.zshrc, ~/.config/fish/config.fish, etc.).`,
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		Args:      cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(cmd.OutOrStdout())
			case "zsh":
				return root.GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(cmd.OutOrStdout(), true)
			case "powershell":
				return root.GenPowerShellCompletion(cmd.OutOrStdout())
			default:
				return fmt.Errorf("unknown shell %q — use bash, zsh, fish, or powershell", args[0])
			}
		},
	}
	return cmd
}

func newActivityCmd(opts *rootOptions) *cobra.Command {
	var since string
	var out string
	var all bool
	cmd := &cobra.Command{
		Use:   "activity [alias]",
		Short: "Show the actual events that happened in a watched repo: comments, reviews, opens, and closes",
		Long: `Show a chronological list of events that occurred in a watched repo.

Answers: what events actually happened in this repo?

Events include issue opens and closes, PR opens, merges and closes, issue
comments, PR review comments, and PR reviews. Events are ordered by time,
most recent first, and grouped by repo when multiple repos are in scope.

Unlike catch-up, activity is not filtered by your involvement — it shows
everything. Use catch-up --me to focus on items relevant to you.

Scoping:

  activity               runs across all watched repos
  activity <alias>       scopes to one repo or project alias
  activity --all         explicit all-repos mode

Output:

  --out results.md      writes markdown to a file
  --out results.json    writes JSON (extension determines format)
  --format json         override format explicitly

The cursor advances after each successful run (same behaviour as catch-up).
Pass --since to override the cursor without advancing it.

Examples:

  ferret activity my-repo
  ferret activity --since 3d
  ferret activity my-repo --out activity.md`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if out != "" && !cmd.Flag("format").Changed {
				switch strings.ToLower(filepath.Ext(out)) {
				case ".json":
					opts.format = "json"
				default:
					opts.format = "markdown"
				}
			}
			out = resolveOutputPath(out, opts.format)
			app, err := newApp(opts, cmd)
			if err != nil {
				return err
			}
			cfg, err := app.store.Load(ctx)
			if err != nil {
				return err
			}
			ref := ""
			if len(args) > 0 {
				ref = args[0]
			}
			if all && ref != "" {
				return fmt.Errorf("use either an alias or --all")
			}
			if ref == "" {
				all = true
			}
			effectiveSince, usedCursor, err := resolveStatefulSince(ctx, app.state, "activity", defaultCursorScope(all, ref), since, defaultSinceForAlias(cfg, ref), "24h")
			if err != nil {
				return err
			}
			var report domain.ActivityReport
			if all {
				report, err = buildAllReposActivityReport(ctx, app, cfg, effectiveSince, cmd.ErrOrStderr())
				if err != nil {
					return err
				}
			} else if project, err := config.ResolveProject(cfg, ref); err == nil {
				report = domain.ActivityReport{
					GeneratedAt: time.Now().UTC(),
					Target:      project.Alias,
					TargetKind:  "project",
					Since:       effectiveSince,
				}
				if len(project.LinkedRepos) == 0 {
					report.Partial = true
					report.Warnings = append(report.Warnings, "project has no linked repos; activity is repo-driven")
				}
				var projectBoardLookupAct map[string]string
				if project.StatusField != "" {
					progressf(cmd.ErrOrStderr(), "fetching project items for board status")
					projectItems, err := app.backend.ListProjectItems(ctx, project.Owner, project.Number, github.ProjectItemsQuery{Limit: 200})
					if err != nil {
						report.Warnings = append(report.Warnings, fmt.Sprintf("board status lookup: %v", err))
					} else {
						projectBoardLookupAct = buildBoardStatusLookup(project, projectItems)
					}
				}
				for _, alias := range project.LinkedRepos {
					repo, err := config.ResolveRepo(cfg, alias)
					if err != nil {
						report.Partial = true
						report.Warnings = append(report.Warnings, err.Error())
						continue
					}
					progressf(cmd.ErrOrStderr(), "fetching repo %s/%s", repo.Owner, repo.Name)
					entries, warnings := collectActivityForRepo(ctx, app, repo.Owner, repo.Name, effectiveSince, cmd.ErrOrStderr())
					report.Entries = append(report.Entries, entries...)
					if len(warnings) > 0 {
						report.Partial = true
						report.Warnings = append(report.Warnings, warnings...)
					}
				}
				if len(projectBoardLookupAct) > 0 {
					applyBoardStatusToActivity(report.Entries, projectBoardLookupAct)
				}
			} else {
				repo, err := config.ResolveRepo(cfg, ref)
				if err != nil {
					return err
				}
				report = domain.ActivityReport{
					GeneratedAt: time.Now().UTC(),
					Target:      repo.Alias,
					TargetKind:  "repo",
					Since:       effectiveSince,
				}
				entries, warnings := collectActivityForRepo(ctx, app, repo.Owner, repo.Name, effectiveSince, cmd.ErrOrStderr())
				report.Entries = entries
				if len(warnings) > 0 {
					report.Partial = true
					report.Warnings = warnings
				}
			}
			// Collect watched items separately — always included regardless of scope.
			if len(cfg.Watch.Items) > 0 {
				progressf(cmd.ErrOrStderr(), "fetching watched items")
				watchedEntries, watchedWarnings := collectWatchedItems(ctx, app, cfg, effectiveSince, cmd.ErrOrStderr())
				report.WatchedItems = watchedEntries
				if len(watchedWarnings) > 0 {
					report.Partial = true
					report.Warnings = append(report.Warnings, watchedWarnings...)
				}
			}
			sortActivityEntries(report.Entries)
			if err := renderToOutput(out, cmd.OutOrStdout(), func(w io.Writer) error {
				return app.renderer.RenderActivity(w, report)
			}); err != nil {
				return err
			}
			if usedCursor {
				return advanceCursor(ctx, app.state, "activity", defaultCursorScope(all, ref))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "lookback duration or timestamp (e.g. 24h, 7d, 2w, 2026-01-15); overrides the per-alias cursor for a one-off look without advancing it")
	cmd.Flags().StringVar(&out, "out", "", "write output to file (default format: markdown)")
	cmd.Flags().BoolVar(&all, "all", false, "run across all watched repos")
	return cmd
}

func newApp(opts *rootOptions, cmd *cobra.Command) (app, error) {
	r, err := render.Factory{}.ForFormat(opts.format)
	if err != nil {
		return app{}, err
	}
	res, err := auth.Resolve(cmd.Context(), cmd.ErrOrStderr(), cmd.InOrStdin())
	if err != nil {
		return app{}, fmt.Errorf("authentication: %w", err)
	}
	return app{
		store:    config.FileStore{Path: opts.configPath},
		state:    config.FileStateStore{Path: config.StatePathForConfig(opts.configPath)},
		backend:  github.NewHTTPBackend(res.Token),
		renderer: r,
	}, nil
}

func resolveProjectRef(cfg *config.Config, arg string) (owner string, number int, alias string, err error) {
	if strings.Contains(arg, "/") && !strings.Contains(arg, "github.com") {
		owner, number, err = splitProjectRef(arg)
		return owner, number, "", err
	}
	project, err := config.ResolveProject(cfg, arg)
	if err != nil {
		return "", 0, "", err
	}
	return project.Owner, project.Number, project.Alias, nil
}

func resolveRepoRef(cfg *config.Config, arg string) (owner string, repo string, err error) {
	if strings.Count(arg, "/") == 1 {
		return splitRepoRef(arg)
	}
	w, err := config.ResolveRepo(cfg, arg)
	if err != nil {
		return "", "", err
	}
	return w.Owner, w.Name, nil
}

func splitProjectRef(ref string) (string, int, error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("project ref must be owner/number")
	}
	number, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, fmt.Errorf("invalid project number %q", parts[1])
	}
	return parts[0], number, nil
}

func splitRepoRef(ref string) (string, string, error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("repo ref must be owner/repo")
	}
	return parts[0], parts[1], nil
}

func renderWatchList(w io.Writer, cfg *config.Config) error {
	fmt.Fprintln(w, "Watched Repos:")
	if len(cfg.Watch.Repos) == 0 {
		fmt.Fprintln(w, "- none")
	} else {
		for _, repo := range cfg.Watch.Repos {
			fmt.Fprintf(w, "- %s -> %s/%s\n", repo.Alias, repo.Owner, repo.Name)
		}
	}
	fmt.Fprintln(w, "\nWatched Projects:")
	if len(cfg.Watch.Projects) == 0 {
		fmt.Fprintln(w, "- none")
	} else {
		for _, project := range cfg.Watch.Projects {
			line := fmt.Sprintf("- %s -> %s/%d", project.Alias, project.Owner, project.Number)
			if len(project.LinkedRepos) > 0 {
				line += " | linked repos: " + strings.Join(project.LinkedRepos, ", ")
			}
			if project.StatusField != "" {
				line += " | status field: " + project.StatusField
			}
			fmt.Fprintln(w, line)
		}
	}
	fmt.Fprintln(w, "\nWatched Items:")
	if len(cfg.Watch.Items) == 0 {
		fmt.Fprintln(w, "- none")
	} else {
		for _, item := range cfg.Watch.Items {
			fmt.Fprintf(w, "- %s -> %s/%s#%d (%s)\n", item.Alias, item.Owner, item.Repo, item.Number, item.Kind)
		}
	}
	return nil
}

func summarizeItems(items []domain.ProjectItem) map[string]int {
	out := map[string]int{"total": len(items)}
	for _, item := range items {
		status := string(item.Status)
		if status == "" || status == string(domain.StatusUnknown) {
			status = "unknown"
		}
		out[status]++
	}
	return out
}

func normalizeProjectItem(item domain.ProjectItem, viewer string) domain.ProjectItem {
	out := item
	out.AssignedToMe = slices.Contains(item.Assignees, viewer)
	out.Status = genericStatus(item)
	out.Priority = genericPriority(item)
	out.Blocked = out.Status == domain.StatusBlocked
	return out
}

// buildBoardStatusLookup returns a map of "owner/repo#number" → board status string.
// When project.StatusField is empty the map is empty (no enrichment).
func buildBoardStatusLookup(project *config.ProjectWatch, items []domain.ProjectItem) map[string]string {
	lookup := map[string]string{}
	if project == nil || project.StatusField == "" {
		return lookup
	}
	for _, item := range items {
		if item.Owner == "" || item.Repo == "" || item.Number == 0 {
			continue
		}
		if v, ok := item.FieldValues[project.StatusField]; ok && v != "" {
			key := fmt.Sprintf("%s/%s#%d", item.Owner, item.Repo, item.Number)
			lookup[key] = v
		}
	}
	return lookup
}

// applyBoardStatusToIssues enriches issues with board status from the lookup map.
func applyBoardStatusToIssues(issues []domain.IssueSnapshot, lookup map[string]string) {
	for i, issue := range issues {
		key := fmt.Sprintf("%s/%s#%d", issue.Owner, issue.Repo, issue.Number)
		if v, ok := lookup[key]; ok {
			issues[i].BoardStatus = v
		}
	}
}

// applyBoardStatusToPRs enriches PRs with board status from the lookup map.
func applyBoardStatusToPRs(prs []domain.PRSnapshot, lookup map[string]string) {
	for i, pr := range prs {
		key := fmt.Sprintf("%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
		if v, ok := lookup[key]; ok {
			prs[i].BoardStatus = v
		}
	}
}

// applyBoardStatusToCatchUp enriches catch-up entries with board status from the lookup map.
func applyBoardStatusToCatchUp(entries []domain.CatchUpEntry, lookup map[string]string) {
	for i, entry := range entries {
		key := fmt.Sprintf("%s/%s#%d", entry.Owner, entry.Repo, entry.Number)
		if v, ok := lookup[key]; ok {
			entries[i].BoardStatus = v
		}
	}
}

// applyBoardStatusToActivity enriches activity entries with board status from the lookup map.
func applyBoardStatusToActivity(entries []domain.ActivityEntry, lookup map[string]string) {
	for i, entry := range entries {
		key := fmt.Sprintf("%s/%s#%d", entry.Owner, entry.Repo, entry.Number)
		if v, ok := lookup[key]; ok {
			entries[i].BoardStatus = v
		}
	}
}

func genericStatus(item domain.ProjectItem) domain.StatusTier {
	for _, value := range item.FieldValues {
		l := strings.ToLower(value)
		switch {
		case strings.Contains(l, "done") || strings.Contains(l, "closed"):
			return domain.StatusDone
		case strings.Contains(l, "review"):
			return domain.StatusInReview
		case strings.Contains(l, "progress"):
			return domain.StatusInProgress
		case strings.Contains(l, "ready") || strings.Contains(l, "todo"):
			return domain.StatusReady
		case strings.Contains(l, "block"):
			return domain.StatusBlocked
		case strings.Contains(l, "backlog"):
			return domain.StatusBacklog
		}
	}
	switch strings.ToUpper(item.State) {
	case "CLOSED", "MERGED":
		return domain.StatusDone
	default:
		return domain.StatusUnknown
	}
}

func genericPriority(item domain.ProjectItem) domain.PriorityTier {
	for _, value := range item.FieldValues {
		l := strings.ToLower(value)
		switch {
		case strings.Contains(l, "critical") || strings.Contains(l, "must"):
			return domain.PriorityCritical
		case strings.Contains(l, "high"):
			return domain.PriorityHigh
		case strings.Contains(l, "medium"):
			return domain.PriorityMedium
		case strings.Contains(l, "low") || strings.Contains(l, "nice"):
			return domain.PriorityLow
		}
	}
	return domain.PriorityNone
}

func filterItems(items []domain.ProjectItem, filter string) []domain.ProjectItem {
	if filter == "" {
		return items
	}
	var out []domain.ProjectItem
	for _, item := range items {
		switch filter {
		case "open":
			if strings.EqualFold(item.State, "closed") || strings.EqualFold(item.State, "merged") || item.Status == domain.StatusDone {
				continue
			}
		case "recently-closed":
			if !strings.EqualFold(item.State, "closed") && !strings.EqualFold(item.State, "merged") && item.Status != domain.StatusDone {
				continue
			}
		case "assigned-to-me":
			if !item.AssignedToMe {
				continue
			}
		case "unassigned":
			if len(item.Assignees) != 0 {
				continue
			}
		case "blocked":
			if !item.Blocked {
				continue
			}
		case "review-needed":
			if item.Type != "pull_request" || item.Status != domain.StatusInReview {
				continue
			}
		case "actionable":
			if item.Blocked || item.Status == domain.StatusBlocked || item.Status == domain.StatusDone || item.Status == domain.StatusUnknown {
				continue
			}
		}
		out = append(out, item)
	}
	return out
}

func buildNextSections(items []domain.ProjectItem) []domain.NextSection {
	var assignedToMe, review, open []domain.ProjectItem
	for _, item := range items {
		switch {
		case item.Status == domain.StatusDone || strings.EqualFold(item.State, "closed") || strings.EqualFold(item.State, "merged"):
			// Closed/done items are excluded from the next view.
			continue
		case item.Status == domain.StatusInReview:
			if item.AssignedToMe {
				review = append(review, item)
			}
			// Reviews not assigned to me are not actionable.
			continue
		case item.AssignedToMe:
			assignedToMe = append(assignedToMe, item)
		case len(item.Assignees) > 0:
			// Assigned work owned by someone else is not actionable in the default next view.
			continue
		default:
			// Blocked items appear in open with their status shown as metadata.
			open = append(open, item)
		}
	}
	sortProjectItems(assignedToMe)
	sortProjectItems(review)
	sortProjectItems(open)
	var sections []domain.NextSection
	if len(assignedToMe) > 0 {
		sections = append(sections, domain.NextSection{Name: "Assigned to Me", Items: assignedToMe})
	}
	if len(review) > 0 {
		sections = append(sections, domain.NextSection{Name: "Review Needed", Items: review})
	}
	if len(open) > 0 {
		sections = append(sections, domain.NextSection{Name: "Open", Items: open})
	}
	return sections
}

func sortProjectItems(items []domain.ProjectItem) {
	slices.SortFunc(items, func(a, b domain.ProjectItem) int {
		if a.Priority != b.Priority {
			return cmpPriority(a.Priority, b.Priority)
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			if a.UpdatedAt.After(b.UpdatedAt) {
				return -1
			}
			return 1
		}
		return a.Number - b.Number
	})
}

func cmpPriority(a, b domain.PriorityTier) int {
	order := []domain.PriorityTier{
		domain.PriorityCritical,
		domain.PriorityHigh,
		domain.PriorityMedium,
		domain.PriorityLow,
		domain.PriorityNone,
	}
	return slices.Index(order, a) - slices.Index(order, b)
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// runDoctor checks GitHub connectivity and reports auth status.
// It does not prompt — use auth.Check to get whatever is currently available.
func runDoctor(ctx context.Context, res auth.Result) (doctorReport, error) {
	report := doctorReport{TokenSource: string(res.Source)}
	if res.Token == "" {
		report.Warning = "no GitHub token found; run any ferret command to authenticate"
		return report, fmt.Errorf("no GitHub token configured")
	}
	login, scopes, err := checkGitHubToken(ctx, res.Token)
	if err != nil {
		report.Warning = err.Error()
		return report, err
	}
	report.Authenticated = true
	report.Login = login
	report.RepoScope = contains(scopes, "repo")
	report.ProjectScope = contains(scopes, "project")
	if !report.ProjectScope {
		report.Warning = "project scope not available; add it at https://github.com/settings/tokens"
	}
	return report, nil
}

// checkGitHubToken calls GET /user with the token and returns the login and
// the token scopes from the X-OAuth-Scopes response header.
func checkGitHubToken(ctx context.Context, token string) (login string, scopes []string, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ferret")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("GitHub connection failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return "", nil, fmt.Errorf("token is invalid or expired; run 'ferret auth login' to re-authenticate")
	}
	if resp.StatusCode != 200 {
		return "", nil, fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
	}
	var out struct {
		Login string `json:"login"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	for _, s := range strings.Split(resp.Header.Get("X-OAuth-Scopes"), ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			scopes = append(scopes, s)
		}
	}
	return out.Login, scopes, nil
}

func printDoctorReport(w io.Writer, report doctorReport) {
	if report.TokenSource != "" {
		_, _ = fmt.Fprintf(w, "token source: %s\n", report.TokenSource)
	} else {
		_, _ = fmt.Fprintln(w, "token source: none")
	}
	if report.Authenticated {
		_, _ = fmt.Fprintln(w, "authenticated: yes")
		_, _ = fmt.Fprintf(w, "account: %s\n", report.Login)
		_, _ = fmt.Fprintf(w, "repo scope: %s\n", yesNo(report.RepoScope))
		_, _ = fmt.Fprintf(w, "project scope: %s\n", yesNo(report.ProjectScope))
	} else {
		_, _ = fmt.Fprintln(w, "authenticated: no")
	}
	if report.Warning != "" {
		_, _ = fmt.Fprintf(w, "hint: %s\n", report.Warning)
	}
}

func progressf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	_, _ = fmt.Fprintf(w, format+"\n", args...)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func resolveSince(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	// Expand shorthand units (d=days, w=weeks) before handing to ParseDuration.
	if d, err := time.ParseDuration(expandDuration(raw)); err == nil {
		return time.Now().UTC().Add(-d).Format(time.RFC3339), nil
	}
	layouts := []string{time.RFC3339, "2006-01-02"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", fmt.Errorf("invalid --since value %q; examples: 24h, 7d, 2w, 2026-01-15", raw)
}

// expandDuration converts shorthand duration units not understood by
// time.ParseDuration: d (days) and w (weeks) are converted to hours.
func expandDuration(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}
	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	var n int
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil || n <= 0 {
		return s
	}
	switch unit {
	case 'd', 'D':
		return fmt.Sprintf("%dh", n*24)
	case 'w', 'W':
		return fmt.Sprintf("%dh", n*24*7)
	}
	return s
}

func resolveStatefulSince(ctx context.Context, store config.StateStore, command, scope, explicit, configDefault, bootstrap string) (string, bool, error) {
	if explicit != "" {
		since, err := resolveSince(explicit)
		return since, false, err
	}
	if configDefault != "" {
		since, err := resolveSince(configDefault)
		return since, false, err
	}
	state, err := store.Load(ctx)
	if err != nil {
		return "", false, err
	}
	if since := state.Cursors[cursorKey(command, scope)]; since != "" {
		return since, true, nil
	}
	since, err := resolveSince(bootstrap)
	return since, true, err
}

func advanceCursor(ctx context.Context, store config.StateStore, command, scope string) error {
	state, err := store.Load(ctx)
	if err != nil {
		return err
	}
	if state.Cursors == nil {
		state.Cursors = map[string]string{}
	}
	state.Cursors[cursorKey(command, scope)] = time.Now().UTC().Format(time.RFC3339)
	return store.Save(ctx, state)
}

func cursorKey(command, scope string) string {
	return command + ":" + scope
}

func defaultCursorScope(all bool, ref string) string {
	if all || ref == "" {
		return "all"
	}
	return ref
}

func defaultSinceForAlias(cfg *config.Config, alias string) string {
	if project, err := config.ResolveProject(cfg, alias); err == nil {
		return project.Defaults.Since
	}
	if repo, err := config.ResolveRepo(cfg, alias); err == nil {
		return repo.Defaults.Since
	}
	return ""
}

func appendRepoData(ctx context.Context, app app, owner, repo, since string, report *domain.ManagerReport, progress io.Writer) {
	progressf(progress, "  issues %s/%s", owner, repo)
	issues, err := app.backend.ListRepoIssues(ctx, owner, repo, github.IssueQuery{State: "all", Since: since})
	if err != nil {
		report.Partial = true
		report.Warnings = append(report.Warnings, fmt.Sprintf("issues for %s/%s: %v", owner, repo, err))
	} else {
		report.Issues = append(report.Issues, issues...)
	}
	progressf(progress, "  prs %s/%s", owner, repo)
	prs, err := app.backend.ListRepoPRs(ctx, owner, repo, github.PRQuery{State: "all", Since: since})
	if err != nil {
		report.Partial = true
		report.Warnings = append(report.Warnings, fmt.Sprintf("prs for %s/%s: %v", owner, repo, err))
	} else {
		report.PRs = append(report.PRs, prs...)
	}
	progressf(progress, "  review-needed prs %s/%s", owner, repo)
	reviewNeeded, err := app.backend.ListRepoPRs(ctx, owner, repo, github.PRQuery{State: "open", Since: since, Review: true})
	if err != nil {
		report.Partial = true
		report.Warnings = append(report.Warnings, fmt.Sprintf("review-needed prs for %s/%s: %v", owner, repo, err))
	} else {
		report.ReviewNeeded = append(report.ReviewNeeded, reviewNeeded...)
	}
	progressf(progress, "  workflows %s/%s", owner, repo)
	runs, err := app.backend.ListWorkflowRuns(ctx, owner, repo, github.WorkflowQuery{PerPage: 10, Since: since})
	if err != nil {
		report.Partial = true
		report.Warnings = append(report.Warnings, fmt.Sprintf("workflow runs for %s/%s: %v", owner, repo, err))
	} else {
		report.Workflows = append(report.Workflows, runs...)
	}
}

func buildRepoManagerReport(ctx context.Context, app app, owner, repo, alias, since string, progress io.Writer) domain.ManagerReport {
	report := domain.ManagerReport{
		GeneratedAt: time.Now().UTC(),
		Target:      alias,
		TargetKind:  "repo",
		Since:       since,
		Summary:     map[string]int{},
	}
	appendRepoData(ctx, app, owner, repo, since, &report, progress)
	report.Summary["issues"] = len(report.Issues)
	report.Summary["prs"] = len(report.PRs)
	report.Summary["review_needed"] = len(report.ReviewNeeded)
	report.Summary["workflow_runs"] = len(report.Workflows)
	report.Summary["total"] = len(report.Issues) + len(report.PRs)
	return report
}

func buildAllReposManagerReport(ctx context.Context, app app, cfg *config.Config, since string, progress io.Writer) (domain.ManagerReport, error) {
	repos, err := allWatchedRepos(cfg)
	if err != nil {
		return domain.ManagerReport{}, err
	}
	report := domain.ManagerReport{
		GeneratedAt: time.Now().UTC(),
		Target:      "all-watched-repos",
		TargetKind:  "portfolio",
		Since:       since,
		Summary:     map[string]int{},
	}
	for _, repo := range repos {
		progressf(progress, "fetching repo %s/%s", repo.Owner, repo.Name)
		appendRepoData(ctx, app, repo.Owner, repo.Name, since, &report, progress)
	}
	report.Summary["issues"] = len(report.Issues)
	report.Summary["prs"] = len(report.PRs)
	report.Summary["review_needed"] = len(report.ReviewNeeded)
	report.Summary["workflow_runs"] = len(report.Workflows)
	report.Summary["total"] = len(report.Issues) + len(report.PRs)
	return report, nil
}

func buildRepoNextReport(ctx context.Context, app app, owner, repo, alias, since, filter, viewer string) domain.NextReport {
	report := domain.NextReport{
		GeneratedAt: time.Now().UTC(),
		Target:      alias,
		TargetKind:  "repo",
		Since:       since,
	}
	issues, err := app.backend.ListRepoIssues(ctx, owner, repo, github.IssueQuery{State: "all", Since: since})
	if err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("issues for %s/%s: %v", owner, repo, err))
	}
	prs, err := app.backend.ListRepoPRs(ctx, owner, repo, github.PRQuery{State: "all", Since: since})
	if err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("prs for %s/%s: %v", owner, repo, err))
	}
	items := repoSnapshotsToItems(issues, prs, viewer)
	items = filterItems(items, firstNonEmpty(filter, "open"))
	report.Sections = buildNextSections(items)
	return report
}

func buildAllReposNextReport(ctx context.Context, app app, cfg *config.Config, since, filter, viewer string, progress io.Writer) (domain.NextReport, error) {
	repos, err := allWatchedRepos(cfg)
	if err != nil {
		return domain.NextReport{}, err
	}
	report := domain.NextReport{
		GeneratedAt: time.Now().UTC(),
		Target:      "all-watched-repos",
		TargetKind:  "portfolio",
		Since:       since,
	}
	var items []domain.ProjectItem
	for _, repo := range repos {
		progressf(progress, "fetching repo %s/%s", repo.Owner, repo.Name)
		issues, ierr := app.backend.ListRepoIssues(ctx, repo.Owner, repo.Name, github.IssueQuery{State: "all", Since: since})
		if ierr != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("issues for %s/%s: %v", repo.Owner, repo.Name, ierr))
		}
		prs, perr := app.backend.ListRepoPRs(ctx, repo.Owner, repo.Name, github.PRQuery{State: "all", Since: since})
		if perr != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("prs for %s/%s: %v", repo.Owner, repo.Name, perr))
		}
		items = append(items, repoSnapshotsToItems(issues, prs, viewer)...)
	}
	items = filterItems(items, firstNonEmpty(filter, "open"))
	report.Sections = buildNextSections(items)
	return report, nil
}

func buildProjectNextReport(alias string, items []domain.ProjectItem, since, filter string) domain.NextReport {
	items = filterItems(items, firstNonEmpty(filter, "open"))
	return domain.NextReport{
		GeneratedAt: time.Now().UTC(),
		Target:      alias,
		TargetKind:  "project",
		Since:       since,
		Sections:    buildNextSections(items),
	}
}

func repoSnapshotsToItems(issues []domain.IssueSnapshot, prs []domain.PRSnapshot, viewer string) []domain.ProjectItem {
	var items []domain.ProjectItem
	for _, issue := range issues {
		item := domain.ProjectItem{
			Type:         "issue",
			Title:        issue.Title,
			URL:          issue.URL,
			Repo:         issue.Repo,
			Owner:        issue.Owner,
			Number:       issue.Number,
			State:        issue.State,
			Assignees:    issue.Assignees,
			UpdatedAt:    issue.UpdatedAt,
			AssignedToMe: contains(issue.Assignees, viewer),
			FieldValues:  map[string]string{},
		}
		if strings.EqualFold(issue.State, "closed") {
			item.Status = domain.StatusDone
		} else {
			item.Status = domain.StatusUnknown
		}
		items = append(items, item)
	}
	for _, pr := range prs {
		item := domain.ProjectItem{
			Type:         "pull_request",
			Title:        pr.Title,
			URL:          pr.URL,
			Repo:         pr.Repo,
			Owner:        pr.Owner,
			Number:       pr.Number,
			State:        pr.State,
			Assignees:    pr.Assignees,
			UpdatedAt:    pr.UpdatedAt,
			AssignedToMe: contains(pr.Assignees, viewer),
			FieldValues:  map[string]string{},
		}
		switch {
		case pr.MergedAt != nil || strings.EqualFold(pr.State, "merged"):
			item.Status = domain.StatusDone
		case strings.EqualFold(pr.State, "closed"):
			item.Status = domain.StatusDone
		case strings.EqualFold(pr.ReviewDecision, "CHANGES_REQUESTED"), strings.EqualFold(pr.ReviewDecision, "REVIEW_REQUIRED"), strings.EqualFold(pr.ReviewDecision, "COMMENTED"):
			item.Status = domain.StatusInReview
		default:
			item.Status = domain.StatusUnknown
		}
		items = append(items, item)
	}
	return items
}

func syncNextFile(path string, report domain.NextReport) error {
	if path == "" {
		return nil
	}
	var existing string
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
	}
	content, err := render.SyncNextMarkdown(existing, report)
	if err != nil {
		return err
	}
	return fsutil.AtomicWriteFile(path, []byte(content))
}

// renderToOutput writes rendered output to path when set, otherwise to stdout.
// When writing to a file it uses AtomicWriteFile for safe writes.
// resolveOutputPath appends an inferred extension to path when it has none.
// The extension is derived from format: "json" → .json, everything else → .md.
// When path is empty or already has an extension, it is returned unchanged.
func resolveOutputPath(path, format string) string {
	if path == "" || filepath.Ext(path) != "" {
		return path
	}
	if strings.ToLower(format) == "json" {
		return path + ".json"
	}
	return path + ".md"
}

func renderToOutput(path string, stdout io.Writer, fn func(io.Writer) error) error {
	if path == "" {
		return fn(stdout)
	}
	var buf strings.Builder
	if err := fn(&buf); err != nil {
		return err
	}
	return fsutil.AtomicWriteFile(path, []byte(buf.String()))
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func collectCatchUpForRepo(ctx context.Context, app app, owner, repo, since, viewer string, me, closedOnly, openOnly, commentsOnly, reviewOnly bool, progress io.Writer) ([]domain.CatchUpEntry, []string) {
	var warnings []string
	progressf(progress, "  issues %s/%s", owner, repo)
	issues, err := app.backend.ListRepoIssues(ctx, owner, repo, github.IssueQuery{State: "all", Since: since})
	if err != nil {
		if github.IsKind(err, github.ErrRateLimited) {
			warnings = append(warnings, fmt.Sprintf("rate-limited fetching issues for %s/%s (partial results)", owner, repo))
		} else {
			warnings = append(warnings, fmt.Sprintf("issues for %s/%s: %v", owner, repo, err))
		}
	}
	progressf(progress, "  prs %s/%s", owner, repo)
	prs, err := app.backend.ListRepoPRs(ctx, owner, repo, github.PRQuery{State: "all", Since: since})
	if err != nil {
		if github.IsKind(err, github.ErrRateLimited) {
			warnings = append(warnings, fmt.Sprintf("rate-limited fetching prs for %s/%s (partial results)", owner, repo))
		} else {
			warnings = append(warnings, fmt.Sprintf("prs for %s/%s: %v", owner, repo, err))
		}
	}
	if len(prs) > 20 {
		progressf(progress, "  warning: %d PRs in scope for %s/%s — review fetches scale with PR count and may approach GitHub rate limits", len(prs), owner, repo)
	}
	progressf(progress, "  issue comments %s/%s", owner, repo)
	issueComments, err := app.backend.ListIssueCommentActivity(ctx, owner, repo, since)
	if err != nil {
		if github.IsKind(err, github.ErrRateLimited) {
			warnings = append(warnings, fmt.Sprintf("rate-limited fetching issue comments for %s/%s (partial results)", owner, repo))
		} else {
			warnings = append(warnings, fmt.Sprintf("issue comments for %s/%s: %v", owner, repo, err))
		}
	}
	progressf(progress, "  review comments %s/%s", owner, repo)
	reviewComments, err := app.backend.ListPullRequestReviewCommentActivity(ctx, owner, repo, since)
	if err != nil {
		if github.IsKind(err, github.ErrRateLimited) {
			warnings = append(warnings, fmt.Sprintf("rate-limited fetching review comments for %s/%s (partial results)", owner, repo))
		} else {
			warnings = append(warnings, fmt.Sprintf("review comments for %s/%s: %v", owner, repo, err))
		}
	}
	progressf(progress, "  reviews %s/%s", owner, repo)
	reviews, err := app.backend.ListPullRequestReviewActivity(ctx, owner, repo, since)
	if err != nil {
		if github.IsKind(err, github.ErrRateLimited) {
			warnings = append(warnings, fmt.Sprintf("rate-limited fetching reviews for %s/%s (partial results)", owner, repo))
		} else {
			warnings = append(warnings, fmt.Sprintf("reviews for %s/%s: %v", owner, repo, err))
		}
	}
	progressf(progress, "  notifications %s/%s", owner, repo)
	notifications, err := app.backend.ListRepoNotifications(ctx, owner, repo, since)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("notifications for %s/%s: %v", owner, repo, err))
	}
	index := map[string]*domain.CatchUpEntry{}
	addEvent := func(event domain.ActivityEvent, title, url, state string, involvement []string) {
		key := fmt.Sprintf("%s#%d:%s", event.Repo, event.Number, event.Kind)
		entry, ok := index[key]
		if !ok {
			entry = &domain.CatchUpEntry{
				Repo: event.Repo, Owner: event.Owner, Number: event.Number, Kind: event.Kind,
				Title: title, URL: url, State: state,
			}
			index[key] = entry
		}
		entry.EventCount++
		switch event.EventType {
		case "closed", "merged":
			entry.HasClosure = true
		case "commented", "reviewed", "mentioned", "team_mentioned", "review_requested":
			entry.HasDiscussion = true
		}
		if event.OccurredAt.After(entry.LatestAt) {
			entry.LatestAt = event.OccurredAt
			entry.LatestActor = event.Actor
			entry.LatestEvent = event.EventType
			entry.Preview = event.Preview
			if event.Preview != "" {
				entry.PreviewSource = "exact"
			} else {
				entry.PreviewSource = ""
			}
		}
		entry.Involvement = mergeStrings(entry.Involvement, involvement)
	}
	addNotification := func(thread domain.NotificationThread, title, url, state string, involvement []string) {
		key := fmt.Sprintf("%s#%d:%s", thread.Repo, thread.Number, thread.Kind)
		entry, ok := index[key]
		if !ok {
			entry = &domain.CatchUpEntry{
				Repo: thread.Repo, Owner: thread.Owner, Number: thread.Number, Kind: thread.Kind,
				Title: firstNonEmpty(title, thread.Title), URL: firstNonEmpty(url, thread.URL), State: state,
			}
			index[key] = entry
		}
		entry.NotificationReasons = mergeStrings(entry.NotificationReasons, []string{thread.Reason})
		entry.Involvement = mergeStrings(entry.Involvement, involvement)
		switch thread.Reason {
		case "mention", "team_mention", "review_requested", "comment":
			entry.HasDiscussion = true
		}
		if thread.UpdatedAt.After(entry.LatestAt) {
			entry.LatestAt = thread.UpdatedAt
			entry.LatestActor = "github"
			entry.LatestEvent = notificationReasonToEvent(thread.Reason)
			if entry.Preview == "" {
				entry.Preview = "thread matched notification reason: " + thread.Reason
				entry.PreviewSource = "notification_only"
			}
		}
	}
	for _, issue := range issues {
		if issue.ClosedAt != nil && withinSinceReport(since, issue.ClosedAt) {
			addEvent(domain.ActivityEvent{
				Repo: repo, Owner: owner, Number: issue.Number, Kind: "issue", EventType: "closed", Actor: issue.Author,
				OccurredAt: *issue.ClosedAt,
			}, issue.Title, issue.URL, issue.State, involvementForIssue(issue, viewer))
		}
	}
	for _, pr := range prs {
		if pr.MergedAt != nil && withinSinceReport(since, pr.MergedAt) {
			mergedBy := pr.MergedBy
			if mergedBy == "" {
				mergedBy = pr.Author
			}
			addEvent(domain.ActivityEvent{
				Repo: repo, Owner: owner, Number: pr.Number, Kind: "pull_request", EventType: "merged", Actor: mergedBy,
				OccurredAt: *pr.MergedAt,
			}, pr.Title, pr.URL, pr.State, involvementForPR(pr, viewer))
		} else if pr.ClosedAt != nil && withinSinceReport(since, pr.ClosedAt) {
			addEvent(domain.ActivityEvent{
				Repo: repo, Owner: owner, Number: pr.Number, Kind: "pull_request", EventType: "closed", Actor: pr.Author,
				OccurredAt: *pr.ClosedAt,
			}, pr.Title, pr.URL, pr.State, involvementForPR(pr, viewer))
		}
	}
	for _, event := range append(append(issueComments, reviewComments...), reviews...) {
		var title, url, state string
		var involvement []string
		if event.Kind == "issue" {
			if issue, ok := findIssue(issues, event.Number); ok {
				title, url, state = issue.Title, issue.URL, issue.State
				involvement = mergeStrings(involvementForIssue(issue, viewer), involvementForActor(event.Actor, viewer, event.EventType))
			}
		} else {
			if pr, ok := findPR(prs, event.Number); ok {
				title, url, state = pr.Title, pr.URL, pr.State
				involvement = mergeStrings(involvementForPR(pr, viewer), involvementForActor(event.Actor, viewer, event.EventType))
			}
		}
		addEvent(event, title, url, state, involvement)
	}
	for _, thread := range notifications {
		var title, url, state string
		var involvement []string
		switch thread.Kind {
		case "pull_request":
			if pr, ok := findPR(prs, thread.Number); ok {
				title, url, state = pr.Title, pr.URL, pr.State
				involvement = mergeStrings(involvementForPR(pr, viewer), involvementForNotification(thread.Reason))
			} else {
				involvement = involvementForNotification(thread.Reason)
			}
		default:
			if issue, ok := findIssue(issues, thread.Number); ok {
				title, url, state = issue.Title, issue.URL, issue.State
				involvement = mergeStrings(involvementForIssue(issue, viewer), involvementForNotification(thread.Reason))
			} else {
				involvement = involvementForNotification(thread.Reason)
			}
		}
		addNotification(thread, title, url, state, involvement)
	}
	addCatchUpFallbackUpdates(index, issues, prs, since, viewer)
	if me && viewer != "" {
		progressf(progress, "  recovery %s/%s", owner, repo)
		recoverWarnings := recoverNotificationPreviews(ctx, app, owner, repo, since, viewer, index)
		warnings = append(warnings, recoverWarnings...)
	}
	var entries []domain.CatchUpEntry
	for _, entry := range index {
		if !matchesCatchUpFilters(entry, me, closedOnly, openOnly, commentsOnly, reviewOnly) {
			continue
		}
		entries = append(entries, *entry)
	}
	return entries, warnings
}

func addCatchUpFallbackUpdates(index map[string]*domain.CatchUpEntry, issues []domain.IssueSnapshot, prs []domain.PRSnapshot, since, viewer string) {
	for _, issue := range issues {
		if !withinSinceReport(since, &issue.UpdatedAt) {
			continue
		}
		key := fmt.Sprintf("%s#%d:%s", issue.Repo, issue.Number, "issue")
		if _, ok := index[key]; ok {
			continue
		}
		index[key] = &domain.CatchUpEntry{
			Repo:        issue.Repo,
			Owner:       issue.Owner,
			Number:      issue.Number,
			Kind:        "issue",
			Title:       issue.Title,
			URL:         issue.URL,
			LatestEvent: "updated",
			LatestActor: "github",
			LatestAt:    issue.UpdatedAt,
			Involvement: involvementForIssue(issue, viewer),
			EventCount:  1,
			State:       issue.State,
		}
	}
	for _, pr := range prs {
		if !withinSinceReport(since, &pr.UpdatedAt) {
			continue
		}
		key := fmt.Sprintf("%s#%d:%s", pr.Repo, pr.Number, "pull_request")
		if _, ok := index[key]; ok {
			continue
		}
		index[key] = &domain.CatchUpEntry{
			Repo:        pr.Repo,
			Owner:       pr.Owner,
			Number:      pr.Number,
			Kind:        "pull_request",
			Title:       pr.Title,
			URL:         pr.URL,
			LatestEvent: "updated",
			LatestActor: "github",
			LatestAt:    pr.UpdatedAt,
			Involvement: involvementForPR(pr, viewer),
			EventCount:  1,
			State:       pr.State,
		}
	}
}

func matchesCatchUpFilters(entry *domain.CatchUpEntry, me, closedOnly, openOnly, commentsOnly, reviewOnly bool) bool {
	if commentsOnly && !entry.HasDiscussion {
		return false
	}
	if closedOnly && !entry.HasClosure {
		return false
	}
	if openOnly {
		s := strings.ToLower(entry.State)
		if s == "closed" || s == "merged" {
			return false
		}
	}
	if reviewOnly && entry.LatestEvent != "review_requested" && !entry.ReviewNeeded {
		return false
	}
	if me && len(entry.Involvement) == 0 {
		return false
	}
	return true
}

func collectActivityForRepo(ctx context.Context, app app, owner, repo, since string, progress io.Writer) ([]domain.ActivityEntry, []string) {
	var warnings []string
	progressf(progress, "  issues %s/%s", owner, repo)
	issues, err := app.backend.ListRepoIssues(ctx, owner, repo, github.IssueQuery{State: "all", Since: since})
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("issues for %s/%s: %v", owner, repo, err))
	}
	progressf(progress, "  prs %s/%s", owner, repo)
	prs, err := app.backend.ListRepoPRs(ctx, owner, repo, github.PRQuery{State: "all", Since: since})
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("prs for %s/%s: %v", owner, repo, err))
	}
	progressf(progress, "  issue comments %s/%s", owner, repo)
	issueComments, err := app.backend.ListIssueCommentActivity(ctx, owner, repo, since)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("issue comments for %s/%s: %v", owner, repo, err))
	}
	progressf(progress, "  review comments %s/%s", owner, repo)
	reviewComments, err := app.backend.ListPullRequestReviewCommentActivity(ctx, owner, repo, since)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("review comments for %s/%s: %v", owner, repo, err))
	}
	progressf(progress, "  reviews %s/%s", owner, repo)
	reviews, err := app.backend.ListPullRequestReviewActivity(ctx, owner, repo, since)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("reviews for %s/%s: %v", owner, repo, err))
	}
	var entries []domain.ActivityEntry
	for _, issue := range issues {
		if withinSinceReport(since, &issue.CreatedAt) {
			entries = append(entries, domain.ActivityEntry{
				Repo: repo, Owner: owner, Number: issue.Number, Kind: "issue", Title: issue.Title, URL: issue.URL,
				State: issue.State, EventType: "opened", Actor: issue.Author, OccurredAt: issue.CreatedAt,
			})
		}
		if issue.ClosedAt != nil && withinSinceReport(since, issue.ClosedAt) {
			entries = append(entries, domain.ActivityEntry{
				Repo: repo, Owner: owner, Number: issue.Number, Kind: "issue", Title: issue.Title, URL: issue.URL,
				State: issue.State, EventType: "closed", Actor: issue.Author, OccurredAt: *issue.ClosedAt,
			})
		}
	}
	for _, pr := range prs {
		if withinSinceReport(since, &pr.CreatedAt) {
			entries = append(entries, domain.ActivityEntry{
				Repo: repo, Owner: owner, Number: pr.Number, Kind: "pull_request", Title: pr.Title, URL: pr.URL,
				State: pr.State, EventType: "opened", Actor: pr.Author, OccurredAt: pr.CreatedAt,
			})
		}
		switch {
		case pr.MergedAt != nil && withinSinceReport(since, pr.MergedAt):
			mergedBy := pr.MergedBy
			if mergedBy == "" {
				mergedBy = pr.Author
			}
			entries = append(entries, domain.ActivityEntry{
				Repo: repo, Owner: owner, Number: pr.Number, Kind: "pull_request", Title: pr.Title, URL: pr.URL,
				State: pr.State, EventType: "merged", Actor: mergedBy, OccurredAt: *pr.MergedAt,
			})
		case pr.ClosedAt != nil && withinSinceReport(since, pr.ClosedAt):
			entries = append(entries, domain.ActivityEntry{
				Repo: repo, Owner: owner, Number: pr.Number, Kind: "pull_request", Title: pr.Title, URL: pr.URL,
				State: pr.State, EventType: "closed", Actor: pr.Author, OccurredAt: *pr.ClosedAt,
			})
		case withinSinceReport(since, &pr.UpdatedAt):
			entries = append(entries, domain.ActivityEntry{
				Repo: repo, Owner: owner, Number: pr.Number, Kind: "pull_request", Title: pr.Title, URL: pr.URL,
				State: pr.State, EventType: "updated", Actor: pr.Author, OccurredAt: pr.UpdatedAt,
			})
		}
	}
	for _, event := range append(append(issueComments, reviewComments...), reviews...) {
		if event.Kind == "issue" {
			if issue, ok := findIssue(issues, event.Number); ok {
				entries = append(entries, activityEntryFromEvent(event, issue.Title, issue.URL, issue.State))
			}
			continue
		}
		if pr, ok := findPR(prs, event.Number); ok {
			entries = append(entries, activityEntryFromEvent(event, pr.Title, pr.URL, pr.State))
		}
	}
	return entries, warnings
}

func buildAllReposCatchUpReport(ctx context.Context, app app, cfg *config.Config, since, viewer string, me, closedOnly, openOnly, commentsOnly, reviewOnly bool, progress io.Writer) (domain.CatchUpReport, error) {
	repos, err := allWatchedRepos(cfg)
	if err != nil {
		return domain.CatchUpReport{}, err
	}
	report := domain.CatchUpReport{
		GeneratedAt: time.Now().UTC(),
		Target:      "all-watched-repos",
		TargetKind:  "portfolio",
		Since:       since,
	}
	for _, repo := range repos {
		progressf(progress, "fetching repo %s/%s", repo.Owner, repo.Name)
		entries, warnings := collectCatchUpForRepo(ctx, app, repo.Owner, repo.Name, since, viewer, me, closedOnly, openOnly, commentsOnly, reviewOnly, progress)
		report.Entries = append(report.Entries, entries...)
		if len(warnings) > 0 {
			report.Partial = true
			report.Warnings = append(report.Warnings, warnings...)
		}
	}
	return report, nil
}

func buildAllReposActivityReport(ctx context.Context, app app, cfg *config.Config, since string, progress io.Writer) (domain.ActivityReport, error) {
	repos, err := allWatchedRepos(cfg)
	if err != nil {
		return domain.ActivityReport{}, err
	}
	report := domain.ActivityReport{
		GeneratedAt: time.Now().UTC(),
		Target:      "all-watched-repos",
		TargetKind:  "portfolio",
		Since:       since,
	}
	for _, repo := range repos {
		progressf(progress, "fetching repo %s/%s", repo.Owner, repo.Name)
		entries, warnings := collectActivityForRepo(ctx, app, repo.Owner, repo.Name, since, progress)
		report.Entries = append(report.Entries, entries...)
		if len(warnings) > 0 {
			report.Partial = true
			report.Warnings = append(report.Warnings, warnings...)
		}
	}
	return report, nil
}

func allWatchedRepos(cfg *config.Config) ([]config.RepoWatch, error) {
	if len(cfg.Watch.Repos) == 0 {
		return nil, fmt.Errorf("no watched repos configured")
	}
	return append([]config.RepoWatch(nil), cfg.Watch.Repos...), nil
}

func activityEntryFromEvent(event domain.ActivityEvent, title, url, state string) domain.ActivityEntry {
	return domain.ActivityEntry{
		Repo: event.Repo, Owner: event.Owner, Number: event.Number, Kind: event.Kind, Title: title, URL: url,
		State: state, EventType: event.EventType, Actor: event.Actor, OccurredAt: event.OccurredAt, Preview: event.Preview, Path: event.Path,
	}
}

func sortActivityEntries(entries []domain.ActivityEntry) {
	slices.SortFunc(entries, func(a, b domain.ActivityEntry) int {
		if a.Repo != b.Repo {
			return strings.Compare(a.Repo, b.Repo)
		}
		if !a.OccurredAt.Equal(b.OccurredAt) {
			if a.OccurredAt.After(b.OccurredAt) {
				return -1
			}
			return 1
		}
		if a.Number != b.Number {
			return a.Number - b.Number
		}
		return strings.Compare(a.EventType, b.EventType)
	})
}

func recoverNotificationPreviews(ctx context.Context, app app, owner, repo, since, viewer string, index map[string]*domain.CatchUpEntry) []string {
	const maxRecoveries = 10
	var warnings []string
	recovered := 0
	for _, entry := range index {
		if recovered >= maxRecoveries {
			break
		}
		if len(entry.NotificationReasons) == 0 {
			continue
		}
		if hasConcretePreview(entry) {
			continue
		}
		var events []domain.ActivityEvent
		var err error
		switch entry.Kind {
		case "pull_request":
			comments, cerr := app.backend.ListPullRequestThreadComments(ctx, owner, repo, entry.Number)
			reviews, rerr := app.backend.ListPullRequestThreadReviews(ctx, owner, repo, entry.Number)
			if cerr != nil {
				warnings = append(warnings, fmt.Sprintf("recover pr comments for %s/%s#%d: %v", owner, repo, entry.Number, cerr))
			}
			if rerr != nil {
				warnings = append(warnings, fmt.Sprintf("recover pr reviews for %s/%s#%d: %v", owner, repo, entry.Number, rerr))
			}
			events = append(events, comments...)
			events = append(events, reviews...)
		default:
			events, err = app.backend.ListIssueThreadComments(ctx, owner, repo, entry.Number)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("recover issue comments for %s/%s#%d: %v", owner, repo, entry.Number, err))
				continue
			}
		}
		if applyRecoveredPreview(entry, events, viewer, since) {
			recovered++
		}
	}
	return warnings
}

func hasConcretePreview(entry *domain.CatchUpEntry) bool {
	return entry.Preview != "" && !strings.HasPrefix(entry.Preview, "thread matched notification reason:")
}

func applyRecoveredPreview(entry *domain.CatchUpEntry, events []domain.ActivityEvent, viewer, since string) bool {
	// Never overwrite an exact preview with a recovered one.
	if entry.PreviewSource == "exact" {
		return false
	}
	var best *domain.ActivityEvent
	for i := range events {
		event := events[i]
		if !withinSinceReport(since, &event.OccurredAt) {
			continue
		}
		if matchesNotificationReason(entry.NotificationReasons, event, viewer) {
			if best == nil || event.OccurredAt.After(best.OccurredAt) {
				best = &event
			}
		}
	}
	if best == nil {
		return false
	}
	entry.LatestActor = best.Actor
	entry.LatestAt = best.OccurredAt
	switch {
	case contains(entry.NotificationReasons, "review_requested"):
		entry.LatestEvent = "review_requested"
	case contains(entry.NotificationReasons, "mention"):
		entry.LatestEvent = "mentioned"
	case contains(entry.NotificationReasons, "team_mention"):
		entry.LatestEvent = "team_mentioned"
	default:
		entry.LatestEvent = best.EventType
	}
	entry.Preview = best.Preview
	entry.PreviewSource = "recovered"
	return true
}

func matchesNotificationReason(reasons []string, event domain.ActivityEvent, viewer string) bool {
	if contains(reasons, "review_requested") && event.Kind == "pull_request" && event.EventType == "reviewed" {
		return true
	}
	if contains(reasons, "mention") || contains(reasons, "team_mention") {
		return strings.Contains(strings.ToLower(event.Preview), "@"+strings.ToLower(viewer))
	}
	return false
}

func findIssue(issues []domain.IssueSnapshot, number int) (domain.IssueSnapshot, bool) {
	for _, issue := range issues {
		if issue.Number == number {
			return issue, true
		}
	}
	return domain.IssueSnapshot{}, false
}

func findPR(prs []domain.PRSnapshot, number int) (domain.PRSnapshot, bool) {
	for _, pr := range prs {
		if pr.Number == number {
			return pr, true
		}
	}
	return domain.PRSnapshot{}, false
}

func involvementForIssue(issue domain.IssueSnapshot, viewer string) []string {
	var out []string
	if viewer == "" {
		return out
	}
	if issue.Author == viewer {
		out = append(out, "author")
	}
	if contains(issue.Assignees, viewer) {
		out = append(out, "assignee")
	}
	return out
}

func involvementForPR(pr domain.PRSnapshot, viewer string) []string {
	var out []string
	if viewer == "" {
		return out
	}
	if pr.Author == viewer {
		out = append(out, "author")
	}
	if contains(pr.Assignees, viewer) {
		out = append(out, "assignee")
	}
	return out
}

func involvementForActor(actor, viewer, eventType string) []string {
	if viewer == "" || actor != viewer {
		return nil
	}
	switch eventType {
	case "reviewed":
		return []string{"reviewer"}
	default:
		return []string{"commenter"}
	}
}

func involvementForNotification(reason string) []string {
	switch reason {
	case "mention":
		return []string{"mentioned"}
	case "team_mention":
		return []string{"team-mentioned"}
	case "review_requested":
		return []string{"review-requested"}
	case "assign":
		return []string{"assigned"}
	default:
		return nil
	}
}

func mergeStrings(base, extra []string) []string {
	for _, value := range extra {
		if !contains(base, value) {
			base = append(base, value)
		}
	}
	return base
}

func sortCatchUpEntries(entries []domain.CatchUpEntry) {
	slices.SortFunc(entries, func(a, b domain.CatchUpEntry) int {
		if !a.LatestAt.Equal(b.LatestAt) {
			if a.LatestAt.After(b.LatestAt) {
				return -1
			}
			return 1
		}
		if len(a.Involvement) != len(b.Involvement) {
			return len(b.Involvement) - len(a.Involvement)
		}
		return a.Number - b.Number
	})
}

func withinSinceReport(since string, t *time.Time) bool {
	if since == "" || t == nil {
		return true
	}
	threshold, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return true
	}
	return t.After(threshold) || t.Equal(threshold)
}

func notificationReasonToEvent(reason string) string {
	switch reason {
	case "mention":
		return "mentioned"
	case "team_mention":
		return "team_mentioned"
	case "review_requested":
		return "review_requested"
	default:
		return "updated"
	}
}

// collectWatchedItemEntry fetches per-item activity for a single watched item and
// returns a WatchedItemEntry summarising the most recent event since the cursor.
// Errors are surfaced as warnings so the caller can mark the report partial.
func collectWatchedItemEntry(ctx context.Context, app app, iw config.ItemWatch, since string, progress io.Writer) (domain.WatchedItemEntry, []string) {
	var warnings []string
	entry := domain.WatchedItemEntry{
		Alias:  iw.Alias,
		Owner:  iw.Owner,
		Repo:   iw.Repo,
		Number: iw.Number,
		Kind:   iw.Kind,
	}

	progressf(progress, "  watched %s %s/%s#%d", iw.Kind, iw.Owner, iw.Repo, iw.Number)

	var events []domain.ActivityEvent

	switch iw.Kind {
	case "pr":
		comments, cerr := app.backend.ListPullRequestThreadComments(ctx, iw.Owner, iw.Repo, iw.Number)
		if cerr != nil {
			if github.IsKind(cerr, github.ErrRateLimited) {
				warnings = append(warnings, fmt.Sprintf("rate-limited fetching pr comments for %s/%s#%d (partial)", iw.Owner, iw.Repo, iw.Number))
			} else {
				warnings = append(warnings, fmt.Sprintf("pr comments for %s/%s#%d: %v", iw.Owner, iw.Repo, iw.Number, cerr))
			}
		}
		reviews, rerr := app.backend.ListPullRequestThreadReviews(ctx, iw.Owner, iw.Repo, iw.Number)
		if rerr != nil {
			if github.IsKind(rerr, github.ErrRateLimited) {
				warnings = append(warnings, fmt.Sprintf("rate-limited fetching pr reviews for %s/%s#%d (partial)", iw.Owner, iw.Repo, iw.Number))
			} else {
				warnings = append(warnings, fmt.Sprintf("pr reviews for %s/%s#%d: %v", iw.Owner, iw.Repo, iw.Number, rerr))
			}
		}
		events = append(events, comments...)
		events = append(events, reviews...)

		// Also fetch current PR state.
		prs, perr := app.backend.ListRepoPRs(ctx, iw.Owner, iw.Repo, github.PRQuery{State: "all"})
		if perr == nil {
			if pr, ok := findPR(prs, iw.Number); ok {
				entry.Title = pr.Title
				entry.URL = pr.URL
				entry.State = pr.State
				// Include state-change events.
				if pr.MergedAt != nil && withinSinceReport(since, pr.MergedAt) {
					events = append(events, domain.ActivityEvent{
						Repo: iw.Repo, Owner: iw.Owner, Number: iw.Number, Kind: "pull_request",
						EventType: "merged", Actor: pr.Author, OccurredAt: *pr.MergedAt,
					})
				} else if pr.ClosedAt != nil && withinSinceReport(since, pr.ClosedAt) {
					events = append(events, domain.ActivityEvent{
						Repo: iw.Repo, Owner: iw.Owner, Number: iw.Number, Kind: "pull_request",
						EventType: "closed", Actor: pr.Author, OccurredAt: *pr.ClosedAt,
					})
				}
			}
		}
	default: // "issue"
		comments, cerr := app.backend.ListIssueThreadComments(ctx, iw.Owner, iw.Repo, iw.Number)
		if cerr != nil {
			if github.IsKind(cerr, github.ErrRateLimited) {
				warnings = append(warnings, fmt.Sprintf("rate-limited fetching issue comments for %s/%s#%d (partial)", iw.Owner, iw.Repo, iw.Number))
			} else {
				warnings = append(warnings, fmt.Sprintf("issue comments for %s/%s#%d: %v", iw.Owner, iw.Repo, iw.Number, cerr))
			}
		}
		events = comments

		// Also fetch current issue state.
		issues, ierr := app.backend.ListRepoIssues(ctx, iw.Owner, iw.Repo, github.IssueQuery{State: "all"})
		if ierr == nil {
			if issue, ok := findIssue(issues, iw.Number); ok {
				entry.Title = issue.Title
				entry.URL = issue.URL
				entry.State = issue.State
				if issue.ClosedAt != nil && withinSinceReport(since, issue.ClosedAt) {
					events = append(events, domain.ActivityEvent{
						Repo: iw.Repo, Owner: iw.Owner, Number: iw.Number, Kind: "issue",
						EventType: "closed", Actor: issue.Author, OccurredAt: *issue.ClosedAt,
					})
				}
			}
		}
	}

	// Find the most recent event within the since window.
	for _, ev := range events {
		if !withinSinceReport(since, &ev.OccurredAt) {
			continue
		}
		entry.EventCount++
		if ev.OccurredAt.After(entry.LatestAt) {
			entry.LatestAt = ev.OccurredAt
			entry.LatestActor = ev.Actor
			entry.LatestEvent = ev.EventType
			entry.Preview = ev.Preview
		}
	}

	return entry, warnings
}

// collectWatchedItems fetches per-item activity for all configured watched items.
func collectWatchedItems(ctx context.Context, app app, cfg *config.Config, since string, progress io.Writer) ([]domain.WatchedItemEntry, []string) {
	var entries []domain.WatchedItemEntry
	var warnings []string
	for _, iw := range cfg.Watch.Items {
		entry, w := collectWatchedItemEntry(ctx, app, iw, since, progress)
		warnings = append(warnings, w...)
		// Always include watched items regardless of --me or other filters.
		entries = append(entries, entry)
	}
	return entries, warnings
}
