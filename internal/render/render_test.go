package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/adrian1-dot/ferret/internal/domain"
)

func TestSyncNextMarkdownPreservesNotes(t *testing.T) {
	t.Parallel()
	report := domain.NextReport{
		GeneratedAt: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
		Target:      "delivery",
		TargetKind:  "project",
		Sections: []domain.NextSection{
			{Name: "Assigned", Items: []domain.ProjectItem{{Repo: "api", Number: 12, Title: "Implement", URL: "https://example.com"}}},
		},
	}
	existing := "# delivery\n\nOld stuff\n\n" + GeneratedStart + "\nold\n" + GeneratedEnd + "\n\n## Notes\n\nKeep me.\n"
	got, err := SyncNextMarkdown(existing, report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Keep me.") {
		t.Fatalf("expected notes to be preserved, got %q", got)
	}
	if strings.Contains(got, "\nold\n") {
		t.Fatalf("expected generated block to be replaced, got %q", got)
	}
}

func TestSyncNextMarkdownRefreshesHeader(t *testing.T) {
	t.Parallel()
	report := domain.NextReport{
		GeneratedAt: time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC),
		Target:      "delivery",
		TargetKind:  "project",
		Since:       "2026-03-28T00:00:00Z",
		Sections: []domain.NextSection{
			{Name: "Open", Items: []domain.ProjectItem{{Repo: "api", Number: 12, Title: "Implement", URL: "https://example.com"}}},
		},
	}
	existing := "# delivery\n\nGenerated at: `2026-03-28 12:00:00Z`\n\nSince: `2026-03-27T00:00:00Z`\n\n" +
		GeneratedStart + "\nold\n" + GeneratedEnd + "\n\n## Notes\n\nKeep me.\n"
	got, err := SyncNextMarkdown(existing, report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Generated at: `2026-03-29 12:00:00Z`") {
		t.Fatalf("expected refreshed generated header, got %q", got)
	}
	if !strings.Contains(got, "Since: `2026-03-28T00:00:00Z`") {
		t.Fatalf("expected refreshed since header, got %q", got)
	}
	if !strings.Contains(got, "Keep me.") {
		t.Fatalf("expected notes to be preserved, got %q", got)
	}
}

func TestTextRendererRenderActivity(t *testing.T) {
	t.Parallel()
	report := domain.ActivityReport{
		GeneratedAt: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
		Target:      "repo1",
		TargetKind:  "repo",
		Since:       "2026-03-21T00:00:00Z",
		Entries: []domain.ActivityEntry{
			{Owner: "org", Repo: "api", Number: 12, Title: "Fix auth", Kind: "issue", URL: "https://example.com/api/12", EventType: "commented", Actor: "alice", Preview: "latest note", OccurredAt: time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)},
			{Owner: "org", Repo: "api", Number: 15, Title: "Add login", Kind: "pr", EventType: "merged", Actor: "bob"},
		},
	}
	var buf bytes.Buffer
	if err := (TextRenderer{}).RenderActivity(&buf, report); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// single-repo: no repo section header
	if strings.Contains(out, "org/api") {
		t.Fatalf("expected no repo header for single-repo report, got %q", out)
	}
	if !strings.Contains(out, "Recent Activity:") || !strings.Contains(out, "#12 Fix auth (issue)") || !strings.Contains(out, "url: https://example.com/api/12") {
		t.Fatalf("unexpected activity render: %q", out)
	}
}

func TestTextRendererRenderActivityMultiRepo(t *testing.T) {
	t.Parallel()
	report := domain.ActivityReport{
		GeneratedAt: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
		Target:      "all",
		TargetKind:  "portfolio",
		Since:       "2026-03-21T00:00:00Z",
		Entries: []domain.ActivityEntry{
			{Owner: "org", Repo: "api", Number: 12, Title: "Fix auth", Kind: "issue", URL: "https://example.com/api/12", EventType: "commented", Actor: "alice", OccurredAt: time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)},
			{Owner: "org", Repo: "api", Number: 12, Title: "Fix auth", Kind: "issue", URL: "https://example.com/api/12", EventType: "labeled", Actor: "bob", OccurredAt: time.Date(2026, 3, 28, 11, 0, 0, 0, time.UTC)},
			{Owner: "org", Repo: "db", Number: 8, Title: "Close session", Kind: "pr", EventType: "merged", Actor: "carol"},
		},
	}
	var buf bytes.Buffer
	if err := (TextRenderer{}).RenderActivity(&buf, report); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// multi-repo: repo headers expected
	if !strings.Contains(out, "org/api") || !strings.Contains(out, "org/db") {
		t.Fatalf("expected repo section headers, got %q", out)
	}
	// item #12 should be collapsed: 2 events
	if !strings.Contains(out, "2 events") {
		t.Fatalf("expected collapsed item with event count, got %q", out)
	}
	// latest event on #12 is "labeled by @bob"
	if !strings.Contains(out, "labeled by @bob") {
		t.Fatalf("expected latest event details, got %q", out)
	}
}

func TestTextRendererRenderCatchUpGroupsEntries(t *testing.T) {
	t.Parallel()
	report := domain.CatchUpReport{
		GeneratedAt: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
		Target:      "all-watched-repos",
		TargetKind:  "portfolio",
		Since:       "2026-03-21T00:00:00Z",
		Entries: []domain.CatchUpEntry{
			{Repo: "api", Number: 1, Title: "Mentioned", LatestEvent: "mentioned", LatestActor: "alice"},
			{Repo: "api", Number: 2, Title: "Closed", LatestEvent: "closed", LatestActor: "alice"},
			{Repo: "api", Number: 3, Title: "Commented", LatestEvent: "commented", LatestActor: "alice"},
			{Repo: "api", Number: 4, Title: "Updated", LatestEvent: "updated", LatestActor: "github"},
		},
	}
	var buf bytes.Buffer
	if err := (TextRenderer{}).RenderCatchUp(&buf, report); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Needs Attention (1):") || !strings.Contains(out, "Recently Closed (1):") || !strings.Contains(out, "Recent Discussion (1):") || !strings.Contains(out, "Other Changes (1):") {
		t.Fatalf("unexpected catch-up grouping: %q", out)
	}
}

func TestTextRendererRenderNextShowsSectionCountsAndMetadata(t *testing.T) {
	t.Parallel()
	report := domain.NextReport{
		GeneratedAt: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
		Target:      "delivery",
		TargetKind:  "project",
		Sections: []domain.NextSection{
			{
				Name: "Assigned to Me",
				Items: []domain.ProjectItem{
					{
						Repo: "api", Number: 12, Title: "Implement", Status: domain.StatusInProgress, Priority: domain.PriorityHigh,
						Assignees: []string{"alice"}, AssignedToMe: true,
						URL: "https://example.com/api/12", FieldValues: map[string]string{"Status": "In Progress"},
					},
				},
			},
		},
	}
	var buf bytes.Buffer
	if err := (TextRenderer{}).RenderNext(&buf, report); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Assigned to Me (1):") {
		t.Fatalf("missing section header in next render: %q", out)
	}
	if !strings.Contains(out, "#12 Implement") {
		t.Fatalf("missing item number/title in next render: %q", out)
	}
	if !strings.Contains(out, "@alice") {
		t.Fatalf("missing assignee in next render: %q", out)
	}
	if !strings.Contains(out, "status: In Progress") {
		t.Fatalf("missing raw status in next render: %q", out)
	}
}

func TestAgeString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Minute, "30m ago"},
		{2 * time.Hour, "2h ago"},
		{47 * time.Hour, "47h ago"},
		{48 * time.Hour, "2d ago"},
		{72 * time.Hour, "3d ago"},
	}
	for _, c := range cases {
		got := ageString(c.d)
		if got != c.want {
			t.Errorf("ageString(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestFormatAssignees(t *testing.T) {
	t.Parallel()
	if got := formatAssignees(nil); got != "unassigned" {
		t.Errorf("expected unassigned, got %q", got)
	}
	if got := formatAssignees([]string{"alice", "bob"}); got != "@alice, @bob" {
		t.Errorf("expected @alice, @bob, got %q", got)
	}
}

func TestLabelsText(t *testing.T) {
	t.Parallel()
	if got := labelsText(nil); got != "" {
		t.Errorf("expected empty for nil labels, got %q", got)
	}
	if got := labelsText([]string{"bug", "help-wanted", "good-first-issue", "extra"}); got != "bug, help-wanted, good-first-issue" {
		t.Errorf("expected 3 labels, got %q", got)
	}
}

func TestTextRendererRenderManagerSectionOrder(t *testing.T) {
	t.Parallel()
	now := time.Now()
	report := domain.ManagerReport{
		GeneratedAt: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
		Target:      "repo1",
		TargetKind:  "repo",
		Summary:     map[string]int{"issues": 1, "prs": 1, "review_needed": 1},
		Issues: []domain.IssueSnapshot{
			{Repo: "api", Number: 10, Title: "Fix login", URL: "https://example.com/10", State: "OPEN",
				Assignees: []string{"alice"}, Labels: []string{"bug"}, UpdatedAt: now.Add(-48 * time.Hour)},
		},
		PRs: []domain.PRSnapshot{
			{Repo: "api", Number: 11, Title: "Add sessions", State: "OPEN", UpdatedAt: now.Add(-24 * time.Hour)},
		},
		ReviewNeeded: []domain.PRSnapshot{
			{Repo: "api", Number: 12, Title: "Refactor auth", State: "OPEN", UpdatedAt: now.Add(-2 * time.Hour)},
		},
	}
	var buf bytes.Buffer
	if err := (TextRenderer{}).RenderManager(&buf, report); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	// Summary appears before section headers
	summaryPos := strings.Index(out, "Summary:")
	reviewPos := strings.Index(out, "Review Needed (1):")
	issuesPos := strings.Index(out, "Issues (1):")
	prsPos := strings.Index(out, "PRs (1):")
	if summaryPos < 0 || reviewPos < 0 || issuesPos < 0 || prsPos < 0 {
		t.Fatalf("missing section in output: %q", out)
	}
	if !(summaryPos < reviewPos && reviewPos < issuesPos && issuesPos < prsPos) {
		t.Fatalf("expected section order Summary < Review Needed < Issues < PRs, got positions summary=%d review=%d issues=%d prs=%d\noutput: %q",
			summaryPos, reviewPos, issuesPos, prsPos, out)
	}

	// Assignees shown
	if !strings.Contains(out, "@alice") {
		t.Fatalf("expected @alice in output, got %q", out)
	}
	// Labels shown
	if !strings.Contains(out, "labels: bug") {
		t.Fatalf("expected labels in output, got %q", out)
	}
	// Age shown for issue (48h = 2d ago)
	if !strings.Contains(out, "2d ago") {
		t.Fatalf("expected age '2d ago' in output, got %q", out)
	}
	// Unassigned PR
	if !strings.Contains(out, "unassigned") {
		t.Fatalf("expected 'unassigned' for PR with no assignees, got %q", out)
	}
}

func TestRenderCatchUpWithWatchedItems(t *testing.T) {
	t.Parallel()
	report := domain.CatchUpReport{
		GeneratedAt: time.Date(2026, 3, 31, 8, 0, 0, 0, time.UTC),
		Target:      "my-repo",
		TargetKind:  "repo",
		Since:       "2026-03-30T08:00:00Z",
		WatchedItems: []domain.WatchedItemEntry{
			{Alias: "my-issue", Owner: "acme", Repo: "api", Number: 42, Kind: "issue", Title: "Important bug", URL: "https://github.com/acme/api/issues/42", State: "OPEN", LatestEvent: "commented", LatestActor: "alice", EventCount: 2, LatestAt: time.Date(2026, 3, 31, 7, 0, 0, 0, time.UTC)},
		},
	}
	var buf bytes.Buffer
	if err := (TextRenderer{}).RenderCatchUp(&buf, report); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Watched Items (1):") {
		t.Fatalf("expected Watched Items section, got %q", out)
	}
	if !strings.Contains(out, "[watched]") {
		t.Fatalf("expected [watched] label, got %q", out)
	}
	if !strings.Contains(out, "acme/api#42") {
		t.Fatalf("expected item reference, got %q", out)
	}
}

func TestRenderActivityWithWatchedItems(t *testing.T) {
	t.Parallel()
	report := domain.ActivityReport{
		GeneratedAt: time.Date(2026, 3, 31, 8, 0, 0, 0, time.UTC),
		Target:      "my-repo",
		TargetKind:  "repo",
		Since:       "2026-03-30T08:00:00Z",
		WatchedItems: []domain.WatchedItemEntry{
			{Alias: "my-pr", Owner: "acme", Repo: "api", Number: 55, Kind: "pr", Title: "Fix auth", EventCount: 0},
		},
	}
	var buf bytes.Buffer
	if err := (TextRenderer{}).RenderActivity(&buf, report); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Watched Items (1):") {
		t.Fatalf("expected Watched Items section, got %q", out)
	}
	if !strings.Contains(out, "no activity in this period") {
		t.Fatalf("expected 'no activity in this period' for item with no events, got %q", out)
	}
}

func TestTextRendererRenderManagerStructuredSections(t *testing.T) {
	t.Parallel()
	report := domain.ManagerReport{
		GeneratedAt: time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC),
		Target:      "repo1",
		TargetKind:  "repo",
		Since:       "2026-03-21T00:00:00Z",
		Summary: map[string]int{
			"issues":        1,
			"prs":           1,
			"review_needed": 1,
			"workflow_runs": 1,
		},
		Issues: []domain.IssueSnapshot{
			{Repo: "api", Number: 10, Title: "Fix login", URL: "https://example.com/api/issues/10", State: "OPEN", Assignees: []string{"alice"}},
		},
		PRs: []domain.PRSnapshot{
			{Repo: "api", Number: 11, Title: "Add sessions", State: "OPEN", ReviewDecision: "REVIEW_REQUIRED"},
		},
		ReviewNeeded: []domain.PRSnapshot{
			{Repo: "api", Number: 12, Title: "Refactor auth", State: "OPEN", IsDraft: true},
		},
		Workflows: []domain.WorkflowSnapshot{
			{Name: "ci", RunNumber: 4, Status: "completed", Conclusion: "success", HeadBranch: "main"},
		},
	}
	var buf bytes.Buffer
	if err := (TextRenderer{}).RenderManager(&buf, report); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Issues (1):") || !strings.Contains(out, "PRs (1):") || !strings.Contains(out, "Workflow Runs (1):") || !strings.Contains(out, "url: https://example.com/api/issues/10") {
		t.Fatalf("unexpected manager render: %q", out)
	}
}
