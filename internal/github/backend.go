package github

import (
	"context"
	"errors"

	"github.com/adrian1-dot/ferret/internal/domain"
)

type IssueQuery struct {
	State string
	Since string
}

type PRQuery struct {
	State  string
	Since  string
	Review bool
}

type WorkflowQuery struct {
	PerPage int
	Since   string
}

type ProjectItemsQuery struct {
	Limit int
	Since string
}

type ErrorKind string

const (
	ErrUnknown      ErrorKind = "unknown"
	ErrAuth         ErrorKind = "auth"
	ErrMissingScope ErrorKind = "missing_scope"
	ErrNotFound     ErrorKind = "not_found"
	ErrRateLimited  ErrorKind = "rate_limited"
)

type Error struct {
	Kind    ErrorKind
	Message string
}

func (e *Error) Error() string {
	return e.Message
}

func IsKind(err error, kind ErrorKind) bool {
	var target *Error
	if !errors.As(err, &target) {
		return false
	}
	return target.Kind == kind
}

type Backend interface {
	GetViewer(ctx context.Context) (string, error)
	GetRepo(ctx context.Context, owner, repo string) (domain.RepoSnapshot, error)
	ListUserRecentRepos(ctx context.Context, limit int) ([]domain.RepoSnapshot, error)
	ListRepoIssues(ctx context.Context, owner, repo string, q IssueQuery) ([]domain.IssueSnapshot, error)
	ListRepoPRs(ctx context.Context, owner, repo string, q PRQuery) ([]domain.PRSnapshot, error)
	ListWorkflowRuns(ctx context.Context, owner, repo string, q WorkflowQuery) ([]domain.WorkflowSnapshot, error)
	ListIssueCommentActivity(ctx context.Context, owner, repo, since string) ([]domain.ActivityEvent, error)
	ListPullRequestReviewCommentActivity(ctx context.Context, owner, repo, since string) ([]domain.ActivityEvent, error)
	ListPullRequestReviewActivity(ctx context.Context, owner, repo, since string) ([]domain.ActivityEvent, error)
	ListRepoNotifications(ctx context.Context, owner, repo, since string) ([]domain.NotificationThread, error)
	ListIssueThreadComments(ctx context.Context, owner, repo string, number int) ([]domain.ActivityEvent, error)
	ListPullRequestThreadComments(ctx context.Context, owner, repo string, number int) ([]domain.ActivityEvent, error)
	ListPullRequestThreadReviews(ctx context.Context, owner, repo string, number int) ([]domain.ActivityEvent, error)
	GetProjectSchema(ctx context.Context, owner string, number int) (domain.ProjectSchema, error)
	ListProjectItems(ctx context.Context, owner string, number int, q ProjectItemsQuery) ([]domain.ProjectItem, error)
}
