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
	"sync"
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
	configPath            string
	format                string
	allowOutsideWorkspace bool
}

type app struct {
	store    config.Store
	state    config.StateStore
	backend  github.Backend
	renderer render.Renderer
}

type doctorReport struct {
	TokenSource   string
	Authenticated bool
	Login         string
	RepoScope     bool
	ProjectScope  bool
	Warning       string
}

const (
	maxReposInFlight           = 3
	maxTopLevelFetchesPerRepo  = 4
	maxPRReviewThreadsInFlight = 4
)

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
	cmd.PersistentFlags().StringVar(&opts.configPath, "config", config.DefaultConfigPath, "config path (defaults to .ferret/config.yaml; outside .ferret requires --allow-outside-workspace)")
	cmd.PersistentFlags().StringVar(&opts.format, "format", "text", "output format: text|json|markdown")
	cmd.PersistentFlags().BoolVar(&opts.allowOutsideWorkspace, "allow-outside-workspace", false, "allow config and output paths outside the approved .ferret workspace roots")

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
			res := auth.Check(ctx)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Authenticated as @%s\n", login)
			if res.Source != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Token source: %s\n", res.Source)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Initialized Ferret config at %s\n\n", path)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Recommended first steps:")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "1. Verify auth and token scopes")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "   ferret doctor")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "2. Watch a repo (run without args to see recently active repos)")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "   ferret watch repo OWNER/REPO")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "3. See what changed")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "   ferret catch-up my-repo --since 7d --me")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "4. Get a broad summary")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "   ferret manager my-repo --since 7d")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "5. Add a project later for board-aware summaries")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "   ferret watch project OWNER/NUMBER --alias my-board --link-repo my-repo")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "6. Inspect the board structure")
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
		Short: "Show raw metadata for a watched repo, project, issue, or pull request",
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
			if err := validateTrustedConfigPaths(cfg, opts.allowOutsideWorkspace); err != nil {
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
			if err := validateTrustedConfigPaths(cfg, opts.allowOutsideWorkspace); err != nil {
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
	issueCmd := &cobra.Command{
		Use:   "issue <owner>/<repo>#<number>|<alias>",
		Short: "Show metadata about a watched or direct issue target",
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
			if err := validateTrustedConfigPaths(cfg, opts.allowOutsideWorkspace); err != nil {
				return err
			}
			owner, repo, number, err := resolveItemRef(cfg, args[0], "issue")
			if err != nil {
				return err
			}
			issue, err := app.backend.GetIssue(ctx, owner, repo, number)
			if err != nil {
				return err
			}
			return app.renderer.RenderIssueInspect(cmd.OutOrStdout(), issue)
		},
	}
	prCmd := &cobra.Command{
		Use:   "pr <owner>/<repo>#<number>|<alias>",
		Short: "Show metadata about a watched or direct pull request target",
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
			if err := validateTrustedConfigPaths(cfg, opts.allowOutsideWorkspace); err != nil {
				return err
			}
			owner, repo, number, err := resolveItemRef(cfg, args[0], "pr")
			if err != nil {
				return err
			}
			pr, err := app.backend.GetPullRequest(ctx, owner, repo, number)
			if err != nil {
				return err
			}
			return app.renderer.RenderPRInspect(cmd.OutOrStdout(), pr)
		},
	}
	cmd.AddCommand(projectCmd, repoCmd, issueCmd, prCmd)
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
			if _, err := app.backend.GetProjectSchema(ctx, owner, number); err != nil {
				return err
			}
			var resolvedPlanFile string
			if err := app.store.Update(ctx, func(cfg *config.Config) error {
				if err := validateTrustedConfigPaths(cfg, opts.allowOutsideWorkspace); err != nil {
					return err
				}
				resolvedPlanFile = planFile
				if resolvedPlanFile == "" {
					resolvedPlanFile = filepath.Join(cfg.Defaults.PlanDir, alias+".md")
				}
				planRoot, err := approvedRoot(config.DefaultPlanDir)
				if err != nil {
					return err
				}
				resolvedPlanFile, err = secureOutputPath(resolvedPlanFile, planRoot, opts.allowOutsideWorkspace)
				if err != nil {
					return err
				}
				return config.AddProjectWatch(cfg, config.ProjectWatch{
					Alias: alias, Owner: owner, Number: number, LinkedRepos: linkedRepos,
					StatusField: statusField, Output: config.ProjectOutput{PlanFile: resolvedPlanFile},
				})
			}); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Watching project %s/%d as %q\n", owner, number, alias)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Plan file: %s\n", resolvedPlanFile)
			if statusField != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Status field: %s\n", statusField)
			}
			return nil
		},
	}
	projectCmd.Flags().StringVar(&alias, "alias", "", "local alias")
	projectCmd.Flags().StringSliceVar(&linkedRepos, "link-repo", nil, "linked repo aliases")
	projectCmd.Flags().StringVar(&planFile, "plan-file", "", "plan markdown path under .ferret/plans/ by default")
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
			if err := app.store.Update(ctx, func(cfg *config.Config) error {
				if err := validateTrustedConfigPaths(cfg, opts.allowOutsideWorkspace); err != nil {
					return err
				}
				return config.AddRepoWatch(cfg, config.RepoWatch{Alias: effectiveAlias, Owner: owner, Name: repo})
			}); err != nil {
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
			if err := validateTrustedConfigPaths(cfg, opts.allowOutsideWorkspace); err != nil {
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
		Use:   kind + " OWNER/REPO NUMBER | OWNER/REPO#NUMBER [--alias name]",
		Short: fmt.Sprintf("Watch a specific %s by OWNER/REPO and NUMBER", kind),
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			var (
				owner  string
				repo   string
				number int
				err    error
			)
			if len(args) == 1 {
				owner, repo, number, err = splitItemRef(args[0])
				if err != nil {
					return err
				}
			} else {
				owner, repo, err = splitRepoRef(args[0])
				if err != nil {
					return err
				}
				number, err = strconv.Atoi(args[1])
				if err != nil || number <= 0 {
					return fmt.Errorf("NUMBER must be a positive integer, got %q", args[1])
				}
			}
			if itemAlias == "" {
				itemAlias = fmt.Sprintf("%s-%s-%d", owner, repo, number)
			}
			app, err := newApp(opts, cmd)
			if err != nil {
				return err
			}
			if err := app.store.Update(ctx, func(cfg *config.Config) error {
				if err := validateTrustedConfigPaths(cfg, opts.allowOutsideWorkspace); err != nil {
					return err
				}
				return config.AddItemWatch(cfg, config.ItemWatch{
					Alias: itemAlias, Owner: owner, Repo: repo, Number: number, Kind: kind,
				})
			}); err != nil {
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
		return app.store.Update(ctx, func(cfg *config.Config) error {
			if err := validateTrustedConfigPaths(cfg, opts.allowOutsideWorkspace); err != nil {
				return err
			}
			if len(args) == 1 {
				if strings.Contains(args[0], "#") && strings.Contains(args[0], "/") {
					owner, repo, number, err := splitItemRef(args[0])
					if err != nil {
						return err
					}
					return config.RemoveItemWatchByOwnerRepoNumber(cfg, owner, repo, number, kind)
				}
				return config.RemoveItemWatch(cfg, args[0])
			}
			owner, repo, err := splitRepoRef(args[0])
			if err != nil {
				return err
			}
			number, err := strconv.Atoi(args[1])
			if err != nil || number <= 0 {
				return fmt.Errorf("NUMBER must be a positive integer, got %q", args[1])
			}
			return config.RemoveItemWatchByOwnerRepoNumber(cfg, owner, repo, number, kind)
		})
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
			return app.store.Update(cmd.Context(), func(cfg *config.Config) error {
				if err := validateTrustedConfigPaths(cfg, opts.allowOutsideWorkspace); err != nil {
					return err
				}
				for _, alias := range args {
					if err := config.RemoveProjectWatch(cfg, alias); err != nil {
						return err
					}
				}
				return nil
			})
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
			return app.store.Update(cmd.Context(), func(cfg *config.Config) error {
				if err := validateTrustedConfigPaths(cfg, opts.allowOutsideWorkspace); err != nil {
					return err
				}
				for _, alias := range args {
					if err := config.RemoveRepoWatch(cfg, alias); err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
	unwatchIssueCmd := &cobra.Command{
		Use:   "issue <alias> | OWNER/REPO NUMBER | OWNER/REPO#NUMBER",
		Short: "Stop watching a specific issue",
		Args:  cobra.RangeArgs(1, 2),
		RunE:  newUnwatchItemRunE(opts, "issue"),
	}
	unwatchPRCmd := &cobra.Command{
		Use:   "pr <alias> | OWNER/REPO NUMBER | OWNER/REPO#NUMBER",
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
board items are included as a current snapshot. In project mode, --since
only scopes linked repo enrichment, not the board item list itself.

Scoping:

  manager               runs across all watched repos
  manager <alias>       scopes to one repo or project alias
  manager --all         explicit all-repos mode

Output:

  --out .ferret/results.md      writes markdown to a file
  --out .ferret/results.json    writes JSON (extension determines format)
  --format json         override format explicitly

Examples:

  ferret manager my-repo
  ferret manager my-board --since 7d
  ferret manager --all --out .ferret/summary.md`,
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
				return renderToOutput(out, cmd.OutOrStdout(), opts.allowOutsideWorkspace, func(w io.Writer) error {
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
				items, err := loadProjectItemsForSnapshot(ctx, app.backend, project.Owner, project.Number, viewer, project.StatusField)
				if err != nil {
					return err
				}
				boardLookup := buildBoardStatusLookup(project, items)
				items = filterItems(items, filter)
				report.Items = items
				report.Summary, report.Warnings = summarizeProjectItems(items, project.StatusField)
				if len(report.Warnings) > 0 {
					report.Partial = true
				}
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
				return renderToOutput(out, cmd.OutOrStdout(), opts.allowOutsideWorkspace, func(w io.Writer) error {
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
			return renderToOutput(out, cmd.OutOrStdout(), opts.allowOutsideWorkspace, func(w io.Writer) error {
				return app.renderer.RenderManager(w, report)
			})
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "optional lookback duration or timestamp (e.g. 24h, 7d, 2w, 2026-01-15); no cursor is used")
	cmd.Flags().StringVar(&filter, "filter", "", "filter preset")
	cmd.Flags().StringVar(&out, "out", "", "write output to file under .ferret/ by default (default format: markdown)")
	cmd.Flags().BoolVar(&all, "all", false, "run across all watched repos")
	return cmd
}

func newNextCmd(opts *rootOptions) *cobra.Command {
	var since string
	var filter string
	var out string
	var all bool
	var me bool
	cmd := &cobra.Command{
		Use:   "next [alias]",
		Short: "Show open work grouped for action",
		Long: `Show open work items grouped for action across one or more repos and projects.

When an alias is provided the command scopes output to that single repo or
project — no --all flag is required. Omitting an alias (or passing --all)
runs across every watched repo.

This is an action view, not a board-ground-truth report. Use manager for a
full project snapshot. In project mode, --since only scopes linked repo
enrichment; the board item set remains a current snapshot.

Filter presets:

  open             All items that are not closed or done
  assigned-to-me   Only items assigned to the current viewer
  actionable       Items that are not blocked, done, or in an unknown state
  recently-closed  Only items that are closed or merged

Examples:

  ferret next my-repo
  ferret next my-board --me
  ferret next my-board --filter assigned-to-me
  ferret next --all --filter actionable
  ferret next my-repo --out .ferret/plans/next.md`,
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
			filter, err = resolveNextFilter(filter, me)
			if err != nil {
				return err
			}
			effectiveSince, err := resolveSince(firstNonEmpty(since, defaultSinceForAlias(cfg, ref), "30d"))
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
				if err := syncNextFile(out, report, opts); err != nil {
					return err
				}
				return app.renderer.RenderNext(cmd.OutOrStdout(), report)
			}
			if project, err := config.ResolveProject(cfg, ref); err == nil {
				items, err := loadProjectItemsForSnapshot(ctx, app.backend, project.Owner, project.Number, viewer, project.StatusField)
				if err != nil {
					return err
				}
				report := buildProjectNextReport(project.Alias, items, effectiveSince, firstNonEmpty(filter, "actionable"))
				report.TargetKind = "project"
				_, projectWarnings := summarizeProjectItems(items, project.StatusField)
				report.Warnings = append(report.Warnings, projectWarnings...)
				if viewerErr != nil {
					report.Warnings = append(report.Warnings, "viewer lookup failed; assigned-to-me grouping may be incomplete")
				}
				if out == "" {
					out = project.Output.PlanFile
				}
				if err := syncNextFile(out, report, opts); err != nil {
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
			if err := syncNextFile(out, report, opts); err != nil {
				return err
			}
			return app.renderer.RenderNext(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "lookback duration or timestamp (e.g. 24h, 7d, 2w, 2026-01-15); defaults to 30d when no per-alias default is set")
	cmd.Flags().StringVar(&filter, "filter", "", "filter preset: open, assigned-to-me, actionable, recently-closed")
	cmd.Flags().StringVar(&out, "out", "", "markdown output path under .ferret/plans/ by default")
	cmd.Flags().BoolVar(&all, "all", false, "run across all watched repos")
	cmd.Flags().BoolVar(&me, "me", false, "alias for --filter assigned-to-me")
	return cmd
}

func newCatchUpCmd(opts *rootOptions) *cobra.Command {
	var since string
	var me bool
	var closedOnly bool
	var openOnly bool
	var commentsOnly bool
	var reviewOnly bool
	var expandOrder string
	var reviewBudget int
	var diagnostics bool
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
  catch-up <alias>       scopes to one repo, project, or watched item alias
  catch-up --all         explicit all-repos mode

Filtering:

  --me                 only show items you are involved in
  --closed-only        only show recently merged or closed items
  --comments-only      only show items with comments or reviews

Output:

  --out .ferret/results.md      writes markdown to a file
  --out .ferret/results.json    writes JSON (extension determines format)
  --format json         override format explicitly

Note: deep review expansion and preview recovery use internal safety budgets.
When those budgets are hit, Ferret emits explicit warnings and returns the
best partial result it has. The budget only affects deep review-thread
expansion, not the base issue and pull-request facts in scope. Under
truncation, Ferret defaults to a balanced expansion order based on already-
fetched GitHub signals. Use --expand-order recency for a more neutral mode.
Use --review-budget to control how many PRs receive deep review-thread fetches.
Set defaults.catch_up.review_budget to make that budget persistent.
Use --diagnostics to print the full detailed warning list when a run is partial.

Examples:

  ferret catch-up my-repo --me
  ferret catch-up my-repo --expand-order recency
  ferret catch-up my-repo --review-budget 10
  ferret catch-up --since 7d
  ferret catch-up --all --me --out .ferret/catchup.md`,
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
			resolvedExpandOrder, err := resolveCatchUpExpandOrder(cfg, expandOrder)
			if err != nil {
				return err
			}
			resolvedBudget, err := resolveCatchUpBudget(cfg, reviewBudget)
			if err != nil {
				return err
			}
			effectiveSince, usedCursor, err := resolveStatefulSince(ctx, app.state, "catch-up", defaultCursorScope(all, ref), since, defaultSinceForAlias(cfg, ref), "24h")
			if err != nil {
				return err
			}
			var report domain.CatchUpReport
			if all {
				report, err = buildAllReposCatchUpReport(ctx, app, cfg, effectiveSince, viewer, me, closedOnly, openOnly, commentsOnly, reviewOnly, resolvedExpandOrder, resolvedBudget, cmd.ErrOrStderr())
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
				linkedRepos, resolveWarnings := resolveLinkedRepos(cfg, project.LinkedRepos)
				if len(resolveWarnings) > 0 {
					report.Partial = true
					report.Warnings = append(report.Warnings, resolveWarnings...)
				}
				type repoResult struct {
					entries  []domain.CatchUpEntry
					warnings []string
				}
				results := make([]repoResult, len(linkedRepos))
				var wg sync.WaitGroup
				sem := make(chan struct{}, maxReposInFlight)
				for i, repo := range linkedRepos {
					i, repo := i, repo
					wg.Add(1)
					go func() {
						defer wg.Done()
						sem <- struct{}{}
						defer func() { <-sem }()
						progressf(cmd.ErrOrStderr(), "fetching repo %s/%s", repo.Owner, repo.Name)
						entries, warnings := collectCatchUpForRepo(ctx, app, repo.Owner, repo.Name, effectiveSince, viewer, me, closedOnly, openOnly, commentsOnly, reviewOnly, resolvedExpandOrder, resolvedBudget, cmd.ErrOrStderr())
						results[i] = repoResult{entries: entries, warnings: warnings}
					}()
				}
				wg.Wait()
				for _, result := range results {
					report.Entries = append(report.Entries, result.entries...)
					if len(result.warnings) > 0 {
						report.Partial = true
						report.Warnings = append(report.Warnings, result.warnings...)
					}
				}
				if len(projectBoardLookup) > 0 {
					applyBoardStatusToCatchUp(report.Entries, projectBoardLookup)
				}
			} else if item, err := config.ResolveItem(cfg, ref); err == nil {
				report = domain.CatchUpReport{
					GeneratedAt: time.Now().UTC(),
					Target:      item.Alias,
					TargetKind:  "item",
					Since:       effectiveSince,
				}
				entry, warnings := collectWatchedItemEntry(ctx, app, *item, effectiveSince, cmd.ErrOrStderr())
				report.WatchedItems = []domain.WatchedItemEntry{entry}
				if len(warnings) > 0 {
					report.Partial = true
					report.Warnings = warnings
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
				entries, warnings := collectCatchUpForRepo(ctx, app, repo.Owner, repo.Name, effectiveSince, viewer, me, closedOnly, openOnly, commentsOnly, reviewOnly, resolvedExpandOrder, resolvedBudget, cmd.ErrOrStderr())
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
			if report.TargetKind != "item" && len(cfg.Watch.Items) > 0 {
				progressf(cmd.ErrOrStderr(), "fetching watched items")
				watchedEntries, watchedWarnings := collectWatchedItems(ctx, app, cfg, effectiveSince, cmd.ErrOrStderr())
				watchedEntries = filterWatchedItemsAgainstCatchUpEntries(watchedEntries, report.Entries)
				report.WatchedItems = watchedEntries
				if len(watchedWarnings) > 0 {
					report.Partial = true
					report.Warnings = append(report.Warnings, watchedWarnings...)
				}
			}
			sortCatchUpEntries(report.Entries)
			finalizeCatchUpDiagnostics(&report, diagnostics)
			if err := renderToOutput(out, cmd.OutOrStdout(), opts.allowOutsideWorkspace, func(w io.Writer) error {
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
	cmd.Flags().StringVar(&expandOrder, "expand-order", "", "deep review expansion order: balanced or recency")
	cmd.Flags().IntVar(&reviewBudget, "review-budget", 0, "max PRs to expand for deep review-thread fetches (0 uses the default budget)")
	cmd.Flags().BoolVar(&diagnostics, "diagnostics", false, "show the full detailed warning list when catch-up is partial")
	cmd.Flags().StringVar(&out, "out", "", "write output to file under .ferret/ by default (default format: markdown)")
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
  activity <alias>       scopes to one repo, project, or watched item alias
  activity --all         explicit all-repos mode

Output:

  --out .ferret/results.md      writes markdown to a file
  --out .ferret/results.json    writes JSON (extension determines format)
  --format json         override format explicitly

The cursor advances after each successful run (same behaviour as catch-up).
Pass --since to override the cursor without advancing it.

Examples:

  ferret activity my-repo
  ferret activity --since 3d
  ferret activity my-repo --out .ferret/activity.md`,
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
				linkedRepos, resolveWarnings := resolveLinkedRepos(cfg, project.LinkedRepos)
				if len(resolveWarnings) > 0 {
					report.Partial = true
					report.Warnings = append(report.Warnings, resolveWarnings...)
				}
				type repoResult struct {
					entries  []domain.ActivityEntry
					warnings []string
				}
				results := make([]repoResult, len(linkedRepos))
				var wg sync.WaitGroup
				sem := make(chan struct{}, maxReposInFlight)
				for i, repo := range linkedRepos {
					i, repo := i, repo
					wg.Add(1)
					go func() {
						defer wg.Done()
						sem <- struct{}{}
						defer func() { <-sem }()
						progressf(cmd.ErrOrStderr(), "fetching repo %s/%s", repo.Owner, repo.Name)
						entries, warnings := collectActivityForRepo(ctx, app, repo.Owner, repo.Name, effectiveSince, cmd.ErrOrStderr())
						results[i] = repoResult{entries: entries, warnings: warnings}
					}()
				}
				wg.Wait()
				for _, result := range results {
					report.Entries = append(report.Entries, result.entries...)
					if len(result.warnings) > 0 {
						report.Partial = true
						report.Warnings = append(report.Warnings, result.warnings...)
					}
				}
				if len(projectBoardLookupAct) > 0 {
					applyBoardStatusToActivity(report.Entries, projectBoardLookupAct)
				}
			} else if item, err := config.ResolveItem(cfg, ref); err == nil {
				report = domain.ActivityReport{
					GeneratedAt: time.Now().UTC(),
					Target:      item.Alias,
					TargetKind:  "item",
					Since:       effectiveSince,
				}
				entry, warnings := collectWatchedItemEntry(ctx, app, *item, effectiveSince, cmd.ErrOrStderr())
				report.WatchedItems = []domain.WatchedItemEntry{entry}
				if len(warnings) > 0 {
					report.Partial = true
					report.Warnings = warnings
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
			if report.TargetKind != "item" && len(cfg.Watch.Items) > 0 {
				progressf(cmd.ErrOrStderr(), "fetching watched items")
				watchedEntries, watchedWarnings := collectWatchedItems(ctx, app, cfg, effectiveSince, cmd.ErrOrStderr())
				watchedEntries = filterWatchedItemsAgainstActivityEntries(watchedEntries, report.Entries)
				report.WatchedItems = watchedEntries
				if len(watchedWarnings) > 0 {
					report.Partial = true
					report.Warnings = append(report.Warnings, watchedWarnings...)
				}
			}
			sortActivityEntries(report.Entries)
			if err := renderToOutput(out, cmd.OutOrStdout(), opts.allowOutsideWorkspace, func(w io.Writer) error {
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
	cmd.Flags().StringVar(&out, "out", "", "write output to file under .ferret/ by default (default format: markdown)")
	cmd.Flags().BoolVar(&all, "all", false, "run across all watched repos")
	return cmd
}

func newApp(opts *rootOptions, cmd *cobra.Command) (app, error) {
	r, err := render.Factory{}.ForFormat(opts.format)
	if err != nil {
		return app{}, err
	}
	configPath, err := secureConfigPath(opts)
	if err != nil {
		return app{}, err
	}
	res, err := auth.Resolve(cmd.Context(), cmd.ErrOrStderr(), cmd.InOrStdin())
	if err != nil {
		return app{}, fmt.Errorf("authentication: %w", err)
	}
	return app{
		store:    config.FileStore{Path: configPath},
		state:    config.FileStateStore{Path: config.StatePathForConfig(configPath)},
		backend:  github.NewHTTPBackend(res.Token),
		renderer: r,
	}, nil
}

func secureConfigPath(opts *rootOptions) (string, error) {
	path := config.ExpandPath(opts.configPath)
	if opts.allowOutsideWorkspace {
		return path, nil
	}
	root, err := approvedRoot(config.DefaultOutputDir)
	if err != nil {
		return "", err
	}
	return fsutil.ResolveApprovedWritePath(path, root)
}

func approvedRoot(rel string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, rel), nil
}

func secureOutputPath(path, root string, allowOutside bool) (string, error) {
	path = config.ExpandPath(path)
	if path == "" {
		return "", nil
	}
	if allowOutside {
		return path, nil
	}
	return fsutil.ResolveApprovedWritePath(path, root)
}

func validateTrustedConfigPaths(cfg *config.Config, allowOutside bool) error {
	if allowOutside {
		return nil
	}
	outputRoot, err := approvedRoot(config.DefaultOutputDir)
	if err != nil {
		return err
	}
	planRoot, err := approvedRoot(config.DefaultPlanDir)
	if err != nil {
		return err
	}
	if _, err := fsutil.ResolveApprovedWritePath(config.ExpandPath(cfg.Defaults.OutputDir), outputRoot); err != nil {
		return fmt.Errorf("unsafe config: defaults.output_dir must stay under %s: %w", outputRoot, err)
	}
	if _, err := fsutil.ResolveApprovedWritePath(config.ExpandPath(cfg.Defaults.PlanDir), planRoot); err != nil {
		return fmt.Errorf("unsafe config: defaults.plan_dir must stay under %s: %w", planRoot, err)
	}
	for _, project := range cfg.Watch.Projects {
		if project.Output.PlanFile == "" {
			continue
		}
		if _, err := fsutil.ResolveApprovedWritePath(config.ExpandPath(project.Output.PlanFile), planRoot); err != nil {
			return fmt.Errorf("unsafe config: project %q output.plan_file must stay under %s: %w", project.Alias, planRoot, err)
		}
	}
	return nil
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

func resolveItemRef(cfg *config.Config, arg, kind string) (owner string, repo string, number int, err error) {
	if strings.Contains(arg, "#") && strings.Count(arg, "/") == 1 {
		return splitItemRef(arg)
	}
	item, err := config.ResolveItem(cfg, arg)
	if err != nil {
		return "", "", 0, err
	}
	if !strings.EqualFold(item.Kind, kind) {
		return "", "", 0, fmt.Errorf("item alias %q is a %s, not a %s", arg, item.Kind, kind)
	}
	return item.Owner, item.Repo, item.Number, nil
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

func splitItemRef(ref string) (string, string, int, error) {
	parts := strings.Split(ref, "#")
	if len(parts) != 2 {
		return "", "", 0, fmt.Errorf("item ref must be owner/repo#number")
	}
	owner, repo, err := splitRepoRef(parts[0])
	if err != nil {
		return "", "", 0, err
	}
	number, err := strconv.Atoi(parts[1])
	if err != nil || number <= 0 {
		return "", "", 0, fmt.Errorf("NUMBER must be a positive integer, got %q", parts[1])
	}
	return owner, repo, number, nil
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

func summarizeProjectItems(items []domain.ProjectItem, statusField string) (map[string]int, []string) {
	if statusField == "" {
		return summarizeItems(items), nil
	}
	out := map[string]int{"total": len(items)}
	missingStatus := 0
	unknownValues := map[string]bool{}
	for _, item := range items {
		out[projectSummaryKey(item.Status)]++
		if item.BoardStatus == "" {
			if !strings.EqualFold(item.State, "closed") && !strings.EqualFold(item.State, "merged") {
				missingStatus++
			}
			continue
		}
		if item.Status == domain.StatusUnknown && !strings.EqualFold(item.State, "closed") && !strings.EqualFold(item.State, "merged") {
			unknownValues[item.BoardStatus] = true
		}
	}
	var warnings []string
	if missingStatus > 0 {
		warnings = append(warnings, fmt.Sprintf("project status field %q missing on %d items; counted as board_unknown", statusField, missingStatus))
	}
	if len(unknownValues) > 0 {
		values := mapKeys(unknownValues)
		warnings = append(warnings, fmt.Sprintf("project status field %q has %d unmapped values (%s); counted as board_unknown", statusField, len(values), strings.Join(values, ", ")))
	}
	return out, warnings
}

func loadProjectItemsForSnapshot(ctx context.Context, backend github.Backend, owner string, number int, viewer, statusField string) ([]domain.ProjectItem, error) {
	items, err := backend.ListProjectItems(ctx, owner, number, github.ProjectItemsQuery{Limit: 100})
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i] = normalizeProjectItem(items[i], viewer, statusField)
		if statusField != "" {
			items[i].BoardStatus = items[i].FieldValues[statusField]
		}
	}
	return items, nil
}

func normalizeProjectItem(item domain.ProjectItem, viewer, statusField string) domain.ProjectItem {
	out := item
	out.AssignedToMe = slices.Contains(item.Assignees, viewer)
	if statusField != "" {
		out.BoardStatus = item.FieldValues[statusField]
	}
	out.Status = projectStatus(item, statusField)
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
		if status := statusTierForText(value); status != domain.StatusUnknown {
			return status
		}
	}
	return fallbackProjectStatus(item)
}

func projectStatus(item domain.ProjectItem, statusField string) domain.StatusTier {
	if statusField == "" {
		return genericStatus(item)
	}
	if value := item.FieldValues[statusField]; value != "" {
		return statusTierForText(value)
	}
	return fallbackProjectStatus(item)
}

func fallbackProjectStatus(item domain.ProjectItem) domain.StatusTier {
	switch strings.ToUpper(item.State) {
	case "CLOSED", "MERGED":
		return domain.StatusDone
	default:
		return domain.StatusUnknown
	}
}

func statusTierForText(value string) domain.StatusTier {
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
	default:
		return domain.StatusUnknown
	}
}

func projectSummaryKey(status domain.StatusTier) string {
	switch status {
	case domain.StatusReady:
		return "board_ready"
	case domain.StatusInProgress:
		return "board_in_progress"
	case domain.StatusInReview:
		return "board_in_review"
	case domain.StatusBlocked:
		return "board_blocked"
	case domain.StatusDone:
		return "board_done"
	case domain.StatusBacklog:
		return "board_backlog"
	default:
		return "board_unknown"
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

func mapKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	slices.Sort(out)
	return out
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
		report.Warning = "no GitHub token found; run `ferret init` or `ferret auth login`, or set GITHUB_TOKEN"
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
	report.Warning = doctorScopeHint(report.RepoScope, report.ProjectScope)
	return report, nil
}

func doctorScopeHint(repoScope, projectScope bool) string {
	switch {
	case !repoScope && !projectScope:
		return "token is missing both repo and project scopes; add `repo`, `read:org`, and `project` at https://github.com/settings/tokens"
	case !repoScope:
		return "repo scope not available; add `repo` and `read:org` at https://github.com/settings/tokens"
	case !projectScope:
		return "project scope not available; repo mode will work, but add `project` at https://github.com/settings/tokens for GitHub Projects support"
	default:
		return ""
	}
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
	data := fetchManagerRepoData(ctx, app, owner, repo, since, progress)
	report.Partial = report.Partial || data.partial
	report.Warnings = append(report.Warnings, data.warnings...)
	report.Issues = append(report.Issues, data.issues...)
	report.PRs = append(report.PRs, data.prs...)
	report.ReviewNeeded = append(report.ReviewNeeded, filterReviewNeededPRs(data.prs, data.viewer)...)
	report.Workflows = append(report.Workflows, data.workflows...)
}

type managerRepoData struct {
	issues    []domain.IssueSnapshot
	prs       []domain.PRSnapshot
	workflows []domain.WorkflowSnapshot
	warnings  []string
	partial   bool
	viewer    string
}

func fetchManagerRepoData(ctx context.Context, app app, owner, repo, since string, progress io.Writer) managerRepoData {
	var data managerRepoData
	viewer, err := app.backend.GetViewer(ctx)
	if err == nil {
		data.viewer = viewer
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, maxTopLevelFetchesPerRepo)
	run := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fn()
		}()
	}
	run(func() {
		progressf(progress, "  issues %s/%s", owner, repo)
		issues, err := app.backend.ListRepoIssues(ctx, owner, repo, github.IssueQuery{State: "all", Since: since})
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			data.partial = true
			data.warnings = append(data.warnings, fmt.Sprintf("issues for %s/%s: %v", owner, repo, err))
			return
		}
		data.issues = issues
	})
	run(func() {
		progressf(progress, "  prs %s/%s", owner, repo)
		prs, err := app.backend.ListRepoPRs(ctx, owner, repo, github.PRQuery{State: "all", Since: since})
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			data.partial = true
			data.warnings = append(data.warnings, fmt.Sprintf("prs for %s/%s: %v", owner, repo, err))
			return
		}
		data.prs = prs
	})
	run(func() {
		progressf(progress, "  workflows %s/%s", owner, repo)
		runs, err := app.backend.ListWorkflowRuns(ctx, owner, repo, github.WorkflowQuery{PerPage: 10, Since: since})
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			data.partial = true
			data.warnings = append(data.warnings, fmt.Sprintf("workflow runs for %s/%s: %v", owner, repo, err))
			return
		}
		data.workflows = runs
	})
	wg.Wait()
	return data
}

func filterReviewNeededPRs(prs []domain.PRSnapshot, viewer string) []domain.PRSnapshot {
	if viewer == "" {
		return nil
	}
	var out []domain.PRSnapshot
	for _, pr := range prs {
		if !strings.EqualFold(pr.State, "open") {
			continue
		}
		if containsFold(pr.RequestedReviewers, viewer) {
			out = append(out, pr)
		}
	}
	return out
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
	results := make([]managerRepoData, len(repos))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxReposInFlight)
	for i, repo := range repos {
		i, repo := i, repo
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			progressf(progress, "fetching repo %s/%s", repo.Owner, repo.Name)
			results[i] = fetchManagerRepoData(ctx, app, repo.Owner, repo.Name, since, progress)
		}()
	}
	wg.Wait()
	for _, data := range results {
		report.Partial = report.Partial || data.partial
		report.Warnings = append(report.Warnings, data.warnings...)
		report.Issues = append(report.Issues, data.issues...)
		report.PRs = append(report.PRs, data.prs...)
		report.ReviewNeeded = append(report.ReviewNeeded, filterReviewNeededPRs(data.prs, data.viewer)...)
		report.Workflows = append(report.Workflows, data.workflows...)
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
	data := fetchNextRepoData(ctx, app, owner, repo, since)
	report.Warnings = append(report.Warnings, data.warnings...)
	items := repoSnapshotsToItems(data.issues, data.prs, viewer)
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
	results := make([]nextRepoData, len(repos))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxReposInFlight)
	for i, repo := range repos {
		i, repo := i, repo
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			progressf(progress, "fetching repo %s/%s", repo.Owner, repo.Name)
			results[i] = fetchNextRepoData(ctx, app, repo.Owner, repo.Name, since)
		}()
	}
	wg.Wait()
	for _, data := range results {
		report.Warnings = append(report.Warnings, data.warnings...)
		items = append(items, repoSnapshotsToItems(data.issues, data.prs, viewer)...)
	}
	items = filterItems(items, firstNonEmpty(filter, "open"))
	report.Sections = buildNextSections(items)
	return report, nil
}

type nextRepoData struct {
	issues   []domain.IssueSnapshot
	prs      []domain.PRSnapshot
	warnings []string
}

func fetchNextRepoData(ctx context.Context, app app, owner, repo, since string) nextRepoData {
	var data nextRepoData
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, maxTopLevelFetchesPerRepo)
	run := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fn()
		}()
	}
	run(func() {
		issues, err := app.backend.ListRepoIssues(ctx, owner, repo, github.IssueQuery{State: "all", Since: since})
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			data.warnings = append(data.warnings, fmt.Sprintf("issues for %s/%s: %v", owner, repo, err))
			return
		}
		data.issues = issues
	})
	run(func() {
		prs, err := app.backend.ListRepoPRs(ctx, owner, repo, github.PRQuery{State: "all", Since: since})
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			data.warnings = append(data.warnings, fmt.Sprintf("prs for %s/%s: %v", owner, repo, err))
			return
		}
		data.prs = prs
	})
	wg.Wait()
	return data
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

func syncNextFile(path string, report domain.NextReport, opts *rootOptions) error {
	if path == "" {
		return nil
	}
	root, err := approvedRoot(config.DefaultPlanDir)
	if err != nil {
		return err
	}
	path, err = secureOutputPath(path, root, opts.allowOutsideWorkspace)
	if err != nil {
		return err
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
	path = config.ExpandPath(path)
	if path == "" || filepath.Ext(path) != "" {
		return path
	}
	if strings.ToLower(format) == "json" {
		return path + ".json"
	}
	return path + ".md"
}

func renderToOutput(path string, stdout io.Writer, allowOutside bool, fn func(io.Writer) error) error {
	if path == "" {
		return fn(stdout)
	}
	root, err := approvedRoot(config.DefaultOutputDir)
	if err != nil {
		return err
	}
	path, err = secureOutputPath(path, root, allowOutside)
	if err != nil {
		return err
	}
	var buf strings.Builder
	if err := fn(&buf); err != nil {
		return err
	}
	return fsutil.AtomicWriteFile(path, []byte(buf.String()))
}

func resolveNextFilter(filter string, me bool) (string, error) {
	if !me {
		return filter, nil
	}
	if filter != "" && filter != "assigned-to-me" {
		return "", fmt.Errorf("use either --me or --filter %q", filter)
	}
	return "assigned-to-me", nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

type catchUpBudget struct {
	MaxPRReviewFetches int
	MaxRecoveryFetches int
}

func defaultCatchUpBudget() catchUpBudget {
	return catchUpBudget{
		MaxPRReviewFetches: 20,
		MaxRecoveryFetches: 10,
	}
}

func resolveCatchUpBudget(cfg *config.Config, override int) (catchUpBudget, error) {
	budget := defaultCatchUpBudget()
	switch {
	case override < 0:
		return catchUpBudget{}, fmt.Errorf("invalid --review-budget %d (expected 0 or greater)", override)
	case override > 0:
		budget.MaxPRReviewFetches = override
		return budget, nil
	case cfg != nil && cfg.Defaults.CatchUp.ReviewBudget > 0:
		budget.MaxPRReviewFetches = cfg.Defaults.CatchUp.ReviewBudget
		return budget, nil
	default:
		return budget, nil
	}
}

func resolveCatchUpExpandOrder(cfg *config.Config, override string) (string, error) {
	if override != "" {
		return config.NormalizeCatchUpExpandOrder(override)
	}
	return config.NormalizeCatchUpExpandOrder(cfg.Defaults.CatchUp.ExpandOrder)
}

func prioritizePRsForReviewFetch(prs []domain.PRSnapshot, notifications []domain.NotificationThread, viewer, expandOrder string) []domain.PRSnapshot {
	if len(prs) == 0 {
		return nil
	}
	if expandOrder == "recency" {
		return prioritizePRsByRecency(prs)
	}
	notificationReasonsByPR := map[int][]string{}
	for _, thread := range notifications {
		if thread.Kind != "pull_request" {
			continue
		}
		notificationReasonsByPR[thread.Number] = append(notificationReasonsByPR[thread.Number], thread.Reason)
	}
	prioritized := append([]domain.PRSnapshot(nil), prs...)
	slices.SortStableFunc(prioritized, func(a, b domain.PRSnapshot) int {
		scoreA := reviewFetchPriorityScore(a, notificationReasonsByPR[a.Number], viewer)
		scoreB := reviewFetchPriorityScore(b, notificationReasonsByPR[b.Number], viewer)
		if scoreA != scoreB {
			return scoreB - scoreA
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			if a.UpdatedAt.After(b.UpdatedAt) {
				return -1
			}
			return 1
		}
		return b.Number - a.Number
	})
	return prioritized
}

func prioritizePRsByRecency(prs []domain.PRSnapshot) []domain.PRSnapshot {
	prioritized := append([]domain.PRSnapshot(nil), prs...)
	slices.SortStableFunc(prioritized, func(a, b domain.PRSnapshot) int {
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			if a.UpdatedAt.After(b.UpdatedAt) {
				return -1
			}
			return 1
		}
		return b.Number - a.Number
	})
	return prioritized
}

func reviewFetchPriorityScore(pr domain.PRSnapshot, notificationReasons []string, viewer string) int {
	score := 0
	switch strings.ToLower(pr.State) {
	case "open":
		score += 40
	case "merged", "closed":
		score += 5
	default:
		score += 10
	}
	if !pr.IsDraft {
		score += 5
	}
	if viewer != "" {
		if pr.Author == viewer {
			score += 15
		}
		if contains(pr.Assignees, viewer) {
			score += 20
		}
		if contains(pr.RequestedReviewers, viewer) {
			score += 45
		}
	}
	for _, reason := range notificationReasons {
		switch reason {
		case "review_requested":
			score += 50
		case "mention", "team_mention":
			score += 25
		case "comment", "author", "assign":
			score += 20
		default:
			score += 10
		}
	}
	return score
}

func boundedPRsForReviewFetch(prs []domain.PRSnapshot, max int) ([]domain.PRSnapshot, bool) {
	if max < 0 || len(prs) <= max {
		return prs, false
	}
	if max == 0 {
		return nil, len(prs) > 0
	}
	return prs[:max], true
}

func exactMentionMatch(preview, viewer string) bool {
	if preview == "" || viewer == "" {
		return false
	}
	lowerPreview := strings.ToLower(preview)
	needle := "@" + strings.ToLower(viewer)
	idx := strings.Index(lowerPreview, needle)
	if idx < 0 {
		return false
	}
	beforeOK := idx == 0 || !isMentionRune(rune(lowerPreview[idx-1]))
	afterIdx := idx + len(needle)
	afterOK := afterIdx >= len(lowerPreview) || !isMentionRune(rune(lowerPreview[afterIdx]))
	return beforeOK && afterOK
}

func isMentionRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
}

func collectPRReviewActivityWithBudget(ctx context.Context, app app, owner, repo, since string, prs []domain.PRSnapshot, budget catchUpBudget) ([]domain.ActivityEvent, []string) {
	limitedPRs, truncated := boundedPRsForReviewFetch(prs, budget.MaxPRReviewFetches)
	var warnings []string
	if truncated {
		warnings = append(warnings, reviewExpansionBudgetWarning(owner, repo, len(limitedPRs), len(prs)))
	}
	type reviewResult struct {
		events  []domain.ActivityEvent
		warning string
	}
	results := make([]reviewResult, len(limitedPRs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxPRReviewThreadsInFlight)
	for i, pr := range limitedPRs {
		i, pr := i, pr
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			reviews, err := app.backend.ListPullRequestThreadReviews(ctx, owner, repo, pr.Number)
			if err != nil {
				results[i].warning = fmt.Sprintf("reviews for %s/%s#%d: %v", owner, repo, pr.Number, err)
				return
			}
			for _, review := range reviews {
				if !withinSinceReport(since, &review.OccurredAt) {
					continue
				}
				results[i].events = append(results[i].events, review)
			}
		}()
	}
	wg.Wait()
	var events []domain.ActivityEvent
	for _, result := range results {
		if result.warning != "" {
			warnings = append(warnings, result.warning)
		}
		events = append(events, result.events...)
	}
	return events, warnings
}

func collectCatchUpForRepo(ctx context.Context, app app, owner, repo, since, viewer string, me, closedOnly, openOnly, commentsOnly, reviewOnly bool, expandOrder string, budget catchUpBudget, progress io.Writer) ([]domain.CatchUpEntry, []string) {
	data := fetchCatchUpRepoData(ctx, app, owner, repo, since, progress)
	warnings := append([]string(nil), data.warnings...)
	issues := data.issues
	prs := data.prs
	if len(prs) > 20 {
		progressf(progress, "  warning: %d PRs in scope for %s/%s — base catch-up entries stay factual; expanding review threads scales with PR count and may approach GitHub rate limits", len(prs), owner, repo)
	}
	issueComments := data.issueComments
	reviewComments := data.reviewComments
	notifications := data.notifications
	progressf(progress, "  reviews %s/%s", owner, repo)
	reviewPRs := prioritizePRsForReviewFetch(prs, notifications, viewer, expandOrder)
	reviews, reviewWarnings := collectPRReviewActivityWithBudget(ctx, app, owner, repo, since, reviewPRs, budget)
	warnings = append(warnings, reviewWarnings...)
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
		event, title, url, state, involvement := normalizeCatchUpEvent(event, issues, prs, viewer)
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
		recoverWarnings := recoverNotificationPreviews(ctx, app, owner, repo, since, viewer, index, budget)
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

type catchUpRepoData struct {
	issues         []domain.IssueSnapshot
	prs            []domain.PRSnapshot
	issueComments  []domain.ActivityEvent
	reviewComments []domain.ActivityEvent
	notifications  []domain.NotificationThread
	warnings       []string
}

func fetchCatchUpRepoData(ctx context.Context, app app, owner, repo, since string, progress io.Writer) catchUpRepoData {
	var data catchUpRepoData
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, maxTopLevelFetchesPerRepo)
	run := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fn()
		}()
	}
	run(func() {
		progressf(progress, "  issues %s/%s", owner, repo)
		issues, err := app.backend.ListRepoIssues(ctx, owner, repo, github.IssueQuery{State: "all", Since: since})
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			if github.IsKind(err, github.ErrRateLimited) {
				data.warnings = append(data.warnings, repoRateLimitWarning("issues", owner, repo, "issue snapshots may be incomplete"))
			} else {
				data.warnings = append(data.warnings, fmt.Sprintf("issues for %s/%s: %v", owner, repo, err))
			}
			return
		}
		data.issues = issues
	})
	run(func() {
		progressf(progress, "  prs %s/%s", owner, repo)
		prs, err := app.backend.ListRepoPRs(ctx, owner, repo, github.PRQuery{State: "all", Since: since})
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			if github.IsKind(err, github.ErrRateLimited) {
				data.warnings = append(data.warnings, repoRateLimitWarning("prs", owner, repo, "PR snapshots may be incomplete"))
			} else {
				data.warnings = append(data.warnings, fmt.Sprintf("prs for %s/%s: %v", owner, repo, err))
			}
			return
		}
		data.prs = prs
	})
	run(func() {
		progressf(progress, "  issue comments %s/%s", owner, repo)
		events, err := app.backend.ListIssueCommentActivity(ctx, owner, repo, since)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			if github.IsKind(err, github.ErrRateLimited) {
				data.warnings = append(data.warnings, repoRateLimitWarning("issue comments", owner, repo, "some issue-thread discussion may be omitted"))
			} else {
				data.warnings = append(data.warnings, fmt.Sprintf("issue comments for %s/%s: %v", owner, repo, err))
			}
			return
		}
		data.issueComments = events
	})
	run(func() {
		progressf(progress, "  review comments %s/%s", owner, repo)
		events, err := app.backend.ListPullRequestReviewCommentActivity(ctx, owner, repo, since)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			if github.IsKind(err, github.ErrRateLimited) {
				data.warnings = append(data.warnings, repoRateLimitWarning("review comments", owner, repo, "some PR-thread discussion may be omitted"))
			} else {
				data.warnings = append(data.warnings, fmt.Sprintf("review comments for %s/%s: %v", owner, repo, err))
			}
			return
		}
		data.reviewComments = events
	})
	run(func() {
		progressf(progress, "  notifications %s/%s", owner, repo)
		threads, err := app.backend.ListRepoNotifications(ctx, owner, repo, since)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			if github.IsKind(err, github.ErrRateLimited) {
				data.warnings = append(data.warnings, repoRateLimitWarning("notifications", owner, repo, "notification-driven involvement may be incomplete"))
			} else {
				data.warnings = append(data.warnings, fmt.Sprintf("notifications for %s/%s: %v", owner, repo, err))
			}
			return
		}
		data.notifications = threads
	})
	wg.Wait()
	return data
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
	data := fetchActivityRepoData(ctx, app, owner, repo, since, progress)
	warnings := append([]string(nil), data.warnings...)
	issues := data.issues
	prs := data.prs
	issueComments := data.issueComments
	reviewComments := data.reviewComments
	progressf(progress, "  reviews %s/%s", owner, repo)
	reviews, reviewWarnings := collectPRReviewActivityWithBudget(ctx, app, owner, repo, since, prs, catchUpBudget{MaxPRReviewFetches: -1})
	warnings = append(warnings, reviewWarnings...)
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
		event, title, url, state, ok := normalizeActivityEvent(event, issues, prs)
		if ok {
			entries = append(entries, activityEntryFromEvent(event, title, url, state))
		}
	}
	return entries, warnings
}

type activityRepoData struct {
	issues         []domain.IssueSnapshot
	prs            []domain.PRSnapshot
	issueComments  []domain.ActivityEvent
	reviewComments []domain.ActivityEvent
	warnings       []string
}

func fetchActivityRepoData(ctx context.Context, app app, owner, repo, since string, progress io.Writer) activityRepoData {
	var data activityRepoData
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, maxTopLevelFetchesPerRepo)
	run := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fn()
		}()
	}
	run(func() {
		progressf(progress, "  issues %s/%s", owner, repo)
		issues, err := app.backend.ListRepoIssues(ctx, owner, repo, github.IssueQuery{State: "all", Since: since})
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			data.warnings = append(data.warnings, fmt.Sprintf("issues for %s/%s: %v", owner, repo, err))
			return
		}
		data.issues = issues
	})
	run(func() {
		progressf(progress, "  prs %s/%s", owner, repo)
		prs, err := app.backend.ListRepoPRs(ctx, owner, repo, github.PRQuery{State: "all", Since: since})
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			data.warnings = append(data.warnings, fmt.Sprintf("prs for %s/%s: %v", owner, repo, err))
			return
		}
		data.prs = prs
	})
	run(func() {
		progressf(progress, "  issue comments %s/%s", owner, repo)
		events, err := app.backend.ListIssueCommentActivity(ctx, owner, repo, since)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			data.warnings = append(data.warnings, fmt.Sprintf("issue comments for %s/%s: %v", owner, repo, err))
			return
		}
		data.issueComments = events
	})
	run(func() {
		progressf(progress, "  review comments %s/%s", owner, repo)
		events, err := app.backend.ListPullRequestReviewCommentActivity(ctx, owner, repo, since)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			data.warnings = append(data.warnings, fmt.Sprintf("review comments for %s/%s: %v", owner, repo, err))
			return
		}
		data.reviewComments = events
	})
	wg.Wait()
	return data
}

func buildAllReposCatchUpReport(ctx context.Context, app app, cfg *config.Config, since, viewer string, me, closedOnly, openOnly, commentsOnly, reviewOnly bool, expandOrder string, budget catchUpBudget, progress io.Writer) (domain.CatchUpReport, error) {
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
	type repoResult struct {
		entries  []domain.CatchUpEntry
		warnings []string
	}
	results := make([]repoResult, len(repos))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxReposInFlight)
	for i, repo := range repos {
		i, repo := i, repo
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			progressf(progress, "fetching repo %s/%s", repo.Owner, repo.Name)
			entries, warnings := collectCatchUpForRepo(ctx, app, repo.Owner, repo.Name, since, viewer, me, closedOnly, openOnly, commentsOnly, reviewOnly, expandOrder, budget, progress)
			results[i] = repoResult{entries: entries, warnings: warnings}
		}()
	}
	wg.Wait()
	for _, result := range results {
		report.Entries = append(report.Entries, result.entries...)
		if len(result.warnings) > 0 {
			report.Partial = true
			report.Warnings = append(report.Warnings, result.warnings...)
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
	type repoResult struct {
		entries  []domain.ActivityEntry
		warnings []string
	}
	results := make([]repoResult, len(repos))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxReposInFlight)
	for i, repo := range repos {
		i, repo := i, repo
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			progressf(progress, "fetching repo %s/%s", repo.Owner, repo.Name)
			entries, warnings := collectActivityForRepo(ctx, app, repo.Owner, repo.Name, since, progress)
			results[i] = repoResult{entries: entries, warnings: warnings}
		}()
	}
	wg.Wait()
	for _, result := range results {
		report.Entries = append(report.Entries, result.entries...)
		if len(result.warnings) > 0 {
			report.Partial = true
			report.Warnings = append(report.Warnings, result.warnings...)
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

func resolveLinkedRepos(cfg *config.Config, aliases []string) ([]config.RepoWatch, []string) {
	repos := make([]config.RepoWatch, 0, len(aliases))
	var warnings []string
	for _, alias := range aliases {
		repo, err := config.ResolveRepo(cfg, alias)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		repos = append(repos, *repo)
	}
	return repos, warnings
}

func activityEntryFromEvent(event domain.ActivityEvent, title, url, state string) domain.ActivityEntry {
	return domain.ActivityEntry{
		Repo: event.Repo, Owner: event.Owner, Number: event.Number, Kind: event.Kind, Title: title, URL: url,
		State: state, EventType: event.EventType, Actor: event.Actor, OccurredAt: event.OccurredAt, Preview: event.Preview, Path: event.Path,
	}
}

func normalizeCatchUpEvent(event domain.ActivityEvent, issues []domain.IssueSnapshot, prs []domain.PRSnapshot, viewer string) (domain.ActivityEvent, string, string, string, []string) {
	if pr, ok := findPR(prs, event.Number); ok {
		event.Kind = "pull_request"
		return event, pr.Title, pr.URL, pr.State, mergeStrings(involvementForPR(pr, viewer), involvementForActor(event.Actor, viewer, event.EventType))
	}
	if issue, ok := findIssue(issues, event.Number); ok {
		event.Kind = "issue"
		return event, issue.Title, issue.URL, issue.State, mergeStrings(involvementForIssue(issue, viewer), involvementForActor(event.Actor, viewer, event.EventType))
	}
	return event, "", "", "", involvementForActor(event.Actor, viewer, event.EventType)
}

func normalizeActivityEvent(event domain.ActivityEvent, issues []domain.IssueSnapshot, prs []domain.PRSnapshot) (domain.ActivityEvent, string, string, string, bool) {
	if pr, ok := findPR(prs, event.Number); ok {
		event.Kind = "pull_request"
		return event, pr.Title, pr.URL, pr.State, true
	}
	if issue, ok := findIssue(issues, event.Number); ok {
		event.Kind = "issue"
		return event, issue.Title, issue.URL, issue.State, true
	}
	return event, "", "", "", false
}

func repoRateLimitWarning(subject, owner, repo, omission string) string {
	return fmt.Sprintf("%s for %s/%s: rate limited; base report stays factual, but %s", subject, owner, repo, omission)
}

func itemRateLimitWarning(subject, owner, repo string, number int, omission string) string {
	return fmt.Sprintf("%s for %s/%s#%d: rate limited; item summary stays factual, but %s", subject, owner, repo, number, omission)
}

func reviewExpansionBudgetWarning(owner, repo string, expanded, total int) string {
	return fmt.Sprintf("review expansion truncated for %s/%s: base catch-up entries remain factual; expanded review threads for %d of %d PRs", owner, repo, expanded, total)
}

func previewRecoveryBudgetWarning(owner, repo string, fetches int) string {
	return fmt.Sprintf("preview recovery truncated for %s/%s after %d fetches: entries remain factual, but some notification-only previews could not be recovered", owner, repo, fetches)
}

func finalizeCatchUpDiagnostics(report *domain.CatchUpReport, includeDetails bool) {
	if len(report.Warnings) == 0 {
		return
	}
	report.DiagnosticsSummary = summarizeCatchUpDiagnostics(report.Warnings)
	if !includeDetails {
		report.Warnings = nil
	}
}

func summarizeCatchUpDiagnostics(warnings []string) []string {
	var out []string
	seen := map[string]struct{}{}
	appendSummary := func(value string) {
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	for _, warning := range warnings {
		switch {
		case strings.HasPrefix(warning, "review expansion truncated for "):
			appendSummary("review expansion truncated")
		case strings.HasPrefix(warning, "preview recovery truncated for "):
			appendSummary("preview recovery truncated")
		case strings.Contains(warning, ": rate limited;"):
			subject, _, _ := strings.Cut(warning, " for ")
			appendSummary(subject + " rate-limited")
		default:
			summary, _, _ := strings.Cut(warning, ":")
			appendSummary(summary)
		}
	}
	return out
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

func recoverNotificationPreviews(ctx context.Context, app app, owner, repo, since, viewer string, index map[string]*domain.CatchUpEntry, budget catchUpBudget) []string {
	var warnings []string
	fetches := 0
	truncated := false
	for _, entry := range index {
		if fetches >= budget.MaxRecoveryFetches {
			truncated = true
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
			if fetches+2 > budget.MaxRecoveryFetches {
				truncated = true
				break
			}
			comments, cerr := app.backend.ListPullRequestThreadComments(ctx, owner, repo, entry.Number)
			reviews, rerr := app.backend.ListPullRequestThreadReviews(ctx, owner, repo, entry.Number)
			fetches += 2
			if cerr != nil {
				warnings = append(warnings, fmt.Sprintf("recover pr comments for %s/%s#%d: %v", owner, repo, entry.Number, cerr))
			}
			if rerr != nil {
				warnings = append(warnings, fmt.Sprintf("recover pr reviews for %s/%s#%d: %v", owner, repo, entry.Number, rerr))
			}
			events = append(events, comments...)
			events = append(events, reviews...)
		default:
			if fetches+1 > budget.MaxRecoveryFetches {
				truncated = true
				break
			}
			events, err = app.backend.ListIssueThreadComments(ctx, owner, repo, entry.Number)
			fetches++
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("recover issue comments for %s/%s#%d: %v", owner, repo, entry.Number, err))
				continue
			}
		}
		_ = applyRecoveredPreview(entry, events, viewer, since)
	}
	if truncated {
		warnings = append(warnings, previewRecoveryBudgetWarning(owner, repo, fetches))
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
	if best.Preview == "" {
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
		return exactMentionMatch(event.Preview, viewer)
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
		issueComments, icerr := app.backend.ListIssueThreadComments(ctx, iw.Owner, iw.Repo, iw.Number)
		if icerr != nil {
			if github.IsKind(icerr, github.ErrRateLimited) {
				warnings = append(warnings, itemRateLimitWarning("pr issue comments", iw.Owner, iw.Repo, iw.Number, "some issue-thread discussion may be omitted"))
			} else {
				warnings = append(warnings, fmt.Sprintf("pr issue comments for %s/%s#%d: %v", iw.Owner, iw.Repo, iw.Number, icerr))
			}
		}
		comments, cerr := app.backend.ListPullRequestThreadComments(ctx, iw.Owner, iw.Repo, iw.Number)
		if cerr != nil {
			if github.IsKind(cerr, github.ErrRateLimited) {
				warnings = append(warnings, itemRateLimitWarning("pr comments", iw.Owner, iw.Repo, iw.Number, "some PR-thread discussion may be omitted"))
			} else {
				warnings = append(warnings, fmt.Sprintf("pr comments for %s/%s#%d: %v", iw.Owner, iw.Repo, iw.Number, cerr))
			}
		}
		reviews, rerr := app.backend.ListPullRequestThreadReviews(ctx, iw.Owner, iw.Repo, iw.Number)
		if rerr != nil {
			if github.IsKind(rerr, github.ErrRateLimited) {
				warnings = append(warnings, itemRateLimitWarning("pr reviews", iw.Owner, iw.Repo, iw.Number, "some review-thread activity may be omitted"))
			} else {
				warnings = append(warnings, fmt.Sprintf("pr reviews for %s/%s#%d: %v", iw.Owner, iw.Repo, iw.Number, rerr))
			}
		}
		events = append(events, issueComments...)
		events = append(events, comments...)
		events = append(events, reviews...)

		// Also fetch current PR state directly.
		pr, perr := app.backend.GetPullRequest(ctx, iw.Owner, iw.Repo, iw.Number)
		if perr != nil {
			if github.IsKind(perr, github.ErrRateLimited) {
				warnings = append(warnings, itemRateLimitWarning("pr state", iw.Owner, iw.Repo, iw.Number, "current PR state may be stale"))
			} else {
				warnings = append(warnings, fmt.Sprintf("pr state for %s/%s#%d: %v", iw.Owner, iw.Repo, iw.Number, perr))
			}
		} else {
			entry.Title = pr.Title
			entry.URL = pr.URL
			entry.State = pr.State
			if pr.MergedAt != nil && withinSinceReport(since, pr.MergedAt) {
				mergedBy := pr.MergedBy
				if mergedBy == "" {
					mergedBy = pr.Author
				}
				events = append(events, domain.ActivityEvent{
					Repo: iw.Repo, Owner: iw.Owner, Number: iw.Number, Kind: "pull_request",
					EventType: "merged", Actor: mergedBy, OccurredAt: *pr.MergedAt,
				})
			} else if pr.ClosedAt != nil && withinSinceReport(since, pr.ClosedAt) {
				events = append(events, domain.ActivityEvent{
					Repo: iw.Repo, Owner: iw.Owner, Number: iw.Number, Kind: "pull_request",
					EventType: "closed", Actor: pr.Author, OccurredAt: *pr.ClosedAt,
				})
			}
		}
	default: // "issue"
		comments, cerr := app.backend.ListIssueThreadComments(ctx, iw.Owner, iw.Repo, iw.Number)
		if cerr != nil {
			if github.IsKind(cerr, github.ErrRateLimited) {
				warnings = append(warnings, itemRateLimitWarning("issue comments", iw.Owner, iw.Repo, iw.Number, "some issue-thread discussion may be omitted"))
			} else {
				warnings = append(warnings, fmt.Sprintf("issue comments for %s/%s#%d: %v", iw.Owner, iw.Repo, iw.Number, cerr))
			}
		}
		events = comments

		// Also fetch current issue state directly.
		issue, ierr := app.backend.GetIssue(ctx, iw.Owner, iw.Repo, iw.Number)
		if ierr != nil {
			if github.IsKind(ierr, github.ErrRateLimited) {
				warnings = append(warnings, itemRateLimitWarning("issue state", iw.Owner, iw.Repo, iw.Number, "current issue state may be stale"))
			} else {
				warnings = append(warnings, fmt.Sprintf("issue state for %s/%s#%d: %v", iw.Owner, iw.Repo, iw.Number, ierr))
			}
		} else {
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

func filterWatchedItemsAgainstCatchUpEntries(items []domain.WatchedItemEntry, entries []domain.CatchUpEntry) []domain.WatchedItemEntry {
	covered := map[string]bool{}
	for _, entry := range entries {
		covered[watchItemKey(entry.Owner, entry.Repo, entry.Number, entry.Kind)] = true
	}
	return filterWatchedItemsByCoverage(items, covered)
}

func filterWatchedItemsAgainstActivityEntries(items []domain.WatchedItemEntry, entries []domain.ActivityEntry) []domain.WatchedItemEntry {
	covered := map[string]bool{}
	for _, entry := range entries {
		covered[watchItemKey(entry.Owner, entry.Repo, entry.Number, entry.Kind)] = true
	}
	return filterWatchedItemsByCoverage(items, covered)
}

func filterWatchedItemsByCoverage(items []domain.WatchedItemEntry, covered map[string]bool) []domain.WatchedItemEntry {
	filtered := make([]domain.WatchedItemEntry, 0, len(items))
	for _, item := range items {
		if covered[watchItemKey(item.Owner, item.Repo, item.Number, item.Kind)] {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func watchItemKey(owner, repo string, number int, kind string) string {
	return fmt.Sprintf("%s/%s#%d:%s", owner, repo, number, normalizeWatchKind(kind))
}

func normalizeWatchKind(kind string) string {
	if kind == "pr" {
		return "pull_request"
	}
	return kind
}
