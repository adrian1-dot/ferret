package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adrian1-dot/ferret/internal/domain"
)

const apiBase = "https://api.github.com"

// HTTPBackend implements Backend using direct GitHub REST and GraphQL API calls.
// It requires only a token — no gh CLI dependency.
type HTTPBackend struct {
	token      string
	client     *http.Client
	viewerOnce sync.Once
	viewerName string
	viewerErr  error
}

// NewHTTPBackend creates a Backend backed by direct HTTP calls authenticated
// with the given token.
func NewHTTPBackend(token string) Backend {
	return &HTTPBackend{
		token:  token,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// GetViewer returns the login of the authenticated user. Result is cached after
// the first successful call.
func (b *HTTPBackend) GetViewer(ctx context.Context) (string, error) {
	b.viewerOnce.Do(func() {
		var out struct {
			Login string `json:"login"`
		}
		b.viewerErr = b.get(ctx, &out, "/user")
		b.viewerName = out.Login
	})
	return b.viewerName, b.viewerErr
}

func (b *HTTPBackend) GetRepo(ctx context.Context, owner, repo string) (domain.RepoSnapshot, error) {
	var out struct {
		Name          string    `json:"name"`
		HTMLURL       string    `json:"html_url"`
		Private       bool      `json:"private"`
		UpdatedAt     time.Time `json:"updated_at"`
		DefaultBranch string    `json:"default_branch"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := b.get(ctx, &out, fmt.Sprintf("/repos/%s/%s", owner, repo)); err != nil {
		return domain.RepoSnapshot{}, err
	}
	return domain.RepoSnapshot{
		Owner:      out.Owner.Login,
		Name:       out.Name,
		URL:        out.HTMLURL,
		IsPrivate:  out.Private,
		UpdatedAt:  out.UpdatedAt,
		DefaultRef: out.DefaultBranch,
	}, nil
}

func (b *HTTPBackend) GetIssue(ctx context.Context, owner, repo string, number int) (domain.IssueSnapshot, error) {
	var out struct {
		Number      int        `json:"number"`
		Title       string     `json:"title"`
		HTMLURL     string     `json:"html_url"`
		State       string     `json:"state"`
		Body        string     `json:"body"`
		CreatedAt   time.Time  `json:"created_at"`
		UpdatedAt   time.Time  `json:"updated_at"`
		ClosedAt    *time.Time `json:"closed_at"`
		PullRequest any        `json:"pull_request"`
		User        struct {
			Login string `json:"login"`
		} `json:"user"`
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := b.get(ctx, &out, fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number)); err != nil {
		return domain.IssueSnapshot{}, err
	}
	issue := domain.IssueSnapshot{
		Owner: owner, Repo: repo, Number: out.Number, Title: out.Title, URL: out.HTMLURL,
		State: out.State, Author: out.User.Login, Body: out.Body, CreatedAt: out.CreatedAt,
		UpdatedAt: out.UpdatedAt, ClosedAt: out.ClosedAt, Repository: owner + "/" + repo,
	}
	for _, a := range out.Assignees {
		issue.Assignees = append(issue.Assignees, a.Login)
	}
	for _, l := range out.Labels {
		issue.Labels = append(issue.Labels, l.Name)
	}
	return issue, nil
}

func (b *HTTPBackend) GetPullRequest(ctx context.Context, owner, repo string, number int) (domain.PRSnapshot, error) {
	var out struct {
		Number    int        `json:"number"`
		Title     string     `json:"title"`
		HTMLURL   string     `json:"html_url"`
		State     string     `json:"state"`
		Body      string     `json:"body"`
		Draft     bool       `json:"draft"`
		CreatedAt time.Time  `json:"created_at"`
		UpdatedAt time.Time  `json:"updated_at"`
		ClosedAt  *time.Time `json:"closed_at"`
		MergedAt  *time.Time `json:"merged_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		MergedBy *struct {
			Login string `json:"login"`
		} `json:"merged_by"`
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
		RequestedReviewers []struct {
			Login string `json:"login"`
		} `json:"requested_reviewers"`
	}
	if err := b.get(ctx, &out, fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)); err != nil {
		return domain.PRSnapshot{}, err
	}
	pr := domain.PRSnapshot{
		Owner: owner, Repo: repo, Number: out.Number, Title: out.Title, URL: out.HTMLURL,
		State: out.State, Author: out.User.Login, IsDraft: out.Draft,
		CreatedAt: out.CreatedAt, UpdatedAt: out.UpdatedAt,
		ClosedAt: out.ClosedAt, MergedAt: out.MergedAt,
		Repository: owner + "/" + repo,
	}
	if out.MergedBy != nil {
		pr.MergedBy = out.MergedBy.Login
	}
	for _, a := range out.Assignees {
		pr.Assignees = append(pr.Assignees, a.Login)
	}
	for _, rr := range out.RequestedReviewers {
		if rr.Login == "" {
			continue
		}
		pr.RequestedReviewers = append(pr.RequestedReviewers, rr.Login)
	}
	return pr, nil
}

func (b *HTTPBackend) ListUserRecentRepos(ctx context.Context, limit int) ([]domain.RepoSnapshot, error) {
	if limit <= 0 {
		limit = 10
	}
	var out []struct {
		Name          string    `json:"name"`
		HTMLURL       string    `json:"html_url"`
		Private       bool      `json:"private"`
		PushedAt      time.Time `json:"pushed_at"`
		UpdatedAt     time.Time `json:"updated_at"`
		DefaultBranch string    `json:"default_branch"`
		Owner         struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	path := fmt.Sprintf("/user/repos?sort=pushed&per_page=%d&affiliation=owner,collaborator,organization_member", limit)
	if err := b.get(ctx, &out, path); err != nil {
		return nil, err
	}
	repos := make([]domain.RepoSnapshot, 0, len(out))
	for _, r := range out {
		repos = append(repos, domain.RepoSnapshot{
			Owner:      r.Owner.Login,
			Name:       r.Name,
			URL:        r.HTMLURL,
			IsPrivate:  r.Private,
			UpdatedAt:  r.PushedAt,
			DefaultRef: r.DefaultBranch,
		})
	}
	return repos, nil
}

func (b *HTTPBackend) ListRepoIssues(ctx context.Context, owner, repo string, q IssueQuery) ([]domain.IssueSnapshot, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues?state=%s&per_page=100", owner, repo, defaultString(q.State, "open"))
	if q.Since != "" {
		path += "&since=" + q.Since
	}
	var out []struct {
		Number      int        `json:"number"`
		Title       string     `json:"title"`
		HTMLURL     string     `json:"html_url"`
		State       string     `json:"state"`
		Body        string     `json:"body"`
		CreatedAt   time.Time  `json:"created_at"`
		UpdatedAt   time.Time  `json:"updated_at"`
		ClosedAt    *time.Time `json:"closed_at"`
		PullRequest any        `json:"pull_request"`
		User        struct {
			Login string `json:"login"`
		} `json:"user"`
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := b.getPaged(ctx, &out, path); err != nil {
		return nil, err
	}
	var issues []domain.IssueSnapshot
	for _, item := range out {
		if item.PullRequest != nil {
			continue
		}
		issue := domain.IssueSnapshot{
			Owner: owner, Repo: repo, Number: item.Number, Title: item.Title, URL: item.HTMLURL,
			State: item.State, Author: item.User.Login, Body: item.Body, CreatedAt: item.CreatedAt,
			UpdatedAt: item.UpdatedAt, ClosedAt: item.ClosedAt, Repository: owner + "/" + repo,
		}
		for _, a := range item.Assignees {
			issue.Assignees = append(issue.Assignees, a.Login)
		}
		for _, l := range item.Labels {
			issue.Labels = append(issue.Labels, l.Name)
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

// ListRepoPRs uses the GraphQL API to retrieve pull requests with reviewDecision,
// which is not available from the REST list endpoint.
func (b *HTTPBackend) ListRepoPRs(ctx context.Context, owner, repo string, q PRQuery) ([]domain.PRSnapshot, error) {
	states := prStates(defaultString(q.State, "open"))
	var viewer string
	if q.Review {
		var err error
		viewer, err = b.GetViewer(ctx)
		if err != nil {
			return nil, fmt.Errorf("get viewer for review filter: %w", err)
		}
	}

	query := `
query($owner: String!, $repo: String!, $states: [PullRequestState!], $after: String) {
  repository(owner: $owner, name: $repo) {
    pullRequests(first: 100, states: $states, after: $after, orderBy: {field: UPDATED_AT, direction: DESC}) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number title url state isDraft reviewDecision
        createdAt updatedAt closedAt mergedAt
        author { login }
        mergedBy { login }
        assignees(first: 20) { nodes { login } }
        reviewRequests(first: 20) {
          nodes {
            requestedReviewer {
              __typename
              ... on User { login }
            }
          }
        }
        closingIssuesReferences(first: 10) { nodes { number } }
      }
    }
  }
}`

	type prNode struct {
		Number         int        `json:"number"`
		Title          string     `json:"title"`
		URL            string     `json:"url"`
		State          string     `json:"state"`
		IsDraft        bool       `json:"isDraft"`
		ReviewDecision string     `json:"reviewDecision"`
		CreatedAt      time.Time  `json:"createdAt"`
		UpdatedAt      time.Time  `json:"updatedAt"`
		ClosedAt       *time.Time `json:"closedAt"`
		MergedAt       *time.Time `json:"mergedAt"`
		Author         struct {
			Login string `json:"login"`
		} `json:"author"`
		MergedBy struct {
			Login string `json:"login"`
		} `json:"mergedBy"`
		Assignees struct {
			Nodes []struct {
				Login string `json:"login"`
			} `json:"nodes"`
		} `json:"assignees"`
		ReviewRequests struct {
			Nodes []struct {
				RequestedReviewer struct {
					TypeName string `json:"__typename"`
					Login    string `json:"login"`
				} `json:"requestedReviewer"`
			} `json:"nodes"`
		} `json:"reviewRequests"`
		ClosingIssuesReferences struct {
			Nodes []struct {
				Number int `json:"number"`
			} `json:"nodes"`
		} `json:"closingIssuesReferences"`
	}

	type response struct {
		Data struct {
			Repository struct {
				PullRequests struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []prNode `json:"nodes"`
				} `json:"pullRequests"`
			} `json:"repository"`
		} `json:"data"`
	}

	var allNodes []prNode
	var cursor *string
	for {
		vars := map[string]any{
			"owner":  owner,
			"repo":   repo,
			"states": states,
			"after":  cursor,
		}
		var resp response
		if err := b.graphQL(ctx, &resp, query, vars); err != nil {
			if shouldFallbackRepoPRs(err) {
				return b.listRepoPRsRESTFallback(ctx, owner, repo, q, viewer)
			}
			return nil, err
		}
		page := resp.Data.Repository.PullRequests
		allNodes = append(allNodes, page.Nodes...)
		if !page.PageInfo.HasNextPage {
			break
		}
		c := page.PageInfo.EndCursor
		cursor = &c
	}

	var prs []domain.PRSnapshot
	for _, node := range allNodes {
		pr := domain.PRSnapshot{
			Owner: owner, Repo: repo, Number: node.Number, Title: node.Title, URL: node.URL,
			State: node.State, Author: node.Author.Login, IsDraft: node.IsDraft,
			ReviewDecision: node.ReviewDecision, MergedBy: node.MergedBy.Login,
			CreatedAt: node.CreatedAt, UpdatedAt: node.UpdatedAt,
			ClosedAt: node.ClosedAt, MergedAt: node.MergedAt,
			Repository: owner + "/" + repo,
		}
		for _, a := range node.Assignees.Nodes {
			pr.Assignees = append(pr.Assignees, a.Login)
		}
		for _, rr := range node.ReviewRequests.Nodes {
			if rr.RequestedReviewer.TypeName != "User" || rr.RequestedReviewer.Login == "" {
				continue
			}
			pr.RequestedReviewers = append(pr.RequestedReviewers, rr.RequestedReviewer.Login)
		}
		for _, ci := range node.ClosingIssuesReferences.Nodes {
			pr.ClosingIssues = append(pr.ClosingIssues, ci.Number)
		}
		if !withinSince(q.Since, pr.UpdatedAt, pr.CreatedAt, pr.ClosedAt, pr.MergedAt) {
			continue
		}
		if q.Review && !containsFold(pr.RequestedReviewers, viewer) {
			continue
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

func (b *HTTPBackend) listRepoPRsRESTFallback(ctx context.Context, owner, repo string, q PRQuery, viewer string) ([]domain.PRSnapshot, error) {
	type restPR struct {
		Number    int        `json:"number"`
		Title     string     `json:"title"`
		HTMLURL   string     `json:"html_url"`
		State     string     `json:"state"`
		Draft     bool       `json:"draft"`
		CreatedAt time.Time  `json:"created_at"`
		UpdatedAt time.Time  `json:"updated_at"`
		ClosedAt  *time.Time `json:"closed_at"`
		MergedAt  *time.Time `json:"merged_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		MergedBy *struct {
			Login string `json:"login"`
		} `json:"merged_by"`
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
		RequestedReviewers []struct {
			Login string `json:"login"`
		} `json:"requested_reviewers"`
	}
	state := strings.ToLower(defaultString(q.State, "open"))
	restState := state
	if state == "merged" {
		restState = "closed"
	}
	url := apiBase + fmt.Sprintf("/repos/%s/%s/pulls?state=%s&sort=updated&direction=desc&per_page=100", owner, repo, restState)
	var out []restPR
	for url != "" {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		b.setHeaders(req)
		resp, err := b.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("GET %s: %w", url, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read response body: %w", err)
		}
		if resp.StatusCode >= 400 {
			return nil, classifyHTTPError(resp.StatusCode, body, url)
		}
		var page []restPR
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode page: %w", err)
		}
		out = append(out, page...)
		if q.Since != "" && len(page) > 0 && !withinSince(q.Since, page[len(page)-1].UpdatedAt) {
			break
		}
		url = nextLink(resp.Header.Get("Link"))
	}
	var prs []domain.PRSnapshot
	for _, item := range out {
		if state == "merged" && item.MergedAt == nil {
			continue
		}
		pr := domain.PRSnapshot{
			Owner: owner, Repo: repo, Number: item.Number, Title: item.Title, URL: item.HTMLURL,
			State: item.State, Author: item.User.Login, IsDraft: item.Draft,
			CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
			ClosedAt: item.ClosedAt, MergedAt: item.MergedAt,
			Repository: owner + "/" + repo,
		}
		if item.MergedBy != nil {
			pr.MergedBy = item.MergedBy.Login
		}
		for _, a := range item.Assignees {
			pr.Assignees = append(pr.Assignees, a.Login)
		}
		for _, rr := range item.RequestedReviewers {
			if rr.Login == "" {
				continue
			}
			pr.RequestedReviewers = append(pr.RequestedReviewers, rr.Login)
		}
		if !withinSince(q.Since, pr.UpdatedAt, pr.CreatedAt, pr.ClosedAt, pr.MergedAt) {
			continue
		}
		if q.Review && !containsFold(pr.RequestedReviewers, viewer) {
			continue
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

func shouldFallbackRepoPRs(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if IsKind(err, ErrAuth) || IsKind(err, ErrMissingScope) || IsKind(err, ErrNotFound) || IsKind(err, ErrRateLimited) {
		return false
	}
	var ghErr *Error
	if errors.As(err, &ghErr) {
		return strings.Contains(ghErr.Message, "HTTP 500") ||
			strings.Contains(ghErr.Message, "HTTP 502") ||
			strings.Contains(ghErr.Message, "HTTP 503") ||
			strings.Contains(ghErr.Message, "HTTP 504")
	}
	msg := strings.ToLower(err.Error())
	return strings.HasPrefix(msg, "graphql: ") || strings.HasPrefix(msg, "read graphql response: ")
}

func (b *HTTPBackend) ListWorkflowRuns(ctx context.Context, owner, repo string, q WorkflowQuery) ([]domain.WorkflowSnapshot, error) {
	perPage := q.PerPage
	if perPage == 0 {
		perPage = 20
	}
	var out struct {
		WorkflowRuns []struct {
			ID         int64     `json:"id"`
			Name       string    `json:"name"`
			Path       string    `json:"path"`
			Status     string    `json:"status"`
			Conclusion string    `json:"conclusion"`
			HTMLURL    string    `json:"html_url"`
			CreatedAt  time.Time `json:"created_at"`
			UpdatedAt  time.Time `json:"updated_at"`
			RunNumber  int       `json:"run_number"`
			HeadBranch string    `json:"head_branch"`
			Event      string    `json:"event"`
		} `json:"workflow_runs"`
	}
	if err := b.get(ctx, &out, fmt.Sprintf("/repos/%s/%s/actions/runs?per_page=%d", owner, repo, perPage)); err != nil {
		return nil, err
	}
	var runs []domain.WorkflowSnapshot
	for _, run := range out.WorkflowRuns {
		ws := domain.WorkflowSnapshot{
			ID: run.ID, Name: run.Name, Path: run.Path, Status: run.Status, Conclusion: run.Conclusion,
			URL: run.HTMLURL, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
			RunNumber: run.RunNumber, HeadBranch: run.HeadBranch, Event: run.Event,
		}
		if !withinSince(q.Since, ws.UpdatedAt, ws.CreatedAt, nil, nil) {
			continue
		}
		runs = append(runs, ws)
	}
	return runs, nil
}

func (b *HTTPBackend) ListIssueCommentActivity(ctx context.Context, owner, repo, since string) ([]domain.ActivityEvent, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/comments?per_page=100", owner, repo)
	if since != "" {
		path += "&since=" + since
	}
	var out []struct {
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		IssueURL string `json:"issue_url"`
	}
	if err := b.getPaged(ctx, &out, path); err != nil {
		return nil, err
	}
	var events []domain.ActivityEvent
	for _, item := range out {
		number := extractTrailingNumber(item.IssueURL)
		if number == 0 {
			continue
		}
		kind := "issue"
		if strings.Contains(item.IssueURL, "/pulls/") {
			kind = "pull_request"
		}
		events = append(events, domain.ActivityEvent{
			Repo: repo, Owner: owner, Number: number, Kind: kind, EventType: "commented",
			Actor: item.User.Login, OccurredAt: item.CreatedAt, Preview: truncatePreview(item.Body),
		})
	}
	return events, nil
}

func (b *HTTPBackend) ListPullRequestReviewCommentActivity(ctx context.Context, owner, repo, since string) ([]domain.ActivityEvent, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/comments?per_page=100", owner, repo)
	if since != "" {
		path += "&since=" + since
	}
	var out []struct {
		Body           string    `json:"body"`
		CreatedAt      time.Time `json:"created_at"`
		Path           string    `json:"path"`
		PullRequestURL string    `json:"pull_request_url"`
		User           struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := b.getPaged(ctx, &out, path); err != nil {
		return nil, err
	}
	var events []domain.ActivityEvent
	for _, item := range out {
		number := extractTrailingNumber(item.PullRequestURL)
		if number == 0 {
			continue
		}
		events = append(events, domain.ActivityEvent{
			Repo: repo, Owner: owner, Number: number, Kind: "pull_request", EventType: "commented",
			Actor: item.User.Login, OccurredAt: item.CreatedAt, Preview: truncatePreview(item.Body), Path: item.Path,
		})
	}
	return events, nil
}

func (b *HTTPBackend) ListPullRequestReviewActivity(ctx context.Context, owner, repo, since string) ([]domain.ActivityEvent, error) {
	prs, err := b.ListRepoPRs(ctx, owner, repo, PRQuery{State: "all", Since: since})
	if err != nil {
		return nil, err
	}
	var events []domain.ActivityEvent
	for _, pr := range prs {
		var out []struct {
			State       string     `json:"state"`
			Body        string     `json:"body"`
			SubmittedAt *time.Time `json:"submitted_at"`
			User        struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=100", owner, repo, pr.Number)
		if err := b.getPaged(ctx, &out, path); err != nil {
			return nil, err
		}
		for _, review := range out {
			if review.SubmittedAt == nil || !withinSince(since, review.SubmittedAt) {
				continue
			}
			events = append(events, domain.ActivityEvent{
				Repo: repo, Owner: owner, Number: pr.Number, Kind: "pull_request", EventType: "reviewed",
				Actor: review.User.Login, OccurredAt: *review.SubmittedAt, Preview: truncatePreview(review.Body),
			})
		}
	}
	return events, nil
}

func (b *HTTPBackend) ListRepoNotifications(ctx context.Context, owner, repo, since string) ([]domain.NotificationThread, error) {
	path := fmt.Sprintf("/repos/%s/%s/notifications?all=true&participating=true&per_page=100", owner, repo)
	if since != "" {
		path += "&since=" + since
	}
	var out []struct {
		ID        string    `json:"id"`
		Reason    string    `json:"reason"`
		Unread    bool      `json:"unread"`
		UpdatedAt time.Time `json:"updated_at"`
		Subject   struct {
			Title string `json:"title"`
			Type  string `json:"type"`
			URL   string `json:"url"`
		} `json:"subject"`
		Repository struct {
			Name  string `json:"name"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repository"`
	}
	if err := b.getPaged(ctx, &out, path); err != nil {
		return nil, err
	}
	var threads []domain.NotificationThread
	for _, item := range out {
		number := extractTrailingNumber(item.Subject.URL)
		kind := "issue"
		switch strings.ToLower(item.Subject.Type) {
		case "pullrequest":
			kind = "pull_request"
		case "issue":
			kind = "issue"
		default:
			if strings.Contains(item.Subject.URL, "/pulls/") {
				kind = "pull_request"
			}
		}
		thread := domain.NotificationThread{
			ID: item.ID, Repo: item.Repository.Name, Owner: item.Repository.Owner.Login,
			Number: number, Kind: kind, Title: item.Subject.Title, Reason: item.Reason,
			UpdatedAt: item.UpdatedAt, Unread: item.Unread,
		}
		if number != 0 {
			switch kind {
			case "pull_request":
				thread.URL = fmt.Sprintf("https://github.com/%s/%s/pull/%d", thread.Owner, thread.Repo, thread.Number)
			default:
				thread.URL = fmt.Sprintf("https://github.com/%s/%s/issues/%d", thread.Owner, thread.Repo, thread.Number)
			}
		}
		threads = append(threads, thread)
	}
	return threads, nil
}

func (b *HTTPBackend) ListIssueThreadComments(ctx context.Context, owner, repo string, number int) ([]domain.ActivityEvent, error) {
	var out []struct {
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := b.getPaged(ctx, &out, fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100", owner, repo, number)); err != nil {
		return nil, err
	}
	var events []domain.ActivityEvent
	for _, item := range out {
		events = append(events, domain.ActivityEvent{
			Repo: repo, Owner: owner, Number: number, Kind: "issue", EventType: "commented",
			Actor: item.User.Login, OccurredAt: item.CreatedAt, Preview: truncatePreview(item.Body),
		})
	}
	return events, nil
}

func (b *HTTPBackend) ListPullRequestThreadComments(ctx context.Context, owner, repo string, number int) ([]domain.ActivityEvent, error) {
	var out []struct {
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
		Path      string    `json:"path"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := b.getPaged(ctx, &out, fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=100", owner, repo, number)); err != nil {
		return nil, err
	}
	var events []domain.ActivityEvent
	for _, item := range out {
		events = append(events, domain.ActivityEvent{
			Repo: repo, Owner: owner, Number: number, Kind: "pull_request", EventType: "commented",
			Actor: item.User.Login, OccurredAt: item.CreatedAt, Preview: truncatePreview(item.Body), Path: item.Path,
		})
	}
	return events, nil
}

func (b *HTTPBackend) ListPullRequestThreadReviews(ctx context.Context, owner, repo string, number int) ([]domain.ActivityEvent, error) {
	var out []struct {
		State       string     `json:"state"`
		Body        string     `json:"body"`
		SubmittedAt *time.Time `json:"submitted_at"`
		User        struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := b.getPaged(ctx, &out, fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=100", owner, repo, number)); err != nil {
		return nil, err
	}
	var events []domain.ActivityEvent
	for _, item := range out {
		if item.SubmittedAt == nil {
			continue
		}
		events = append(events, domain.ActivityEvent{
			Repo: repo, Owner: owner, Number: number, Kind: "pull_request", EventType: "reviewed",
			Actor: item.User.Login, OccurredAt: *item.SubmittedAt, Preview: truncatePreview(item.Body),
		})
	}
	return events, nil
}

func (b *HTTPBackend) GetProjectSchema(ctx context.Context, owner string, number int) (domain.ProjectSchema, error) {
	project, err := b.fetchProjectSchema(ctx, owner, number)
	if err != nil {
		return domain.ProjectSchema{}, err
	}
	schema := domain.ProjectSchema{
		Owner:  owner,
		Number: number,
		Title:  project.ProjectV2.Title,
		URL:    project.ProjectV2.URL,
	}
	for _, node := range project.ProjectV2.Fields.Nodes {
		field := domain.ProjectField{ID: node.ID, Name: node.Name, Type: node.DataType}
		for _, opt := range node.Options {
			field.Options = append(field.Options, domain.ProjectFieldOption{ID: opt.ID, Name: opt.Name})
		}
		schema.Fields = append(schema.Fields, field)
	}
	return schema, nil
}

func (b *HTTPBackend) ListProjectItems(ctx context.Context, owner string, number int, q ProjectItemsQuery) ([]domain.ProjectItem, error) {
	limit := q.Limit
	if limit == 0 {
		limit = 100
	}
	project, err := b.fetchProjectItems(ctx, owner, number, limit)
	if err != nil {
		return nil, err
	}
	var items []domain.ProjectItem
	for _, node := range project.ProjectV2.Items.Nodes {
		item := domain.ProjectItem{
			ID: node.ID, Title: node.Content.Title, URL: node.Content.URL, Number: node.Content.Number,
			State: node.Content.State, Repo: node.Content.Repository.Name,
			Owner: node.Content.Repository.Owner.Login, UpdatedAt: node.UpdatedAt,
			FieldValues: map[string]string{},
		}
		for _, a := range node.Content.Assignees.Nodes {
			item.Assignees = append(item.Assignees, a.Login)
		}
		switch {
		case item.Number > 0 && strings.Contains(item.URL, "/pull/"):
			item.Type = "pull_request"
		case item.Number > 0:
			item.Type = "issue"
		default:
			item.Type = "draft"
		}
		for _, fv := range node.FieldValues.Nodes {
			if fv.Field.Name == "" {
				continue
			}
			value := firstNonEmpty(fv.Name, fv.Text, fv.Title)
			if value != "" {
				item.FieldValues[fv.Field.Name] = value
			}
		}
		if !withinSince(q.Since, item.UpdatedAt, item.UpdatedAt, nil, nil) {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

// --- HTTP helpers ---

// get performs a single GET request and unmarshals the JSON body into dest.
func (b *HTTPBackend) get(ctx context.Context, dest any, path string) error {
	url := apiBase + path
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	b.setHeaders(req)
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return classifyHTTPError(resp.StatusCode, body, path)
	}
	return json.Unmarshal(body, dest)
}

// getPaged performs paginated GET requests following Link headers, accumulating
// all pages into dest (which must be a pointer to a slice).
func (b *HTTPBackend) getPaged(ctx context.Context, dest any, path string) error {
	var all []json.RawMessage
	url := apiBase + path
	for url != "" {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		b.setHeaders(req)
		resp, err := b.client.Do(req)
		if err != nil {
			return fmt.Errorf("GET %s: %w", url, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read response body: %w", err)
		}
		if resp.StatusCode >= 400 {
			return classifyHTTPError(resp.StatusCode, body, url)
		}
		var page []json.RawMessage
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("decode page: %w", err)
		}
		all = append(all, page...)
		url = nextLink(resp.Header.Get("Link"))
	}
	combined, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return json.Unmarshal(combined, dest)
}

// graphQL executes a GraphQL query and unmarshals the response into dest.
func (b *HTTPBackend) graphQL(ctx context.Context, dest any, query string, vars map[string]any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", apiBase+"/graphql", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	b.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("GraphQL: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read GraphQL response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return classifyHTTPError(resp.StatusCode, body, "graphql")
	}
	// Check for GraphQL-level errors.
	var errCheck struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &errCheck); err == nil && len(errCheck.Errors) > 0 {
		msg := errCheck.Errors[0].Message
		return classifyError(msg, []string{"graphql"})
	}
	return json.Unmarshal(body, dest)
}

func (b *HTTPBackend) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "ferret")
}

// --- GraphQL project helpers ---

type ownerProjectSchema struct {
	ProjectV2 struct {
		Title  string `json:"title"`
		URL    string `json:"url"`
		Fields struct {
			Nodes []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				DataType string `json:"dataType"`
				Options  []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"options"`
			} `json:"nodes"`
		} `json:"fields"`
	} `json:"projectV2"`
}

type ownerProjectItems struct {
	ProjectV2 struct {
		Items struct {
			Nodes []struct {
				ID          string    `json:"id"`
				UpdatedAt   time.Time `json:"updatedAt"`
				FieldValues struct {
					Nodes []struct {
						Name  string `json:"name"`
						Text  string `json:"text"`
						Title string `json:"title"`
						Field struct {
							Name string `json:"name"`
						} `json:"field"`
					} `json:"nodes"`
				} `json:"fieldValues"`
				Content struct {
					Number     int    `json:"number"`
					Title      string `json:"title"`
					URL        string `json:"url"`
					State      string `json:"state"`
					Repository struct {
						Name  string `json:"name"`
						Owner struct {
							Login string `json:"login"`
						} `json:"owner"`
					} `json:"repository"`
					Assignees struct {
						Nodes []struct {
							Login string `json:"login"`
						} `json:"nodes"`
					} `json:"assignees"`
				} `json:"content"`
			} `json:"nodes"`
		} `json:"items"`
	} `json:"projectV2"`
}

func (b *HTTPBackend) fetchProjectSchema(ctx context.Context, owner string, number int) (ownerProjectSchema, error) {
	query := `
query($owner: String!, $number: Int!) {
  organization(login: $owner) {
    projectV2(number: $number) {
      title url
      fields(first: 50) {
        nodes {
          ... on ProjectV2FieldCommon { id name }
          ... on ProjectV2SingleSelectField { dataType options { id name } }
          ... on ProjectV2IterationField { dataType }
        }
      }
    }
  }
}`
	vars := map[string]any{"owner": owner, "number": number}
	var orgResp struct {
		Data struct {
			Organization ownerProjectSchema `json:"organization"`
		} `json:"data"`
	}
	if err := b.graphQL(ctx, &orgResp, query, vars); err == nil && orgResp.Data.Organization.ProjectV2.Title != "" {
		return orgResp.Data.Organization, nil
	}

	query = strings.ReplaceAll(query, "organization(login: $owner)", "user(login: $owner)")
	var userResp struct {
		Data struct {
			User ownerProjectSchema `json:"user"`
		} `json:"data"`
	}
	if err := b.graphQL(ctx, &userResp, query, vars); err != nil {
		return ownerProjectSchema{}, err
	}
	if userResp.Data.User.ProjectV2.Title == "" {
		return ownerProjectSchema{}, &Error{Kind: ErrNotFound, Message: fmt.Sprintf("project %s/%d not found", owner, number)}
	}
	return userResp.Data.User, nil
}

func (b *HTTPBackend) fetchProjectItems(ctx context.Context, owner string, number, limit int) (ownerProjectItems, error) {
	query := `
query($owner: String!, $number: Int!, $limit: Int!) {
  organization(login: $owner) {
    projectV2(number: $number) {
      items(first: $limit) {
        nodes {
          id updatedAt
          fieldValues(first: 50) {
            nodes {
              ... on ProjectV2ItemFieldSingleSelectValue { name field { ... on ProjectV2FieldCommon { name } } }
              ... on ProjectV2ItemFieldTextValue { text field { ... on ProjectV2FieldCommon { name } } }
              ... on ProjectV2ItemFieldIterationValue { title field { ... on ProjectV2FieldCommon { name } } }
            }
          }
          content {
            ... on Issue { number title url state repository { name owner { login } } assignees(first: 20) { nodes { login } } }
            ... on PullRequest { number title url state repository { name owner { login } } assignees(first: 20) { nodes { login } } }
            ... on DraftIssue { title }
          }
        }
      }
    }
  }
}`
	vars := map[string]any{"owner": owner, "number": number, "limit": limit}
	var orgResp struct {
		Data struct {
			Organization ownerProjectItems `json:"organization"`
		} `json:"data"`
	}
	if err := b.graphQL(ctx, &orgResp, query, vars); err == nil && orgResp.Data.Organization.ProjectV2.Items.Nodes != nil {
		return orgResp.Data.Organization, nil
	}

	query = strings.ReplaceAll(query, "organization(login: $owner)", "user(login: $owner)")
	var userResp struct {
		Data struct {
			User ownerProjectItems `json:"user"`
		} `json:"data"`
	}
	if err := b.graphQL(ctx, &userResp, query, vars); err != nil {
		return ownerProjectItems{}, err
	}
	return userResp.Data.User, nil
}

// --- Error classification ---

func classifyHTTPError(status int, body []byte, path string) *Error {
	msg := strings.TrimSpace(string(body))
	// Try to extract a message from a JSON error body.
	var errBody struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &errBody) == nil && errBody.Message != "" {
		msg = errBody.Message
	}
	full := fmt.Sprintf("GitHub API %s: HTTP %d: %s", path, status, msg)
	switch {
	case status == 401:
		return &Error{Kind: ErrAuth, Message: full}
	case status == 403 && strings.Contains(strings.ToLower(msg), "scope"):
		return &Error{Kind: ErrMissingScope, Message: full}
	case status == 403:
		return &Error{Kind: ErrAuth, Message: full}
	case status == 404:
		return &Error{Kind: ErrNotFound, Message: full}
	case status == 429:
		return &Error{Kind: ErrRateLimited, Message: full}
	default:
		return &Error{Kind: ErrUnknown, Message: full}
	}
}

// classifyError maps a string error message to a typed *Error.
// Used for GraphQL error messages and tests.
func classifyError(msg string, args []string) *Error {
	lower := strings.ToLower(msg)
	full := fmt.Sprintf("github %s: %s", strings.Join(args, " "), msg)
	switch {
	case strings.Contains(lower, "token") || strings.Contains(lower, "authentication"):
		return &Error{Kind: ErrAuth, Message: full}
	case strings.Contains(lower, "project scope") || strings.Contains(lower, "requires scope"):
		return &Error{Kind: ErrMissingScope, Message: full}
	case strings.Contains(lower, "not found") ||
		strings.Contains(lower, "could not resolve to a user") ||
		strings.Contains(lower, "could not resolve to an organization"):
		return &Error{Kind: ErrNotFound, Message: full}
	case strings.Contains(lower, "rate limit"):
		return &Error{Kind: ErrRateLimited, Message: full}
	default:
		return &Error{Kind: ErrUnknown, Message: full}
	}
}

// --- Utility functions ---

// nextLink parses a GitHub Link header and returns the URL for rel="next", or "".
var reLinkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func nextLink(header string) string {
	m := reLinkNext.FindStringSubmatch(header)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// prStates maps a REST-style state string to GraphQL PullRequestState values.
func prStates(state string) []string {
	switch strings.ToLower(state) {
	case "open":
		return []string{"OPEN"}
	case "closed":
		return []string{"CLOSED"}
	case "merged":
		return []string{"MERGED"}
	default: // "all" or ""
		return []string{"OPEN", "CLOSED", "MERGED"}
	}
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func extractTrailingNumber(url string) int {
	parts := strings.Split(strings.TrimSpace(url), "/")
	if len(parts) == 0 {
		return 0
	}
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0
	}
	return n
}

func truncatePreview(body string) string {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\n", " "))
	if len(body) <= 120 {
		return body
	}
	return body[:117] + "..."
}

func withinSince(since string, times ...interface{}) bool {
	if since == "" {
		return true
	}
	threshold, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return true
	}
	for _, raw := range times {
		switch v := raw.(type) {
		case time.Time:
			if !v.IsZero() && (v.After(threshold) || v.Equal(threshold)) {
				return true
			}
		case *time.Time:
			if v != nil && !v.IsZero() && (v.After(threshold) || v.Equal(threshold)) {
				return true
			}
		}
	}
	return false
}
