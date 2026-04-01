package render

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/adrian1-dot/ferret/internal/domain"
)

const (
	GeneratedStart = "<!-- ferret:generated:start -->"
	GeneratedEnd   = "<!-- ferret:generated:end -->"
)

type Renderer interface {
	RenderManager(io.Writer, domain.ManagerReport) error
	RenderNext(io.Writer, domain.NextReport) error
	RenderCatchUp(io.Writer, domain.CatchUpReport) error
	RenderActivity(io.Writer, domain.ActivityReport) error
	RenderProjectInspect(io.Writer, domain.ProjectSchema) error
	RenderRepoInspect(io.Writer, domain.RepoSnapshot) error
	RenderIssueInspect(io.Writer, domain.IssueSnapshot) error
	RenderPRInspect(io.Writer, domain.PRSnapshot) error
}

type Factory struct{}

func (Factory) ForFormat(name string) (Renderer, error) {
	switch strings.ToLower(name) {
	case "", "text":
		return TextRenderer{}, nil
	case "json":
		return JSONRenderer{}, nil
	case "markdown", "md":
		return MarkdownRenderer{}, nil
	default:
		return nil, fmt.Errorf("unknown format %q", name)
	}
}

type TextRenderer struct{}

func (TextRenderer) RenderManager(w io.Writer, report domain.ManagerReport) error {
	fmt.Fprintf(w, "%s: %s\nGenerated: %s\n", capitalize(report.TargetKind), report.Target, report.GeneratedAt.Format("2006-01-02 15:04:05Z07:00"))
	if report.Since != "" {
		fmt.Fprintf(w, "Since: %s\n", report.Since)
	}
	if report.Partial {
		fmt.Fprintln(w, "Partial: true")
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintln(w, "\nWarnings:")
		for _, warning := range report.Warnings {
			fmt.Fprintf(w, "- %s\n", warning)
		}
	}
	fmt.Fprintln(w, "\nSummary:")
	for _, line := range managerSummaryLines(report.Summary) {
		fmt.Fprintf(w, "- %s\n", line)
	}
	if len(report.ReviewNeeded) > 0 {
		renderManagerPRsText(w, "Review Needed", report.ReviewNeeded)
	}
	if len(report.Issues) > 0 {
		renderManagerIssuesText(w, report.Issues)
	}
	if len(report.PRs) > 0 {
		renderManagerPRsText(w, "PRs", report.PRs)
	}
	if len(report.Items) > 0 {
		renderManagerProjectItemsText(w, report.Items)
	}
	if len(report.Workflows) > 0 {
		renderManagerWorkflowsText(w, report.Workflows)
	}
	return nil
}

func (TextRenderer) RenderNext(w io.Writer, report domain.NextReport) error {
	fmt.Fprintf(w, "%s: %s\nGenerated: %s\n", capitalize(report.TargetKind), report.Target, report.GeneratedAt.Format("2006-01-02 15:04:05Z07:00"))
	if report.Since != "" {
		fmt.Fprintf(w, "Since: %s\n", report.Since)
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintln(w, "\nWarnings:")
		for _, warning := range report.Warnings {
			fmt.Fprintf(w, "- %s\n", warning)
		}
	}
	sections := sortedNextSections(report.Sections)
	if len(sections) == 0 {
		fmt.Fprintln(w, "\nNo open work matched the selected scope.")
		return nil
	}
	for _, section := range sections {
		if len(section.Items) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s (%d):\n", section.Name, len(section.Items))
		for _, item := range section.Items {
			fmt.Fprintf(w, "- #%d %s\n", item.Number, item.Title)
			if meta := nextItemMeta(item); meta != "" {
				fmt.Fprintf(w, "  %s\n", meta)
			}
		}
	}
	return nil
}

func (TextRenderer) RenderCatchUp(w io.Writer, report domain.CatchUpReport) error {
	fmt.Fprintf(w, "%s: %s\nGenerated: %s\nSince: %s\n", capitalize(report.TargetKind), report.Target, report.GeneratedAt.Format("2006-01-02 15:04:05Z07:00"), report.Since)
	if report.Partial {
		fmt.Fprintln(w, "Partial: true")
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintln(w, "\nWarnings:")
		for _, warning := range report.Warnings {
			fmt.Fprintf(w, "- %s\n", warning)
		}
	}
	if len(report.WatchedItems) > 0 {
		fmt.Fprintf(w, "\nWatched Items (%d):\n", len(report.WatchedItems))
		for _, item := range report.WatchedItems {
			renderWatchedItemEntryText(w, item)
		}
	}
	if len(report.Entries) == 0 {
		fmt.Fprintln(w, "\nNo changed items.")
		return nil
	}
	fmt.Fprintln(w, "\nChanged Items:")
	for _, group := range groupCatchUpEntries(report.Entries) {
		fmt.Fprintf(w, "\n%s (%d):\n", group.name, len(group.entries))
		for _, entry := range group.entries {
			renderCatchUpEntryText(w, entry)
		}
	}
	return nil
}

func (TextRenderer) RenderActivity(w io.Writer, report domain.ActivityReport) error {
	fmt.Fprintf(w, "%s: %s\nGenerated: %s\nSince: %s\n", capitalize(report.TargetKind), report.Target, report.GeneratedAt.Format("2006-01-02 15:04:05Z07:00"), report.Since)
	if report.Partial {
		fmt.Fprintln(w, "Partial: true")
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintln(w, "\nWarnings:")
		for _, warning := range report.Warnings {
			fmt.Fprintf(w, "- %s\n", warning)
		}
	}
	if len(report.WatchedItems) > 0 {
		fmt.Fprintf(w, "\nWatched Items (%d):\n", len(report.WatchedItems))
		for _, item := range report.WatchedItems {
			renderWatchedItemEntryText(w, item)
		}
	}
	if len(report.Entries) == 0 {
		fmt.Fprintln(w, "\nNo recent activity.")
		return nil
	}
	fmt.Fprintln(w, "\nRecent Activity:")
	repoGroups := groupActivityByRepo(report.Entries)
	multiRepo := len(repoGroups) > 1
	for _, group := range repoGroups {
		if multiRepo {
			fmt.Fprintf(w, "\n%s (%d):\n", group.repo, len(group.entries))
		}
		for _, itemGroup := range groupActivityByItem(group.entries) {
			latest := latestActivityEntry(itemGroup.entries)
			count := len(itemGroup.entries)
			fmt.Fprintf(w, "- #%d %s (%s)\n", latest.Number, latest.Title, latest.Kind)
			if latest.URL != "" {
				fmt.Fprintf(w, "  url: %s\n", latest.URL)
			}
			if count > 1 {
				fmt.Fprintf(w, "  %d events, latest: %s by %s\n", count, latest.EventType, displayActor(latest.Actor))
			} else {
				fmt.Fprintf(w, "  %s by %s\n", latest.EventType, displayActor(latest.Actor))
			}
			if !latest.OccurredAt.IsZero() {
				fmt.Fprintf(w, "  at: %s\n", latest.OccurredAt.Format("2006-01-02 15:04:05Z07:00"))
			}
			if latest.Path != "" {
				fmt.Fprintf(w, "  path: %s\n", latest.Path)
			}
			if latest.Preview != "" {
				fmt.Fprintf(w, "  %s\n", latest.Preview)
			}
		}
	}
	return nil
}

func (TextRenderer) RenderProjectInspect(w io.Writer, schema domain.ProjectSchema) error {
	fmt.Fprintf(w, "Project %s/%d: %s\n%s\n", schema.Owner, schema.Number, schema.Title, schema.URL)
	fmt.Fprintln(w, "\nFields:")
	for _, field := range schema.Fields {
		fmt.Fprintf(w, "- %s (%s)\n", field.Name, field.Type)
		if len(field.Options) > 0 {
			var names []string
			for _, opt := range field.Options {
				names = append(names, opt.Name)
			}
			fmt.Fprintf(w, "  options: %s\n", strings.Join(names, ", "))
		}
	}
	return nil
}

func (TextRenderer) RenderRepoInspect(w io.Writer, repo domain.RepoSnapshot) error {
	fmt.Fprintf(w, "Repo: %s/%s\nURL: %s\nUpdated: %s\n", repo.Owner, repo.Name, repo.URL, repo.UpdatedAt.Format("2006-01-02 15:04:05Z07:00"))
	return nil
}

func (TextRenderer) RenderIssueInspect(w io.Writer, issue domain.IssueSnapshot) error {
	fmt.Fprintf(w, "Issue: %s/%s#%d\nTitle: %s\nURL: %s\nState: %s\nAuthor: %s\n",
		issue.Owner, issue.Repo, issue.Number, issue.Title, issue.URL, issue.State, displayActor(issue.Author))
	if len(issue.Assignees) > 0 {
		fmt.Fprintf(w, "Assignees: %s\n", strings.Join(issue.Assignees, ", "))
	}
	if len(issue.Labels) > 0 {
		fmt.Fprintf(w, "Labels: %s\n", strings.Join(issue.Labels, ", "))
	}
	fmt.Fprintf(w, "Updated: %s\n", issue.UpdatedAt.Format("2006-01-02 15:04:05Z07:00"))
	return nil
}

func (TextRenderer) RenderPRInspect(w io.Writer, pr domain.PRSnapshot) error {
	fmt.Fprintf(w, "Pull Request: %s/%s#%d\nTitle: %s\nURL: %s\nState: %s\nAuthor: %s\n",
		pr.Owner, pr.Repo, pr.Number, pr.Title, pr.URL, pr.State, displayActor(pr.Author))
	if pr.MergedBy != "" {
		fmt.Fprintf(w, "Merged By: %s\n", displayActor(pr.MergedBy))
	}
	if len(pr.Assignees) > 0 {
		fmt.Fprintf(w, "Assignees: %s\n", strings.Join(pr.Assignees, ", "))
	}
	if len(pr.RequestedReviewers) > 0 {
		fmt.Fprintf(w, "Requested Reviewers: %s\n", strings.Join(pr.RequestedReviewers, ", "))
	}
	fmt.Fprintf(w, "Updated: %s\n", pr.UpdatedAt.Format("2006-01-02 15:04:05Z07:00"))
	return nil
}

type JSONRenderer struct{}

func (JSONRenderer) RenderManager(w io.Writer, report domain.ManagerReport) error {
	return json.NewEncoder(w).Encode(report)
}

func (JSONRenderer) RenderNext(w io.Writer, report domain.NextReport) error {
	return json.NewEncoder(w).Encode(report)
}
func (JSONRenderer) RenderCatchUp(w io.Writer, report domain.CatchUpReport) error {
	return json.NewEncoder(w).Encode(report)
}
func (JSONRenderer) RenderActivity(w io.Writer, report domain.ActivityReport) error {
	return json.NewEncoder(w).Encode(report)
}

func (JSONRenderer) RenderProjectInspect(w io.Writer, schema domain.ProjectSchema) error {
	return json.NewEncoder(w).Encode(schema)
}

func (JSONRenderer) RenderRepoInspect(w io.Writer, repo domain.RepoSnapshot) error {
	return json.NewEncoder(w).Encode(repo)
}

func (JSONRenderer) RenderIssueInspect(w io.Writer, issue domain.IssueSnapshot) error {
	return json.NewEncoder(w).Encode(issue)
}

func (JSONRenderer) RenderPRInspect(w io.Writer, pr domain.PRSnapshot) error {
	return json.NewEncoder(w).Encode(pr)
}

type MarkdownRenderer struct{ TextRenderer }

func (MarkdownRenderer) RenderManager(w io.Writer, report domain.ManagerReport) error {
	fmt.Fprintf(w, "# %s\n\n", report.Target)
	fmt.Fprintf(w, "Generated at: `%s`\n\n", report.GeneratedAt.Format("2006-01-02 15:04:05Z07:00"))
	if report.Since != "" {
		fmt.Fprintf(w, "Since: `%s`\n\n", report.Since)
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintln(w, "## Warnings")
		for _, warning := range report.Warnings {
			fmt.Fprintf(w, "- %s\n", warning)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "## Summary")
	for _, line := range managerSummaryLines(report.Summary) {
		fmt.Fprintf(w, "- %s\n", line)
	}
	if len(report.ReviewNeeded) > 0 {
		renderManagerPRsMarkdown(w, "Review Needed", report.ReviewNeeded)
	}
	if len(report.Issues) > 0 {
		renderManagerIssuesMarkdown(w, report.Issues)
	}
	if len(report.PRs) > 0 {
		renderManagerPRsMarkdown(w, "PRs", report.PRs)
	}
	if len(report.Items) > 0 {
		renderManagerProjectItemsMarkdown(w, report.Items)
	}
	if len(report.Workflows) > 0 {
		renderManagerWorkflowsMarkdown(w, report.Workflows)
	}
	return nil
}

func (MarkdownRenderer) RenderNext(w io.Writer, report domain.NextReport) error {
	fmt.Fprintf(w, "# %s\n\n", report.Target)
	fmt.Fprintf(w, "Generated at: `%s`\n\n", report.GeneratedAt.Format("2006-01-02 15:04:05Z07:00"))
	if report.Since != "" {
		fmt.Fprintf(w, "Since: `%s`\n\n", report.Since)
	}
	fmt.Fprintln(w, GeneratedStart)
	if len(report.Warnings) > 0 {
		fmt.Fprintln(w, "## Warnings")
		for _, warning := range report.Warnings {
			fmt.Fprintf(w, "- %s\n", warning)
		}
		fmt.Fprintln(w)
	}
	for _, section := range sortedNextSections(report.Sections) {
		if len(section.Items) == 0 {
			continue
		}
		fmt.Fprintf(w, "## %s (%d)\n", section.Name, len(section.Items))
		for _, item := range section.Items {
			title := item.Title
			if item.URL != "" {
				title = fmt.Sprintf("[%s](%s)", item.Title, item.URL)
			}
			fmt.Fprintf(w, "- #%d %s\n", item.Number, title)
			if meta := nextItemMeta(item); meta != "" {
				fmt.Fprintf(w, "  - %s\n", meta)
			}
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, GeneratedEnd)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## Notes")
	fmt.Fprintln(w)
	return nil
}

func (MarkdownRenderer) RenderCatchUp(w io.Writer, report domain.CatchUpReport) error {
	fmt.Fprintf(w, "# %s\n\n", report.Target)
	fmt.Fprintf(w, "Generated at: `%s`\n\n", report.GeneratedAt.Format("2006-01-02 15:04:05Z07:00"))
	fmt.Fprintf(w, "Since: `%s`\n\n", report.Since)
	if len(report.Warnings) > 0 {
		fmt.Fprintln(w, "## Warnings")
		for _, warning := range report.Warnings {
			fmt.Fprintf(w, "- %s\n", warning)
		}
		fmt.Fprintln(w)
	}
	if len(report.WatchedItems) > 0 {
		fmt.Fprintf(w, "## Watched Items (%d)\n", len(report.WatchedItems))
		for _, item := range report.WatchedItems {
			renderWatchedItemEntryMarkdown(w, item)
		}
		fmt.Fprintln(w)
	}
	if len(report.Entries) == 0 {
		fmt.Fprintln(w, "## Changed Items")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "No changed items.")
		return nil
	}
	fmt.Fprintln(w, "## Changed Items")
	for _, group := range groupCatchUpEntries(report.Entries) {
		fmt.Fprintf(w, "### %s (%d)\n", group.name, len(group.entries))
		for _, entry := range group.entries {
			fmt.Fprintf(w, "- %s#%d %s [%s by %s]\n", entry.Repo, entry.Number, entry.Title, entry.LatestEvent, displayActor(entry.LatestActor))
			if entry.URL != "" {
				fmt.Fprintf(w, "  - url: %s\n", entry.URL)
			}
			if entry.BoardStatus != "" {
				fmt.Fprintf(w, "  - board: %s\n", entry.BoardStatus)
			}
			if !entry.LatestAt.IsZero() {
				fmt.Fprintf(w, "  - at: %s\n", entry.LatestAt.Format("2006-01-02 15:04:05Z07:00"))
			}
			if len(entry.Involvement) > 0 {
				fmt.Fprintf(w, "  - involvement: %s\n", strings.Join(entry.Involvement, ", "))
			}
			if len(entry.NotificationReasons) > 0 {
				fmt.Fprintf(w, "  - notifications: %s\n", strings.Join(entry.NotificationReasons, ", "))
			}
			switch entry.PreviewSource {
			case "exact":
				fmt.Fprintf(w, "  - preview: %s\n", entry.Preview)
			case "recovered":
				fmt.Fprintf(w, "  - preview (recovered): %s\n", entry.Preview)
			case "notification_only":
				// notification-only entries have no meaningful preview content; skip
			default:
				if entry.Preview != "" {
					fmt.Fprintf(w, "  - preview: %s\n", entry.Preview)
				}
			}
		}
		fmt.Fprintln(w)
	}
	return nil
}

func (MarkdownRenderer) RenderActivity(w io.Writer, report domain.ActivityReport) error {
	fmt.Fprintf(w, "# %s\n\n", report.Target)
	fmt.Fprintf(w, "Generated at: `%s`\n\n", report.GeneratedAt.Format("2006-01-02 15:04:05Z07:00"))
	fmt.Fprintf(w, "Since: `%s`\n\n", report.Since)
	if len(report.Warnings) > 0 {
		fmt.Fprintln(w, "## Warnings")
		for _, warning := range report.Warnings {
			fmt.Fprintf(w, "- %s\n", warning)
		}
		fmt.Fprintln(w)
	}
	if len(report.WatchedItems) > 0 {
		fmt.Fprintf(w, "## Watched Items (%d)\n", len(report.WatchedItems))
		for _, item := range report.WatchedItems {
			renderWatchedItemEntryMarkdown(w, item)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "## Recent Activity")
	repoGroups := groupActivityByRepo(report.Entries)
	multiRepo := len(repoGroups) > 1
	for _, group := range repoGroups {
		if multiRepo {
			fmt.Fprintf(w, "### %s (%d)\n", group.repo, len(group.entries))
		}
		for _, itemGroup := range groupActivityByItem(group.entries) {
			latest := latestActivityEntry(itemGroup.entries)
			count := len(itemGroup.entries)
			fmt.Fprintf(w, "- [#%d %s (%s)](%s)\n", latest.Number, latest.Title, latest.Kind, latest.URL)
			if count > 1 {
				fmt.Fprintf(w, "  - %d events, latest: %s by %s\n", count, latest.EventType, displayActor(latest.Actor))
			} else {
				fmt.Fprintf(w, "  - %s by %s\n", latest.EventType, displayActor(latest.Actor))
			}
			if !latest.OccurredAt.IsZero() {
				fmt.Fprintf(w, "  - at: %s\n", latest.OccurredAt.Format("2006-01-02 15:04:05Z07:00"))
			}
			if latest.Path != "" {
				fmt.Fprintf(w, "  - path: %s\n", latest.Path)
			}
			if latest.Preview != "" {
				fmt.Fprintf(w, "  - %s\n", latest.Preview)
			}
		}
		if multiRepo {
			fmt.Fprintln(w)
		}
	}
	return nil
}

func (m MarkdownRenderer) RenderProjectInspect(w io.Writer, schema domain.ProjectSchema) error {
	return m.TextRenderer.RenderProjectInspect(w, schema)
}

func (m MarkdownRenderer) RenderRepoInspect(w io.Writer, repo domain.RepoSnapshot) error {
	return m.TextRenderer.RenderRepoInspect(w, repo)
}

func (m MarkdownRenderer) RenderIssueInspect(w io.Writer, issue domain.IssueSnapshot) error {
	return m.TextRenderer.RenderIssueInspect(w, issue)
}

func (m MarkdownRenderer) RenderPRInspect(w io.Writer, pr domain.PRSnapshot) error {
	return m.TextRenderer.RenderPRInspect(w, pr)
}

func SyncNextMarkdown(existing string, report domain.NextReport) (string, error) {
	var b strings.Builder
	if err := (MarkdownRenderer{}).RenderNext(&b, report); err != nil {
		return "", err
	}
	generated := b.String()
	if idx := strings.Index(existing, "## Notes"); idx >= 0 {
		head := generated
		if cut := strings.Index(head, "## Notes"); cut >= 0 {
			head = strings.TrimRight(head[:cut], "\n")
		}
		return head + "\n\n" + existing[idx:], nil
	}
	return generated, nil
}

// sectionSortOrder returns a numeric priority for known next section names so
// they are always rendered in the order: Assigned to Me, Review Needed, Open.
// Unknown sections sort last.
func sectionSortOrder(name string) int {
	switch strings.ToLower(name) {
	case "assigned to me":
		return 0
	case "review needed":
		return 1
	case "open":
		return 2
	default:
		return 3
	}
}

// sortedNextSections returns a copy of the sections slice ordered by
// sectionSortOrder. Empty sections are included so callers can decide whether
// to skip them.
func sortedNextSections(sections []domain.NextSection) []domain.NextSection {
	out := make([]domain.NextSection, len(sections))
	copy(out, sections)
	slices.SortStableFunc(out, func(a, b domain.NextSection) int {
		return sectionSortOrder(a.Name) - sectionSortOrder(b.Name)
	})
	return out
}

func nextItemMeta(item domain.ProjectItem) string {
	var parts []string
	parts = append(parts, formatAssignees(item.Assignees))
	if age := ageText(item.UpdatedAt, time.Time{}); age != "" {
		parts = append(parts, age)
	}
	if raw := rawStatusValue(item.FieldValues); raw != "" {
		parts = append(parts, "status: "+raw)
	}
	return strings.Join(parts, " | ")
}

// rawStatusValue returns the raw value of the first field whose name contains
// "status" (case-insensitive), or an empty string if none is found.
func rawStatusValue(fields map[string]string) string {
	for key, value := range fields {
		if strings.Contains(strings.ToLower(key), "status") {
			v := strings.TrimSpace(value)
			if v != "" {
				return v
			}
		}
	}
	return ""
}

func managerSummaryLines(summary map[string]int) []string {
	order := []string{"total", "issues", "prs", "review_needed", "workflow_runs", "board_ready", "board_in_progress", "board_in_review", "board_blocked", "board_done", "board_backlog", "board_unknown", "open", "in_progress", "in_review", "blocked", "done", "unknown"}
	seen := map[string]bool{}
	var lines []string
	for _, key := range order {
		if value, ok := summary[key]; ok {
			lines = append(lines, fmt.Sprintf("%s: %d", key, value))
			seen[key] = true
		}
	}
	var rest []string
	for key, value := range summary {
		if seen[key] {
			continue
		}
		rest = append(rest, fmt.Sprintf("%s: %d", key, value))
	}
	slices.Sort(rest)
	lines = append(lines, rest...)
	return lines
}

func renderManagerProjectItemsText(w io.Writer, items []domain.ProjectItem) {
	fmt.Fprintf(w, "\nProject Items (%d):\n", len(items))
	for _, item := range items {
		fmt.Fprintf(w, "- %s#%d %s\n", item.Repo, item.Number, item.Title)
		if item.URL != "" {
			fmt.Fprintf(w, "  url: %s\n", item.URL)
		}
		if meta := nextItemMeta(item); meta != "" {
			fmt.Fprintf(w, "  %s\n", meta)
		}
		if !item.UpdatedAt.IsZero() {
			fmt.Fprintf(w, "  updated: %s\n", item.UpdatedAt.Format("2006-01-02 15:04:05Z07:00"))
		}
	}
}

func renderManagerIssuesText(w io.Writer, issues []domain.IssueSnapshot) {
	open, closed := partitionIssuesByState(issues)
	renderManagerIssueGroupText(w, "Open Issues", open)
	renderManagerIssueGroupText(w, "Closed Issues", closed)
}

func renderManagerIssueGroupText(w io.Writer, title string, issues []domain.IssueSnapshot) {
	if len(issues) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s (%d):\n", title, len(issues))
	for _, issue := range issues {
		fmt.Fprintf(w, "- %s#%d %s\n", issue.Repo, issue.Number, issue.Title)
		if issue.URL != "" {
			fmt.Fprintf(w, "  url: %s\n", issue.URL)
		}
		var meta []string
		meta = append(meta, issueAssigneesText(issue.Assignees))
		if labels := labelsText(issue.Labels); labels != "" {
			meta = append(meta, "labels: "+labels)
		}
		if issue.BoardStatus != "" {
			meta = append(meta, "board: "+issue.BoardStatus)
		}
		meta = append(meta, ageText(issue.UpdatedAt, issue.CreatedAt))
		fmt.Fprintf(w, "  %s\n", strings.Join(meta, " | "))
	}
}

func renderManagerPRsText(w io.Writer, title string, prs []domain.PRSnapshot) {
	open, closedMerged := partitionPRsByState(prs)
	if len(open) > 0 || len(closedMerged) == 0 {
		renderManagerPRGroupText(w, title, open)
	}
	if len(closedMerged) > 0 {
		renderManagerPRGroupText(w, "Closed/Merged "+title, closedMerged)
	}
}

func renderManagerPRGroupText(w io.Writer, title string, prs []domain.PRSnapshot) {
	if len(prs) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s (%d):\n", title, len(prs))
	for _, pr := range prs {
		fmt.Fprintf(w, "- %s#%d %s\n", pr.Repo, pr.Number, pr.Title)
		if pr.URL != "" {
			fmt.Fprintf(w, "  url: %s\n", pr.URL)
		}
		var meta []string
		if pr.IsDraft {
			meta = append(meta, "draft")
		}
		if pr.ReviewDecision != "" {
			meta = append(meta, "review: "+strings.ToLower(pr.ReviewDecision))
		}
		if pr.BoardStatus != "" {
			meta = append(meta, "board: "+pr.BoardStatus)
		}
		meta = append(meta, prAssigneesText(pr.Assignees))
		meta = append(meta, ageText(pr.UpdatedAt, pr.CreatedAt))
		fmt.Fprintf(w, "  %s\n", strings.Join(meta, " | "))
		if len(pr.ClosingIssues) > 0 {
			parts := make([]string, len(pr.ClosingIssues))
			for i, n := range pr.ClosingIssues {
				parts[i] = fmt.Sprintf("#%d", n)
			}
			fmt.Fprintf(w, "  closes: %s\n", strings.Join(parts, ", "))
		}
	}
}

func renderManagerWorkflowsText(w io.Writer, runs []domain.WorkflowSnapshot) {
	fmt.Fprintf(w, "\nWorkflow Runs (%d):\n", len(runs))
	for _, run := range runs {
		fmt.Fprintf(w, "- %s #%d\n", run.Name, run.RunNumber)
		if run.URL != "" {
			fmt.Fprintf(w, "  url: %s\n", run.URL)
		}
		var meta []string
		if run.Status != "" {
			meta = append(meta, "status: "+strings.ToLower(run.Status))
		}
		if run.Conclusion != "" {
			meta = append(meta, "conclusion: "+strings.ToLower(run.Conclusion))
		}
		if run.HeadBranch != "" {
			meta = append(meta, "branch: "+run.HeadBranch)
		}
		if len(meta) > 0 {
			fmt.Fprintf(w, "  %s\n", strings.Join(meta, " | "))
		}
		if !run.UpdatedAt.IsZero() {
			fmt.Fprintf(w, "  updated: %s\n", run.UpdatedAt.Format("2006-01-02 15:04:05Z07:00"))
		}
	}
}

func renderManagerProjectItemsMarkdown(w io.Writer, items []domain.ProjectItem) {
	fmt.Fprintf(w, "\n## Project Items (%d)\n", len(items))
	for _, item := range items {
		fmt.Fprintf(w, "- %s#%d %s (%s)\n", item.Repo, item.Number, item.Title, item.URL)
		if meta := nextItemMeta(item); meta != "" {
			fmt.Fprintf(w, "  - %s\n", meta)
		}
	}
	fmt.Fprintln(w)
}

func renderManagerIssuesMarkdown(w io.Writer, issues []domain.IssueSnapshot) {
	open, closed := partitionIssuesByState(issues)
	renderManagerIssueGroupMarkdown(w, "Open Issues", open)
	renderManagerIssueGroupMarkdown(w, "Closed Issues", closed)
}

func renderManagerIssueGroupMarkdown(w io.Writer, title string, issues []domain.IssueSnapshot) {
	if len(issues) == 0 {
		return
	}
	fmt.Fprintf(w, "\n## %s (%d)\n", title, len(issues))
	for _, issue := range issues {
		fmt.Fprintf(w, "- %s#%d %s (%s)\n", issue.Repo, issue.Number, issue.Title, issue.URL)
		var meta []string
		meta = append(meta, issueAssigneesText(issue.Assignees))
		if labels := labelsText(issue.Labels); labels != "" {
			meta = append(meta, "labels: "+labels)
		}
		if issue.BoardStatus != "" {
			meta = append(meta, "board: "+issue.BoardStatus)
		}
		meta = append(meta, ageText(issue.UpdatedAt, issue.CreatedAt))
		fmt.Fprintf(w, "  - %s\n", strings.Join(meta, " | "))
	}
	fmt.Fprintln(w)
}

func renderManagerPRsMarkdown(w io.Writer, title string, prs []domain.PRSnapshot) {
	open, closedMerged := partitionPRsByState(prs)
	if len(open) > 0 || len(closedMerged) == 0 {
		renderManagerPRGroupMarkdown(w, title, open)
	}
	if len(closedMerged) > 0 {
		renderManagerPRGroupMarkdown(w, "Closed/Merged "+title, closedMerged)
	}
}

func renderManagerPRGroupMarkdown(w io.Writer, title string, prs []domain.PRSnapshot) {
	if len(prs) == 0 {
		return
	}
	fmt.Fprintf(w, "\n## %s (%d)\n", title, len(prs))
	for _, pr := range prs {
		fmt.Fprintf(w, "- %s#%d %s (%s)\n", pr.Repo, pr.Number, pr.Title, pr.URL)
		var meta []string
		if pr.IsDraft {
			meta = append(meta, "draft")
		}
		if pr.ReviewDecision != "" {
			meta = append(meta, "review: "+strings.ToLower(pr.ReviewDecision))
		}
		if pr.BoardStatus != "" {
			meta = append(meta, "board: "+pr.BoardStatus)
		}
		meta = append(meta, prAssigneesText(pr.Assignees))
		meta = append(meta, ageText(pr.UpdatedAt, pr.CreatedAt))
		fmt.Fprintf(w, "  - %s\n", strings.Join(meta, " | "))
		if len(pr.ClosingIssues) > 0 {
			parts := make([]string, len(pr.ClosingIssues))
			for i, n := range pr.ClosingIssues {
				parts[i] = fmt.Sprintf("#%d", n)
			}
			fmt.Fprintf(w, "  - closes: %s\n", strings.Join(parts, ", "))
		}
	}
	fmt.Fprintln(w)
}

func renderManagerWorkflowsMarkdown(w io.Writer, runs []domain.WorkflowSnapshot) {
	fmt.Fprintf(w, "\n## Workflow Runs (%d)\n", len(runs))
	for _, run := range runs {
		fmt.Fprintf(w, "- %s #%d (%s)\n", run.Name, run.RunNumber, run.URL)
		var meta []string
		if run.Status != "" {
			meta = append(meta, "status: "+strings.ToLower(run.Status))
		}
		if run.Conclusion != "" {
			meta = append(meta, "conclusion: "+strings.ToLower(run.Conclusion))
		}
		if run.HeadBranch != "" {
			meta = append(meta, "branch: "+run.HeadBranch)
		}
		if len(meta) > 0 {
			fmt.Fprintf(w, "  - %s\n", strings.Join(meta, " | "))
		}
	}
	fmt.Fprintln(w)
}

type activityGroup struct {
	repo    string
	entries []domain.ActivityEntry
}

type activityItemGroup struct {
	key     string // "number/kind"
	entries []domain.ActivityEntry
}

type catchUpGroup struct {
	name    string
	entries []domain.CatchUpEntry
}

func groupActivityByRepo(entries []domain.ActivityEntry) []activityGroup {
	if len(entries) == 0 {
		return nil
	}
	index := map[string][]domain.ActivityEntry{}
	var repos []string
	for _, entry := range entries {
		repoKey := entry.Owner + "/" + entry.Repo
		if _, ok := index[repoKey]; !ok {
			repos = append(repos, repoKey)
		}
		index[repoKey] = append(index[repoKey], entry)
	}
	slices.Sort(repos)
	var groups []activityGroup
	for _, repo := range repos {
		groups = append(groups, activityGroup{repo: repo, entries: index[repo]})
	}
	return groups
}

// groupActivityByItem collapses multiple events on the same issue/PR into one block.
func groupActivityByItem(entries []domain.ActivityEntry) []activityItemGroup {
	if len(entries) == 0 {
		return nil
	}
	index := map[string][]domain.ActivityEntry{}
	var keys []string
	for _, entry := range entries {
		itemKey := fmt.Sprintf("%d/%s", entry.Number, entry.Kind)
		if _, ok := index[itemKey]; !ok {
			keys = append(keys, itemKey)
		}
		index[itemKey] = append(index[itemKey], entry)
	}
	var groups []activityItemGroup
	for _, key := range keys {
		groups = append(groups, activityItemGroup{key: key, entries: index[key]})
	}
	return groups
}

// latestActivityEntry returns the most recent entry from a slice.
func latestActivityEntry(entries []domain.ActivityEntry) domain.ActivityEntry {
	latest := entries[0]
	for _, e := range entries[1:] {
		if e.OccurredAt.After(latest.OccurredAt) {
			latest = e
		}
	}
	return latest
}

func groupCatchUpEntries(entries []domain.CatchUpEntry) []catchUpGroup {
	var attention, closed, discussion, updated []domain.CatchUpEntry
	for _, entry := range entries {
		isTerminal := entry.State == "closed" || entry.State == "merged" ||
			entry.LatestEvent == "closed" || entry.LatestEvent == "merged"
		switch {
		case isTerminal:
			closed = append(closed, entry)
		case entry.LatestEvent == "mentioned" || entry.LatestEvent == "team_mentioned" || entry.LatestEvent == "review_requested":
			attention = append(attention, entry)
		case entry.LatestEvent == "commented" || entry.LatestEvent == "reviewed":
			discussion = append(discussion, entry)
		default:
			updated = append(updated, entry)
		}
	}
	var groups []catchUpGroup
	if len(attention) > 0 {
		groups = append(groups, catchUpGroup{name: "Needs Attention", entries: attention})
	}
	if len(closed) > 0 {
		groups = append(groups, catchUpGroup{name: "Recently Closed", entries: closed})
	}
	if len(discussion) > 0 {
		groups = append(groups, catchUpGroup{name: "Recent Discussion", entries: discussion})
	}
	if len(updated) > 0 {
		groups = append(groups, catchUpGroup{name: "Other Changes", entries: updated})
	}
	return groups
}

func renderCatchUpEntryText(w io.Writer, entry domain.CatchUpEntry) {
	fmt.Fprintf(w, "- %s#%d %s [%s by %s]\n", entry.Repo, entry.Number, entry.Title, entry.LatestEvent, displayActor(entry.LatestActor))
	if entry.URL != "" {
		fmt.Fprintf(w, "  url: %s\n", entry.URL)
	}
	if entry.BoardStatus != "" {
		fmt.Fprintf(w, "  board: %s\n", entry.BoardStatus)
	}
	if !entry.LatestAt.IsZero() {
		fmt.Fprintf(w, "  at: %s\n", entry.LatestAt.Format("2006-01-02 15:04:05Z07:00"))
	}
	if len(entry.Involvement) > 0 || len(entry.NotificationReasons) > 0 {
		fmt.Fprintf(w, "  involvement: %s", strings.Join(entry.Involvement, ", "))
		if len(entry.NotificationReasons) > 0 {
			if len(entry.Involvement) > 0 {
				fmt.Fprint(w, " | ")
			} else {
				fmt.Fprint(w, "  ")
			}
			fmt.Fprintf(w, "notifications: %s", strings.Join(entry.NotificationReasons, ", "))
		}
		fmt.Fprintln(w)
	}
	switch entry.PreviewSource {
	case "exact":
		fmt.Fprintf(w, "  preview: %s\n", entry.Preview)
	case "recovered":
		fmt.Fprintf(w, "  preview (recovered): %s\n", entry.Preview)
	case "notification_only":
		// notification-only entries have no meaningful preview content; skip
	default:
		if entry.Preview != "" {
			fmt.Fprintf(w, "  preview: %s\n", entry.Preview)
		}
	}
}

// ageString formats a duration as a compact human-readable age string.
// capitalize returns s with the first rune uppercased.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func ageString(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// ageText returns the age string based on the most recent of updatedAt/createdAt.
// If both are zero it returns an empty string.
func ageText(updatedAt, createdAt time.Time) string {
	t := updatedAt
	if t.IsZero() {
		t = createdAt
	}
	if t.IsZero() {
		return ""
	}
	return ageString(time.Since(t))
}

// issueAssigneesText returns "@alice, @bob" or "unassigned".
func issueAssigneesText(assignees []string) string {
	return formatAssignees(assignees)
}

// prAssigneesText returns "@alice, @bob" or "unassigned".
func prAssigneesText(assignees []string) string {
	return formatAssignees(assignees)
}

func formatAssignees(assignees []string) string {
	if len(assignees) == 0 {
		return "unassigned"
	}
	parts := make([]string, len(assignees))
	for i, a := range assignees {
		parts[i] = "@" + a
	}
	return strings.Join(parts, ", ")
}

func partitionIssuesByState(issues []domain.IssueSnapshot) (open, closed []domain.IssueSnapshot) {
	for _, i := range issues {
		if strings.ToLower(i.State) == "open" {
			open = append(open, i)
		} else {
			closed = append(closed, i)
		}
	}
	return
}

func partitionPRsByState(prs []domain.PRSnapshot) (open, closedMerged []domain.PRSnapshot) {
	for _, pr := range prs {
		s := strings.ToLower(pr.State)
		if s == "open" {
			open = append(open, pr)
		} else {
			closedMerged = append(closedMerged, pr)
		}
	}
	return
}

// normalizeActor maps known automation identities to a human-readable label.
func normalizeActor(actor string) string {
	lower := strings.ToLower(actor)
	if strings.HasSuffix(lower, "[bot]") || lower == "github-actions" || lower == "github" {
		return "workflow"
	}
	return actor
}

// displayActor returns "@login" for human actors and "workflow" for automation.
func displayActor(actor string) string {
	n := normalizeActor(actor)
	if n == "workflow" {
		return "workflow"
	}
	return "@" + n
}

// labelsText returns up to 3 labels joined by ", " or "" if none.
func labelsText(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	shown := labels
	if len(shown) > 3 {
		shown = shown[:3]
	}
	return strings.Join(shown, ", ")
}

func renderWatchedItemEntryText(w io.Writer, item domain.WatchedItemEntry) {
	label := "[watched]"
	fmt.Fprintf(w, "- %s %s/%s#%d %s\n", label, item.Owner, item.Repo, item.Number, item.Title)
	if item.URL != "" {
		fmt.Fprintf(w, "  url: %s\n", item.URL)
	}
	if item.State != "" {
		fmt.Fprintf(w, "  state: %s\n", strings.ToLower(item.State))
	}
	if item.EventCount > 0 {
		if !item.LatestAt.IsZero() {
			fmt.Fprintf(w, "  %d events, latest: %s by %s at %s\n", item.EventCount, item.LatestEvent, displayActor(item.LatestActor), item.LatestAt.Format("2006-01-02 15:04:05Z07:00"))
		} else {
			fmt.Fprintf(w, "  %d events, latest: %s by %s\n", item.EventCount, item.LatestEvent, displayActor(item.LatestActor))
		}
		if item.Preview != "" {
			fmt.Fprintf(w, "  preview: %s\n", item.Preview)
		}
	} else {
		fmt.Fprintf(w, "  no activity in this period\n")
	}
}

func renderWatchedItemEntryMarkdown(w io.Writer, item domain.WatchedItemEntry) {
	title := item.Title
	if item.URL != "" {
		title = fmt.Sprintf("[%s](%s)", item.Title, item.URL)
	}
	fmt.Fprintf(w, "- [watched] %s/%s#%d %s\n", item.Owner, item.Repo, item.Number, title)
	if item.State != "" {
		fmt.Fprintf(w, "  - state: %s\n", strings.ToLower(item.State))
	}
	if item.EventCount > 0 {
		if !item.LatestAt.IsZero() {
			fmt.Fprintf(w, "  - %d events, latest: %s by %s at %s\n", item.EventCount, item.LatestEvent, displayActor(item.LatestActor), item.LatestAt.Format("2006-01-02 15:04:05Z07:00"))
		} else {
			fmt.Fprintf(w, "  - %d events, latest: %s by %s\n", item.EventCount, item.LatestEvent, displayActor(item.LatestActor))
		}
		if item.Preview != "" {
			fmt.Fprintf(w, "  - preview: %s\n", item.Preview)
		}
	} else {
		fmt.Fprintf(w, "  - no activity in this period\n")
	}
}

func formatInterestingFields(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	preferred := []string{"Release", "RC Milestone", "Priority", "Initiative"}
	seen := map[string]bool{}
	var parts []string
	for _, key := range preferred {
		if value := strings.TrimSpace(values[key]); value != "" {
			parts = append(parts, key+"="+value)
			seen[key] = true
		}
	}
	var rest []string
	for key, value := range values {
		if seen[key] || strings.TrimSpace(value) == "" {
			continue
		}
		lower := strings.ToLower(key)
		if strings.Contains(lower, "status") {
			continue
		}
		rest = append(rest, key+"="+value)
	}
	slices.Sort(rest)
	parts = append(parts, rest...)
	if len(parts) == 0 {
		return ""
	}
	return "fields: " + strings.Join(parts, " | ")
}
