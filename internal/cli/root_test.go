package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrian1-dot/ferret/internal/config"
	"github.com/adrian1-dot/ferret/internal/domain"
	"github.com/adrian1-dot/ferret/internal/github"
)

type fakeBackend struct {
	viewer                     string
	issues                     []domain.IssueSnapshot
	prs                        []domain.PRSnapshot
	workflowRuns               []domain.WorkflowSnapshot
	issueComments              []domain.ActivityEvent
	reviewComments             []domain.ActivityEvent
	threadIssueCommentsByItem  map[int][]domain.ActivityEvent
	threadReviewCommentsByPR   map[int][]domain.ActivityEvent
	threadReviewsByPR          map[int][]domain.ActivityEvent
	repoNotifications          []domain.NotificationThread
	listRepoPRsCalls           int
	listPullRequestReviewCalls int
}

func (f *fakeBackend) GetViewer(context.Context) (string, error) {
	return f.viewer, nil
}

func (f *fakeBackend) GetRepo(context.Context, string, string) (domain.RepoSnapshot, error) {
	return domain.RepoSnapshot{}, fmt.Errorf("not implemented")
}

func (f *fakeBackend) GetIssue(_ context.Context, _ string, _ string, number int) (domain.IssueSnapshot, error) {
	for _, issue := range f.issues {
		if issue.Number == number {
			return issue, nil
		}
	}
	return domain.IssueSnapshot{}, fmt.Errorf("not found")
}

func (f *fakeBackend) GetPullRequest(_ context.Context, _ string, _ string, number int) (domain.PRSnapshot, error) {
	for _, pr := range f.prs {
		if pr.Number == number {
			return pr, nil
		}
	}
	return domain.PRSnapshot{}, fmt.Errorf("not found")
}

func (f *fakeBackend) ListUserRecentRepos(context.Context, int) ([]domain.RepoSnapshot, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeBackend) ListRepoIssues(context.Context, string, string, github.IssueQuery) ([]domain.IssueSnapshot, error) {
	return append([]domain.IssueSnapshot(nil), f.issues...), nil
}

func (f *fakeBackend) ListRepoPRs(context.Context, string, string, github.PRQuery) ([]domain.PRSnapshot, error) {
	f.listRepoPRsCalls++
	return append([]domain.PRSnapshot(nil), f.prs...), nil
}

func (f *fakeBackend) ListWorkflowRuns(context.Context, string, string, github.WorkflowQuery) ([]domain.WorkflowSnapshot, error) {
	return append([]domain.WorkflowSnapshot(nil), f.workflowRuns...), nil
}

func (f *fakeBackend) ListIssueCommentActivity(context.Context, string, string, string) ([]domain.ActivityEvent, error) {
	return append([]domain.ActivityEvent(nil), f.issueComments...), nil
}

func (f *fakeBackend) ListPullRequestReviewCommentActivity(context.Context, string, string, string) ([]domain.ActivityEvent, error) {
	return append([]domain.ActivityEvent(nil), f.reviewComments...), nil
}

func (f *fakeBackend) ListPullRequestReviewActivity(context.Context, string, string, string) ([]domain.ActivityEvent, error) {
	f.listPullRequestReviewCalls++
	return nil, fmt.Errorf("legacy review activity path should not be used")
}

func (f *fakeBackend) ListRepoNotifications(context.Context, string, string, string) ([]domain.NotificationThread, error) {
	return append([]domain.NotificationThread(nil), f.repoNotifications...), nil
}

func (f *fakeBackend) ListIssueThreadComments(_ context.Context, _ string, _ string, number int) ([]domain.ActivityEvent, error) {
	return append([]domain.ActivityEvent(nil), f.threadIssueCommentsByItem[number]...), nil
}

func (f *fakeBackend) ListPullRequestThreadComments(_ context.Context, _ string, _ string, number int) ([]domain.ActivityEvent, error) {
	return append([]domain.ActivityEvent(nil), f.threadReviewCommentsByPR[number]...), nil
}

func (f *fakeBackend) ListPullRequestThreadReviews(_ context.Context, _ string, _ string, number int) ([]domain.ActivityEvent, error) {
	return append([]domain.ActivityEvent(nil), f.threadReviewsByPR[number]...), nil
}

func (f *fakeBackend) GetProjectSchema(context.Context, string, int) (domain.ProjectSchema, error) {
	return domain.ProjectSchema{}, fmt.Errorf("not implemented")
}

func (f *fakeBackend) ListProjectItems(context.Context, string, int, github.ProjectItemsQuery) ([]domain.ProjectItem, error) {
	return nil, fmt.Errorf("not implemented")
}

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatal(err)
		}
	})
}

func TestResolveSinceDuration(t *testing.T) {
	t.Parallel()
	got, err := resolveSince("24h")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Fatalf("expected RFC3339 timestamp, got %q", got)
	}
}

func TestFilterItemsAssignedAndActionable(t *testing.T) {
	t.Parallel()
	items := []domain.ProjectItem{
		{Number: 1, AssignedToMe: true, Status: domain.StatusReady, State: "OPEN"},
		{Number: 2, AssignedToMe: false, Status: domain.StatusUnknown, State: "OPEN"},
		{Number: 3, AssignedToMe: false, Status: domain.StatusBlocked, Blocked: true, State: "OPEN"},
	}
	gotAssigned := filterItems(items, "assigned-to-me")
	if len(gotAssigned) != 1 || gotAssigned[0].Number != 1 {
		t.Fatalf("unexpected assigned filter result: %#v", gotAssigned)
	}
	gotActionable := filterItems(items, "actionable")
	if len(gotActionable) != 1 || gotActionable[0].Number != 1 {
		t.Fatalf("unexpected actionable filter result: %#v", gotActionable)
	}
}

func TestBuildNextSectionsSeparatesAssignedAndOpenWork(t *testing.T) {
	t.Parallel()
	items := []domain.ProjectItem{
		{Number: 1, Title: "Mine", AssignedToMe: true, Assignees: []string{"alice"}, Status: domain.StatusUnknown, State: "OPEN"},
		{Number: 2, Title: "Theirs", AssignedToMe: false, Assignees: []string{"bob"}, Status: domain.StatusUnknown, State: "OPEN"},
		{Number: 3, Title: "Open", AssignedToMe: false, Status: domain.StatusUnknown, State: "OPEN"},
	}
	sections := buildNextSections(items)
	if len(sections) != 2 {
		t.Fatalf("unexpected sections: %#v", sections)
	}
	if sections[0].Name != "Assigned to Me" || len(sections[0].Items) != 1 || sections[0].Items[0].Number != 1 {
		t.Fatalf("unexpected first section: %#v", sections[0])
	}
	if sections[1].Name != "Open" || len(sections[1].Items) != 1 || sections[1].Items[0].Number != 3 {
		t.Fatalf("unexpected second section: %#v", sections[1])
	}
}

func TestBuildNextSectionsOnlyShowsReviewWhenAssignedToMe(t *testing.T) {
	t.Parallel()
	items := []domain.ProjectItem{
		{Number: 1, Title: "Their review", AssignedToMe: false, Status: domain.StatusInReview, State: "OPEN"},
		{Number: 2, Title: "My review", AssignedToMe: true, Status: domain.StatusInReview, State: "OPEN"},
	}
	sections := buildNextSections(items)
	if len(sections) != 1 {
		t.Fatalf("unexpected sections: %#v", sections)
	}
	if sections[0].Name != "Review Needed" || len(sections[0].Items) != 1 || sections[0].Items[0].Number != 2 {
		t.Fatalf("unexpected review section: %#v", sections[0])
	}
}

func TestNormalizeProjectItemUsesConfiguredStatusField(t *testing.T) {
	t.Parallel()
	item := normalizeProjectItem(domain.ProjectItem{
		Number: 12,
		State:  "OPEN",
		FieldValues: map[string]string{
			"Status":   "Todo",
			"Priority": "High",
			"Review":   "Needs Review",
		},
	}, "alice", "Status")
	if item.Status != domain.StatusReady {
		t.Fatalf("expected configured board status to win, got %s", item.Status)
	}
	if item.BoardStatus != "Todo" {
		t.Fatalf("expected board status to be preserved, got %q", item.BoardStatus)
	}
}

func TestSummarizeProjectItemsUsesBoardScopedKeysAndWarnings(t *testing.T) {
	t.Parallel()
	items := []domain.ProjectItem{
		{Number: 1, State: "OPEN", Status: domain.StatusReady, BoardStatus: "Todo"},
		{Number: 2, State: "OPEN", Status: domain.StatusUnknown, BoardStatus: ""},
		{Number: 3, State: "OPEN", Status: domain.StatusUnknown, BoardStatus: "Needs Triage"},
	}
	summary, warnings := summarizeProjectItems(items, "Status")
	if summary["total"] != 3 || summary["board_ready"] != 1 || summary["board_unknown"] != 2 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if len(warnings) != 2 {
		t.Fatalf("expected missing and unmapped warnings, got %#v", warnings)
	}
	if !strings.Contains(warnings[0], `missing on 1 items`) || !strings.Contains(warnings[1], "Needs Triage") {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
}

func TestResolveNextFilterSupportsMeAlias(t *testing.T) {
	t.Parallel()
	got, err := resolveNextFilter("", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "assigned-to-me" {
		t.Fatalf("unexpected filter: %q", got)
	}
	if _, err := resolveNextFilter("actionable", true); err == nil {
		t.Fatal("expected conflict error when --me and --filter actionable are combined")
	}
}

func TestSplitItemRefParsesOwnerRepoNumber(t *testing.T) {
	t.Parallel()
	owner, repo, number, err := splitItemRef("acme/api#42")
	if err != nil {
		t.Fatal(err)
	}
	if owner != "acme" || repo != "api" || number != 42 {
		t.Fatalf("unexpected item ref parse: %s %s %d", owner, repo, number)
	}
}

func TestResolveItemRefSupportsAliasAndKindCheck(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Watch.Items = append(cfg.Watch.Items, config.ItemWatch{
		Alias: "review-42", Owner: "acme", Repo: "api", Number: 42, Kind: "pr",
	})
	owner, repo, number, err := resolveItemRef(cfg, "review-42", "pr")
	if err != nil {
		t.Fatal(err)
	}
	if owner != "acme" || repo != "api" || number != 42 {
		t.Fatalf("unexpected resolved item ref: %s %s %d", owner, repo, number)
	}
	if _, _, _, err := resolveItemRef(cfg, "review-42", "issue"); err == nil {
		t.Fatal("expected kind mismatch error")
	}
}

func TestBoundedPRsForReviewFetchTruncatesAtBudget(t *testing.T) {
	t.Parallel()
	prs := []domain.PRSnapshot{{Number: 1}, {Number: 2}, {Number: 3}}
	got, truncated := boundedPRsForReviewFetch(prs, 2)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if len(got) != 2 || got[0].Number != 1 || got[1].Number != 2 {
		t.Fatalf("unexpected bounded PRs: %#v", got)
	}
}

func TestPrioritizePRsForReviewFetchPrefersNotificationAndViewerSignals(t *testing.T) {
	t.Parallel()
	prs := []domain.PRSnapshot{
		{
			Number:             1,
			State:              "OPEN",
			UpdatedAt:          time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
			RequestedReviewers: []string{"someone-else"},
		},
		{
			Number:             2,
			State:              "OPEN",
			UpdatedAt:          time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
			RequestedReviewers: []string{"adrian1-dot"},
		},
		{
			Number:    3,
			State:     "OPEN",
			UpdatedAt: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		},
	}
	notifications := []domain.NotificationThread{
		{Number: 3, Kind: "pull_request", Reason: "mention"},
	}

	got := prioritizePRsForReviewFetch(prs, notifications, "adrian1-dot")
	if len(got) != 3 {
		t.Fatalf("unexpected prioritized PR list: %#v", got)
	}
	if got[0].Number != 2 {
		t.Fatalf("expected directly review-requested PR first, got %#v", got)
	}
	if got[1].Number != 3 {
		t.Fatalf("expected mentioned PR second, got %#v", got)
	}
	if got[2].Number != 1 {
		t.Fatalf("expected plain recency-only PR last, got %#v", got)
	}
}

func TestExactMentionMatchUsesBoundaries(t *testing.T) {
	t.Parallel()
	if !exactMentionMatch("@adrian1-dot please review", "adrian1-dot") {
		t.Fatal("expected direct mention to match")
	}
	if exactMentionMatch("@adrian1-dot-extra please review", "adrian1-dot") {
		t.Fatal("expected partial username mention not to match")
	}
}

func TestRepoSnapshotsToItemsLeavesOpenIssuesUnknown(t *testing.T) {
	t.Parallel()
	items := repoSnapshotsToItems([]domain.IssueSnapshot{
		{Owner: "acme", Repo: "api", Number: 1, Title: "Open issue", State: "OPEN"},
	}, nil, "alice")
	if len(items) != 1 {
		t.Fatalf("unexpected items: %#v", items)
	}
	if items[0].Status != domain.StatusUnknown {
		t.Fatalf("expected open repo issue to remain unknown, got %s", items[0].Status)
	}
}

func TestMergeStringsAndActorInvolvement(t *testing.T) {
	t.Parallel()
	got := mergeStrings([]string{"author"}, involvementForActor("alice", "alice", "reviewed"))
	if len(got) != 2 {
		t.Fatalf("expected merged involvement markers, got %#v", got)
	}
	if involvement := involvementForActor("bob", "alice", "commented"); len(involvement) != 0 {
		t.Fatalf("expected no involvement for non-viewer actor, got %#v", involvement)
	}
}

func TestNotificationReasonMapping(t *testing.T) {
	t.Parallel()
	if got := involvementForNotification("mention"); len(got) != 1 || got[0] != "mentioned" {
		t.Fatalf("unexpected mention involvement: %#v", got)
	}
	if event := notificationReasonToEvent("review_requested"); event != "review_requested" {
		t.Fatalf("unexpected notification event: %s", event)
	}
}

func TestApplyRecoveredPreviewForMention(t *testing.T) {
	t.Parallel()
	entry := &domain.CatchUpEntry{
		NotificationReasons: []string{"mention"},
		Preview:             "thread matched notification reason: mention",
	}
	events := []domain.ActivityEvent{
		{
			Actor:      "alice",
			OccurredAt: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
			Preview:    "@adrian1-dot please look at this",
			EventType:  "commented",
			Kind:       "issue",
		},
	}
	if !applyRecoveredPreview(entry, events, "adrian1-dot", "2026-03-20T00:00:00Z") {
		t.Fatal("expected preview recovery to succeed")
	}
	if entry.LatestActor != "alice" || entry.LatestEvent != "mentioned" {
		t.Fatalf("unexpected recovered entry: %#v", entry)
	}
}

func TestApplyRecoveredPreviewFallsBackWhenNoMentionFound(t *testing.T) {
	t.Parallel()
	entry := &domain.CatchUpEntry{
		NotificationReasons: []string{"mention"},
		Preview:             "thread matched notification reason: mention",
	}
	events := []domain.ActivityEvent{
		{
			Actor:      "alice",
			OccurredAt: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
			Preview:    "please look at this",
			EventType:  "commented",
			Kind:       "issue",
		},
	}
	if applyRecoveredPreview(entry, events, "adrian1-dot", "2026-03-20T00:00:00Z") {
		t.Fatal("expected preview recovery to fail without a direct mention")
	}
	if entry.Preview != "thread matched notification reason: mention" {
		t.Fatalf("expected fallback preview to remain, got %#v", entry.Preview)
	}
}

func TestMatchesCatchUpFiltersUsesAnyRelevantEvent(t *testing.T) {
	t.Parallel()
	entry := &domain.CatchUpEntry{
		LatestEvent:         "commented",
		HasClosure:          true,
		HasDiscussion:       true,
		Involvement:         []string{"mentioned"},
		EventCount:          2,
		LatestActor:         "alice",
		NotificationReasons: []string{"mention"},
	}
	if !matchesCatchUpFilters(entry, false, true, false, false, false) {
		t.Fatal("expected closed-only to match when the item had a closure event")
	}
	if !matchesCatchUpFilters(entry, false, false, false, true, false) {
		t.Fatal("expected comments-only to match when the item had discussion")
	}
	if !matchesCatchUpFilters(entry, true, false, false, false, false) {
		t.Fatal("expected --me to match when involvement exists")
	}
}

func TestAddCatchUpFallbackUpdatesIncludesPlainUpdatedItems(t *testing.T) {
	t.Parallel()
	index := map[string]*domain.CatchUpEntry{}
	since := "2026-03-21T00:00:00Z"
	issues := []domain.IssueSnapshot{
		{
			Owner:     "acme",
			Repo:      "api",
			Number:    55,
			Title:     "Still open",
			URL:       "https://example.com/issues/55",
			State:     "OPEN",
			Author:    "alice",
			Assignees: []string{"alice"},
			UpdatedAt: time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		},
	}
	addCatchUpFallbackUpdates(index, issues, nil, since, "alice")
	entry, ok := index["api#55:issue"]
	if !ok {
		t.Fatal("expected fallback updated entry")
	}
	if entry.LatestEvent != "updated" || entry.LatestActor != "github" {
		t.Fatalf("unexpected fallback entry: %#v", entry)
	}
}

func TestSortCatchUpEntriesOrdersByLatestTime(t *testing.T) {
	t.Parallel()
	entries := []domain.CatchUpEntry{
		{Number: 1, LatestAt: time.Date(2026, 3, 27, 10, 0, 0, 0, time.UTC), Involvement: []string{"author", "commenter"}},
		{Number: 2, LatestAt: time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)},
	}
	sortCatchUpEntries(entries)
	if entries[0].Number != 2 {
		t.Fatalf("expected newest entry first, got %#v", entries)
	}
}

func TestInitCommandCreatesConfig(t *testing.T) {
	dir := t.TempDir()
	withWorkingDir(t, dir)
	configPath := filepath.Join(".ferret", "config.yaml")
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--config", configPath, "init"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected config to be created: %v", err)
	}
	store := config.FileStore{Path: configPath}
	cfg, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 {
		t.Fatalf("unexpected config version: %#v", cfg.Version)
	}
}

func TestSecureConfigPathRejectsPathOutsideFerretRootByDefault(t *testing.T) {
	dir := t.TempDir()
	withWorkingDir(t, dir)
	_, err := secureConfigPath(&rootOptions{configPath: "ferret.yaml"})
	if err == nil {
		t.Fatal("expected config path outside .ferret root to be rejected")
	}
}

func TestValidateTrustedConfigPathsRejectsPlanDirOutsideFerretPlans(t *testing.T) {
	dir := t.TempDir()
	withWorkingDir(t, dir)
	cfg := config.Default()
	cfg.Defaults.PlanDir = ".ferret-other/plans"
	err := validateTrustedConfigPaths(cfg, false)
	if err == nil {
		t.Fatal("expected unsafe plan dir to be rejected")
	}
	if !strings.Contains(err.Error(), "defaults.plan_dir") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScopeContains(t *testing.T) {
	t.Parallel()
	scopes := []string{"repo", "project", "read:org"}
	if !contains(scopes, "repo") {
		t.Fatal("expected repo scope")
	}
	if !contains(scopes, "project") {
		t.Fatal("expected project scope")
	}
	if contains(scopes, "admin") {
		t.Fatal("unexpected admin scope")
	}
}

func TestRenderWatchList(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	if err := config.AddRepoWatch(cfg, config.RepoWatch{Alias: "api", Owner: "acme", Name: "api"}); err != nil {
		t.Fatal(err)
	}
	if err := config.AddProjectWatch(cfg, config.ProjectWatch{Alias: "delivery", Owner: "acme", Number: 12, LinkedRepos: []string{"api"}}); err != nil {
		t.Fatal(err)
	}
	if err := config.AddItemWatch(cfg, config.ItemWatch{Alias: "my-issue", Owner: "acme", Repo: "api", Number: 42, Kind: "issue"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := renderWatchList(&buf, cfg); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Watched Repos:") || !strings.Contains(out, "api -> acme/api") || !strings.Contains(out, "delivery -> acme/12 | linked repos: api") {
		t.Fatalf("unexpected watch list render: %q", out)
	}
	if !strings.Contains(out, "Watched Items:") || !strings.Contains(out, "my-issue -> acme/api#42 (issue)") {
		t.Fatalf("expected watched items in watch list, got %q", out)
	}
}

func TestAllWatchedRepos(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	if _, err := allWatchedRepos(cfg); err == nil {
		t.Fatal("expected error when no repos are watched")
	}
	if err := config.AddRepoWatch(cfg, config.RepoWatch{Alias: "api", Owner: "acme", Name: "api"}); err != nil {
		t.Fatal(err)
	}
	repos, err := allWatchedRepos(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Alias != "api" {
		t.Fatalf("unexpected repos: %#v", repos)
	}
}

func TestResolveStatefulSinceUsesCursorWhenNoExplicitOrConfigDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := config.FileStateStore{Path: filepath.Join(dir, "state.yaml")}
	state := config.DefaultState()
	state.Cursors["catch-up:all"] = "2026-03-28T10:00:00Z"
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	got, usedCursor, err := resolveStatefulSince(context.Background(), store, "catch-up", "all", "", "", "24h")
	if err != nil {
		t.Fatal(err)
	}
	if !usedCursor || got != "2026-03-28T10:00:00Z" {
		t.Fatalf("unexpected stateful since resolution: since=%q usedCursor=%v", got, usedCursor)
	}
}

func TestResolveStatefulSinceDoesNotUseCursorWhenExplicitSinceProvided(t *testing.T) {
	t.Parallel()
	store := config.FileStateStore{Path: filepath.Join(t.TempDir(), "state.yaml")}
	got, usedCursor, err := resolveStatefulSince(context.Background(), store, "catch-up", "all", "24h", "", "24h")
	if err != nil {
		t.Fatal(err)
	}
	if usedCursor {
		t.Fatal("expected explicit --since to bypass cursor")
	}
	if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Fatalf("expected RFC3339 timestamp, got %q", got)
	}
}

func TestAppendRepoDataReusesSinglePRFetchForReviewNeeded(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{
		viewer: "alice",
		prs: []domain.PRSnapshot{
			{Owner: "acme", Repo: "api", Number: 1, State: "OPEN", RequestedReviewers: []string{"alice"}},
			{Owner: "acme", Repo: "api", Number: 2, State: "OPEN", RequestedReviewers: []string{"bob"}},
		},
	}
	application := app{backend: backend}
	report := &domain.ManagerReport{}

	appendRepoData(context.Background(), application, "acme", "api", "", report, io.Discard)

	if backend.listRepoPRsCalls != 1 {
		t.Fatalf("expected one PR fetch, got %d", backend.listRepoPRsCalls)
	}
	if len(report.ReviewNeeded) != 1 || report.ReviewNeeded[0].Number != 1 {
		t.Fatalf("unexpected review-needed PRs: %#v", report.ReviewNeeded)
	}
}

func TestCollectActivityForRepoDoesNotUseLegacyReviewActivityFetch(t *testing.T) {
	t.Parallel()
	reviewedAt := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	backend := &fakeBackend{
		prs: []domain.PRSnapshot{
			{
				Owner:     "acme",
				Repo:      "api",
				Number:    7,
				Title:     "PR",
				URL:       "https://example.com/pr/7",
				State:     "OPEN",
				Author:    "alice",
				CreatedAt: time.Date(2026, 3, 27, 10, 0, 0, 0, time.UTC),
				UpdatedAt: reviewedAt,
			},
		},
		threadReviewsByPR: map[int][]domain.ActivityEvent{
			7: {{
				Repo:       "api",
				Owner:      "acme",
				Number:     7,
				Kind:       "pull_request",
				EventType:  "reviewed",
				Actor:      "bob",
				OccurredAt: reviewedAt,
				Preview:    "looks good",
			}},
		},
	}
	application := app{backend: backend}

	entries, warnings := collectActivityForRepo(context.Background(), application, "acme", "api", "2026-03-20T00:00:00Z", io.Discard)

	if backend.listPullRequestReviewCalls != 0 {
		t.Fatalf("expected legacy review activity path to be unused, got %d calls", backend.listPullRequestReviewCalls)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	foundReview := false
	for _, entry := range entries {
		if entry.Number == 7 && entry.EventType == "reviewed" && entry.Actor == "bob" {
			foundReview = true
			break
		}
	}
	if !foundReview {
		t.Fatalf("expected reviewed event from thread reviews, got %#v", entries)
	}
}

func TestCollectWatchedItemEntryForPRUsesDirectHydrationAndCountsIssueComments(t *testing.T) {
	t.Parallel()
	latest := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	backend := &fakeBackend{
		prs: []domain.PRSnapshot{
			{
				Owner:     "acme",
				Repo:      "api",
				Number:    872,
				Title:     "Direct PR hydration",
				URL:       "https://example.com/pr/872",
				State:     "OPEN",
				Author:    "alice",
				UpdatedAt: latest,
			},
		},
		threadIssueCommentsByItem: map[int][]domain.ActivityEvent{
			872: {{
				Repo:       "api",
				Owner:      "acme",
				Number:     872,
				Kind:       "pull_request",
				EventType:  "commented",
				Actor:      "alice",
				OccurredAt: time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC),
				Preview:    "top-level issue thread comment",
			}},
		},
		threadReviewCommentsByPR: map[int][]domain.ActivityEvent{
			872: {{
				Repo:       "api",
				Owner:      "acme",
				Number:     872,
				Kind:       "pull_request",
				EventType:  "commented",
				Actor:      "bob",
				OccurredAt: time.Date(2026, 3, 28, 11, 0, 0, 0, time.UTC),
				Preview:    "review comment",
			}},
		},
		threadReviewsByPR: map[int][]domain.ActivityEvent{
			872: {{
				Repo:       "api",
				Owner:      "acme",
				Number:     872,
				Kind:       "pull_request",
				EventType:  "reviewed",
				Actor:      "carol",
				OccurredAt: latest,
				Preview:    "approved",
			}},
		},
	}

	entry, warnings := collectWatchedItemEntry(context.Background(), app{backend: backend}, config.ItemWatch{
		Alias: "dolos-pr-872", Owner: "acme", Repo: "api", Number: 872, Kind: "pr",
	}, "2026-03-20T00:00:00Z", io.Discard)

	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	if backend.listRepoPRsCalls != 0 {
		t.Fatalf("expected no repo-wide PR listing for watched PR hydration, got %d calls", backend.listRepoPRsCalls)
	}
	if entry.Title != "Direct PR hydration" || entry.URL != "https://example.com/pr/872" || entry.State != "OPEN" {
		t.Fatalf("unexpected watched PR hydration: %#v", entry)
	}
	if entry.EventCount != 3 {
		t.Fatalf("expected issue comment + review comment + review = 3 events, got %#v", entry)
	}
	if entry.LatestActor != "carol" || entry.LatestEvent != "reviewed" || entry.Preview != "approved" {
		t.Fatalf("unexpected latest watched PR event: %#v", entry)
	}
}

func TestCollectCatchUpForRepoNormalizesPRIssueThreadComments(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{
		viewer: "adrian1-dot",
		prs: []domain.PRSnapshot{{
			Owner:     "acme",
			Repo:      "api",
			Number:    302,
			Title:     "Gateway-owned request structs",
			URL:       "https://example.com/pr/302",
			State:     "OPEN",
			Author:    "adrian1-dot",
			UpdatedAt: time.Date(2026, 4, 1, 11, 41, 23, 0, time.UTC),
		}},
		issueComments: []domain.ActivityEvent{{
			Repo:       "api",
			Owner:      "acme",
			Number:     302,
			Kind:       "issue",
			EventType:  "commented",
			Actor:      "adrian1-dot",
			OccurredAt: time.Date(2026, 4, 1, 11, 41, 1, 0, time.UTC),
			Preview:    "Merging this now",
		}},
	}

	entries, warnings := collectCatchUpForRepo(context.Background(), app{backend: backend}, "acme", "api", "2026-03-20T00:00:00Z", "adrian1-dot", false, false, false, false, false, io.Discard)

	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 catch-up entry, got %#v", entries)
	}
	entry := entries[0]
	if entry.Kind != "pull_request" {
		t.Fatalf("expected PR entry kind, got %#v", entry)
	}
	if entry.Title != "Gateway-owned request structs" || entry.URL != "https://example.com/pr/302" {
		t.Fatalf("expected PR metadata to be preserved, got %#v", entry)
	}
	if entry.LatestEvent != "commented" || entry.LatestActor != "adrian1-dot" {
		t.Fatalf("unexpected latest event: %#v", entry)
	}
}

func TestCollectActivityForRepoNormalizesPRIssueThreadComments(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{
		prs: []domain.PRSnapshot{{
			Owner:     "acme",
			Repo:      "api",
			Number:    302,
			Title:     "Gateway-owned request structs",
			URL:       "https://example.com/pr/302",
			State:     "OPEN",
			Author:    "adrian1-dot",
			UpdatedAt: time.Date(2026, 4, 1, 11, 41, 23, 0, time.UTC),
		}},
		issueComments: []domain.ActivityEvent{{
			Repo:       "api",
			Owner:      "acme",
			Number:     302,
			Kind:       "issue",
			EventType:  "commented",
			Actor:      "adrian1-dot",
			OccurredAt: time.Date(2026, 4, 1, 11, 41, 1, 0, time.UTC),
			Preview:    "Merging this now",
		}},
	}

	entries, warnings := collectActivityForRepo(context.Background(), app{backend: backend}, "acme", "api", "2026-03-20T00:00:00Z", io.Discard)

	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	if len(entries) != 2 {
		t.Fatalf("expected opened+commented PR activity, got %#v", entries)
	}
	foundComment := false
	for _, entry := range entries {
		if entry.EventType != "commented" {
			continue
		}
		foundComment = true
		if entry.Kind != "pull_request" || entry.Title != "Gateway-owned request structs" || entry.URL != "https://example.com/pr/302" {
			t.Fatalf("expected PR comment entry metadata, got %#v", entry)
		}
	}
	if !foundComment {
		t.Fatalf("expected comment entry in activity output, got %#v", entries)
	}
}

func TestFilterWatchedItemsAgainstCatchUpEntriesDropsCoveredItems(t *testing.T) {
	t.Parallel()
	items := []domain.WatchedItemEntry{
		{Alias: "kept", Owner: "acme", Repo: "api", Number: 1, Kind: "issue"},
		{Alias: "dropped", Owner: "acme", Repo: "api", Number: 2, Kind: "pr"},
	}
	entries := []domain.CatchUpEntry{
		{Owner: "acme", Repo: "api", Number: 2, Kind: "pull_request"},
	}
	got := filterWatchedItemsAgainstCatchUpEntries(items, entries)
	if len(got) != 1 || got[0].Alias != "kept" {
		t.Fatalf("unexpected filtered watched items: %#v", got)
	}
}

func TestFilterWatchedItemsAgainstActivityEntriesDropsCoveredItems(t *testing.T) {
	t.Parallel()
	items := []domain.WatchedItemEntry{
		{Alias: "kept", Owner: "acme", Repo: "api", Number: 7, Kind: "pr"},
		{Alias: "dropped", Owner: "acme", Repo: "api", Number: 8, Kind: "issue"},
	}
	entries := []domain.ActivityEntry{
		{Owner: "acme", Repo: "api", Number: 8, Kind: "issue"},
	}
	got := filterWatchedItemsAgainstActivityEntries(items, entries)
	if len(got) != 1 || got[0].Alias != "kept" {
		t.Fatalf("unexpected filtered watched items: %#v", got)
	}
}
