package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrian1-dot/ferret/internal/config"
	"github.com/adrian1-dot/ferret/internal/domain"
)

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
	t.Parallel()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "ferret.yaml")
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
