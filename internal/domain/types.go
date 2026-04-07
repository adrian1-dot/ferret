package domain

import "time"

type StatusTier string

const (
	StatusUnknown    StatusTier = "unknown"
	StatusDone       StatusTier = "done"
	StatusInReview   StatusTier = "in_review"
	StatusInProgress StatusTier = "in_progress"
	StatusReady      StatusTier = "ready"
	StatusBlocked    StatusTier = "blocked"
	StatusBacklog    StatusTier = "backlog"
)

type PriorityTier string

const (
	PriorityNone     PriorityTier = "none"
	PriorityLow      PriorityTier = "low"
	PriorityMedium   PriorityTier = "medium"
	PriorityHigh     PriorityTier = "high"
	PriorityCritical PriorityTier = "critical"
)

type RepoSnapshot struct {
	Owner      string    `json:"owner"`
	Name       string    `json:"name"`
	URL        string    `json:"url"`
	IsPrivate  bool      `json:"is_private"`
	UpdatedAt  time.Time `json:"updated_at"`
	DefaultRef string    `json:"default_ref"`
}

type IssueSnapshot struct {
	Owner       string     `json:"owner"`
	Repo        string     `json:"repo"`
	Number      int        `json:"number"`
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	State       string     `json:"state"`
	Author      string     `json:"author"`
	Assignees   []string   `json:"assignees"`
	Labels      []string   `json:"labels"`
	Body        string     `json:"body"`
	BoardStatus string     `json:"board_status,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
	Repository  string     `json:"repository"`
}

type PRSnapshot struct {
	Owner              string     `json:"owner"`
	Repo               string     `json:"repo"`
	Number             int        `json:"number"`
	Title              string     `json:"title"`
	URL                string     `json:"url"`
	State              string     `json:"state"`
	Author             string     `json:"author"`
	MergedBy           string     `json:"merged_by,omitempty"`
	ClosingIssues      []int      `json:"closing_issues,omitempty"`
	Assignees          []string   `json:"assignees"`
	RequestedReviewers []string   `json:"requested_reviewers,omitempty"`
	IsDraft            bool       `json:"is_draft"`
	ReviewDecision     string     `json:"review_decision"`
	BoardStatus        string     `json:"board_status,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	ClosedAt           *time.Time `json:"closed_at,omitempty"`
	MergedAt           *time.Time `json:"merged_at,omitempty"`
	Repository         string     `json:"repository"`
}

type WorkflowSnapshot struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	URL        string    `json:"url"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	RunNumber  int       `json:"run_number"`
	HeadBranch string    `json:"head_branch"`
	Event      string    `json:"event"`
}

type ProjectFieldOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ProjectField struct {
	ID      string               `json:"id"`
	Name    string               `json:"name"`
	Type    string               `json:"type"`
	Options []ProjectFieldOption `json:"options,omitempty"`
}

type ProjectSchema struct {
	Owner  string         `json:"owner"`
	Number int            `json:"number"`
	Title  string         `json:"title"`
	URL    string         `json:"url"`
	Fields []ProjectField `json:"fields"`
}

type ProjectItem struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Title        string            `json:"title"`
	URL          string            `json:"url"`
	Repo         string            `json:"repo"`
	Owner        string            `json:"owner"`
	Number       int               `json:"number"`
	State        string            `json:"state"`
	Assignees    []string          `json:"assignees"`
	FieldValues  map[string]string `json:"field_values"`
	UpdatedAt    time.Time         `json:"updated_at"`
	Status       StatusTier        `json:"status"`
	Priority     PriorityTier      `json:"priority"`
	Blocked      bool              `json:"blocked"`
	AssignedToMe bool              `json:"assigned_to_me"`
	BoardStatus  string            `json:"board_status,omitempty"`
}

type ActivityEvent struct {
	Repo        string    `json:"repo"`
	Owner       string    `json:"owner"`
	Number      int       `json:"number"`
	Kind        string    `json:"kind"`
	EventType   string    `json:"event_type"`
	Actor       string    `json:"actor"`
	OccurredAt  time.Time `json:"occurred_at"`
	Preview     string    `json:"preview,omitempty"`
	Path        string    `json:"path,omitempty"`
	RequestedTo []string  `json:"requested_to,omitempty"`
}

type NotificationThread struct {
	ID        string    `json:"id"`
	Repo      string    `json:"repo"`
	Owner     string    `json:"owner"`
	Number    int       `json:"number"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Reason    string    `json:"reason"`
	UpdatedAt time.Time `json:"updated_at"`
	Unread    bool      `json:"unread"`
}

type CatchUpEntry struct {
	Repo                string    `json:"repo"`
	Owner               string    `json:"owner"`
	Number              int       `json:"number"`
	Kind                string    `json:"kind"`
	Title               string    `json:"title"`
	URL                 string    `json:"url"`
	LatestEvent         string    `json:"latest_event"`
	LatestActor         string    `json:"latest_actor"`
	LatestAt            time.Time `json:"latest_at"`
	Involvement         []string  `json:"involvement,omitempty"`
	NotificationReasons []string  `json:"notification_reasons,omitempty"`
	Preview             string    `json:"preview,omitempty"`
	PreviewSource       string    `json:"preview_source,omitempty"` // "exact", "recovered", "notification_only"
	EventCount          int       `json:"event_count"`
	State               string    `json:"state,omitempty"`
	BoardStatus         string    `json:"board_status,omitempty"`
	ReviewNeeded        bool      `json:"review_needed,omitempty"`
	HasDiscussion       bool      `json:"has_discussion,omitempty"`
	HasClosure          bool      `json:"has_closure,omitempty"`
}

// WatchedItemEntry holds activity for a single watched issue or PR.
type WatchedItemEntry struct {
	Alias       string    `json:"alias"`
	Owner       string    `json:"owner"`
	Repo        string    `json:"repo"`
	Number      int       `json:"number"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	State       string    `json:"state,omitempty"`
	LatestEvent string    `json:"latest_event"`
	LatestActor string    `json:"latest_actor"`
	LatestAt    time.Time `json:"latest_at"`
	Preview     string    `json:"preview,omitempty"`
	EventCount  int       `json:"event_count"`
}

type CatchUpReport struct {
	GeneratedAt        time.Time          `json:"generated_at"`
	Target             string             `json:"target"`
	TargetKind         string             `json:"target_kind"`
	Since              string             `json:"since"`
	Partial            bool               `json:"partial"`
	DiagnosticsSummary []string           `json:"diagnostics_summary,omitempty"`
	Warnings           []string           `json:"warnings,omitempty"`
	Entries            []CatchUpEntry     `json:"entries"`
	WatchedItems       []WatchedItemEntry `json:"watched_items,omitempty"`
}

type ActivityEntry struct {
	Repo        string    `json:"repo"`
	Owner       string    `json:"owner"`
	Number      int       `json:"number"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	State       string    `json:"state,omitempty"`
	BoardStatus string    `json:"board_status,omitempty"`
	EventType   string    `json:"event_type"`
	Actor       string    `json:"actor"`
	OccurredAt  time.Time `json:"occurred_at"`
	Preview     string    `json:"preview,omitempty"`
	Path        string    `json:"path,omitempty"`
}

type ActivityReport struct {
	GeneratedAt  time.Time          `json:"generated_at"`
	Target       string             `json:"target"`
	TargetKind   string             `json:"target_kind"`
	Since        string             `json:"since"`
	Partial      bool               `json:"partial"`
	Warnings     []string           `json:"warnings,omitempty"`
	Entries      []ActivityEntry    `json:"entries"`
	WatchedItems []WatchedItemEntry `json:"watched_items,omitempty"`
}

type ManagerReport struct {
	GeneratedAt  time.Time          `json:"generated_at"`
	Target       string             `json:"target"`
	TargetKind   string             `json:"target_kind"`
	Since        string             `json:"since,omitempty"`
	Partial      bool               `json:"partial"`
	Warnings     []string           `json:"warnings,omitempty"`
	Summary      map[string]int     `json:"summary"`
	Items        []ProjectItem      `json:"items"`
	Issues       []IssueSnapshot    `json:"issues"`
	PRs          []PRSnapshot       `json:"prs"`
	ReviewNeeded []PRSnapshot       `json:"review_needed,omitempty"`
	Workflows    []WorkflowSnapshot `json:"workflows"`
}

type NextReport struct {
	GeneratedAt time.Time     `json:"generated_at"`
	Target      string        `json:"target"`
	TargetKind  string        `json:"target_kind"`
	Since       string        `json:"since,omitempty"`
	Warnings    []string      `json:"warnings,omitempty"`
	Sections    []NextSection `json:"sections"`
}

type NextSection struct {
	Name  string        `json:"name"`
	Items []ProjectItem `json:"items"`
}
