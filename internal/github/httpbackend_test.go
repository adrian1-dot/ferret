package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type errReadCloser struct {
	err error
}

func (r errReadCloser) Read([]byte) (int, error) {
	return 0, r.err
}

func (r errReadCloser) Close() error {
	return nil
}

func TestListRepoPRsReviewFilterUsesReviewRequests(t *testing.T) {
	t.Parallel()

	backend := &HTTPBackend{
		token: "test-token",
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/user":
					return jsonResponse(http.StatusOK, `{"login":"alice"}`), nil
				case "/graphql":
					body, err := io.ReadAll(req.Body)
					if err != nil {
						t.Fatalf("read request body: %v", err)
					}
					query := string(body)
					if strings.Contains(query, "requestedReviewers") {
						t.Fatalf("unexpected legacy requestedReviewers field in query: %s", query)
					}
					if !strings.Contains(query, "reviewRequests(first: 20)") {
						t.Fatalf("expected reviewRequests in query: %s", query)
					}
					return jsonResponse(http.StatusOK, `{
						"data": {
							"repository": {
								"pullRequests": {
									"pageInfo": {"hasNextPage": false, "endCursor": ""},
									"nodes": [
										{
											"number": 12,
											"title": "Needs Alice review",
											"url": "https://example.test/pr/12",
											"state": "OPEN",
											"isDraft": false,
											"reviewDecision": "REVIEW_REQUIRED",
											"createdAt": "2026-03-30T10:00:00Z",
											"updatedAt": "2026-03-31T10:00:00Z",
											"closedAt": null,
											"mergedAt": null,
											"author": {"login": "bob"},
											"mergedBy": {"login": ""},
											"assignees": {"nodes": [{"login": "alice"}]},
											"reviewRequests": {
												"nodes": [
													{"requestedReviewer": {"__typename": "User", "login": "alice"}},
													{"requestedReviewer": {"__typename": "Team", "login": ""}}
												]
											},
											"closingIssuesReferences": {"nodes": [{"number": 99}]}
										},
										{
											"number": 13,
											"title": "Needs someone else",
											"url": "https://example.test/pr/13",
											"state": "OPEN",
											"isDraft": false,
											"reviewDecision": "REVIEW_REQUIRED",
											"createdAt": "2026-03-30T10:00:00Z",
											"updatedAt": "2026-03-31T10:00:00Z",
											"closedAt": null,
											"mergedAt": null,
											"author": {"login": "carol"},
											"mergedBy": {"login": ""},
											"assignees": {"nodes": []},
											"reviewRequests": {
												"nodes": [
													{"requestedReviewer": {"__typename": "User", "login": "dave"}}
												]
											},
											"closingIssuesReferences": {"nodes": []}
										}
									]
								}
							}
						}
					}`), nil
				default:
					t.Fatalf("unexpected request path: %s", req.URL.Path)
					return nil, nil
				}
			}),
		},
	}

	prs, err := backend.ListRepoPRs(context.Background(), "acme", "rocket", PRQuery{State: "open", Review: true})
	if err != nil {
		t.Fatalf("ListRepoPRs returned error: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR after review filter, got %d", len(prs))
	}
	if prs[0].Number != 12 {
		t.Fatalf("expected PR #12, got #%d", prs[0].Number)
	}
	if len(prs[0].ClosingIssues) != 1 || prs[0].ClosingIssues[0] != 99 {
		t.Fatalf("expected closing issue 99, got %#v", prs[0].ClosingIssues)
	}
}

func TestListRepoPRsFallsBackToRESTOnTransientGraphQLError(t *testing.T) {
	t.Parallel()

	var graphqlCalls atomic.Int32
	var pullsCalls atomic.Int32
	backend := &HTTPBackend{
		token: "test-token",
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/user":
					return jsonResponse(http.StatusOK, `{"login":"alice"}`), nil
				case "/graphql":
					graphqlCalls.Add(1)
					return jsonResponse(http.StatusBadGateway, `{"message":"Bad Gateway"}`), nil
				case "/repos/acme/rocket/pulls":
					pullsCalls.Add(1)
					if got := req.URL.Query().Get("state"); got != "open" {
						t.Fatalf("expected REST fallback to request open state, got %q", got)
					}
					return jsonResponse(http.StatusOK, `[
						{
							"number": 12,
							"title": "Needs Alice review",
							"html_url": "https://example.test/pr/12",
							"state": "open",
							"draft": false,
							"created_at": "2026-03-30T10:00:00Z",
							"updated_at": "2026-03-31T10:00:00Z",
							"closed_at": null,
							"merged_at": null,
							"user": {"login": "bob"},
							"merged_by": null,
							"assignees": [{"login": "alice"}],
							"requested_reviewers": [{"login": "alice"}]
						},
						{
							"number": 13,
							"title": "Needs someone else",
							"html_url": "https://example.test/pr/13",
							"state": "open",
							"draft": false,
							"created_at": "2026-03-30T10:00:00Z",
							"updated_at": "2026-03-31T10:00:00Z",
							"closed_at": null,
							"merged_at": null,
							"user": {"login": "carol"},
							"merged_by": null,
							"assignees": [],
							"requested_reviewers": [{"login": "dave"}]
						}
					]`), nil
				default:
					t.Fatalf("unexpected request path: %s", req.URL.Path)
					return nil, nil
				}
			}),
		},
	}

	prs, err := backend.ListRepoPRs(context.Background(), "acme", "rocket", PRQuery{State: "open", Review: true})
	if err != nil {
		t.Fatalf("ListRepoPRs returned error: %v", err)
	}
	if graphqlCalls.Load() != 1 {
		t.Fatalf("expected 1 GraphQL call, got %d", graphqlCalls.Load())
	}
	if pullsCalls.Load() != 1 {
		t.Fatalf("expected 1 REST pulls call, got %d", pullsCalls.Load())
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR after review filter, got %d", len(prs))
	}
	if prs[0].Number != 12 {
		t.Fatalf("expected PR #12, got #%d", prs[0].Number)
	}
	if prs[0].ReviewDecision != "" {
		t.Fatalf("expected empty review decision in degraded REST fallback, got %q", prs[0].ReviewDecision)
	}
	if len(prs[0].ClosingIssues) != 0 {
		t.Fatalf("expected no closing issues in degraded REST fallback, got %#v", prs[0].ClosingIssues)
	}
}

func TestListRepoPRsFallsBackToRESTOnGraphQLReadResponseError(t *testing.T) {
	t.Parallel()

	var graphqlCalls atomic.Int32
	var pullsCalls atomic.Int32
	backend := &HTTPBackend{
		token: "test-token",
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/graphql":
					graphqlCalls.Add(1)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       errReadCloser{err: errors.New("stream error: stream ID 17; CANCEL; received from peer")},
					}, nil
				case "/repos/acme/rocket/pulls":
					pullsCalls.Add(1)
					return jsonResponse(http.StatusOK, `[
						{
							"number": 12,
							"title": "Recovered through REST fallback",
							"html_url": "https://example.test/pr/12",
							"state": "open",
							"draft": false,
							"created_at": "2026-03-30T10:00:00Z",
							"updated_at": "2026-03-31T10:00:00Z",
							"closed_at": null,
							"merged_at": null,
							"user": {"login": "bob"},
							"merged_by": null,
							"assignees": [],
							"requested_reviewers": []
						}
					]`), nil
				default:
					t.Fatalf("unexpected request path: %s", req.URL.Path)
					return nil, nil
				}
			}),
		},
	}

	prs, err := backend.ListRepoPRs(context.Background(), "acme", "rocket", PRQuery{State: "open"})
	if err != nil {
		t.Fatalf("ListRepoPRs returned error: %v", err)
	}
	if graphqlCalls.Load() != 1 {
		t.Fatalf("expected 1 GraphQL call, got %d", graphqlCalls.Load())
	}
	if pullsCalls.Load() != 1 {
		t.Fatalf("expected 1 REST fallback call, got %d", pullsCalls.Load())
	}
	if len(prs) != 1 || prs[0].Number != 12 {
		t.Fatalf("unexpected REST fallback PRs: %#v", prs)
	}
}

func TestListRepoPRsRESTFallbackStopsPagingPastSinceWindow(t *testing.T) {
	t.Parallel()

	var pullsCalls atomic.Int32
	backend := &HTTPBackend{
		token: "test-token",
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/graphql":
					return jsonResponse(http.StatusBadGateway, `{"message":"Bad Gateway"}`), nil
				case "/repos/acme/rocket/pulls":
					pullsCalls.Add(1)
					if pullsCalls.Load() > 1 {
						t.Fatalf("expected REST fallback pagination to stop after the first page")
					}
					resp := jsonResponse(http.StatusOK, `[
						{
							"number": 12,
							"title": "Recent PR",
							"html_url": "https://example.test/pr/12",
							"state": "open",
							"draft": false,
							"created_at": "2026-03-30T10:00:00Z",
							"updated_at": "2026-03-31T10:00:00Z",
							"closed_at": null,
							"merged_at": null,
							"user": {"login": "bob"},
							"merged_by": null,
							"assignees": [],
							"requested_reviewers": []
						},
						{
							"number": 11,
							"title": "Old PR",
							"html_url": "https://example.test/pr/11",
							"state": "closed",
							"draft": false,
							"created_at": "2026-01-01T10:00:00Z",
							"updated_at": "2026-01-02T10:00:00Z",
							"closed_at": "2026-01-02T10:00:00Z",
							"merged_at": null,
							"user": {"login": "carol"},
							"merged_by": null,
							"assignees": [],
							"requested_reviewers": []
						}
					]`)
					resp.Header.Set("Link", `<https://api.github.com/repos/acme/rocket/pulls?page=2>; rel="next"`)
					return resp, nil
				default:
					t.Fatalf("unexpected request path: %s", req.URL.Path)
					return nil, nil
				}
			}),
		},
	}

	prs, err := backend.ListRepoPRs(context.Background(), "acme", "rocket", PRQuery{State: "all", Since: "2026-03-01T00:00:00Z"})
	if err != nil {
		t.Fatalf("ListRepoPRs returned error: %v", err)
	}
	if pullsCalls.Load() != 1 {
		t.Fatalf("expected exactly one REST page fetch, got %d", pullsCalls.Load())
	}
	if len(prs) != 1 || prs[0].Number != 12 {
		t.Fatalf("unexpected PRs after early-stop fallback: %#v", prs)
	}
}

func TestListRepoPRsDoesNotFallbackOnAuthGraphQLError(t *testing.T) {
	t.Parallel()

	var pullsCalls atomic.Int32
	backend := &HTTPBackend{
		token: "test-token",
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/graphql":
					return jsonResponse(http.StatusUnauthorized, `{"message":"Bad credentials"}`), nil
				case "/repos/acme/rocket/pulls":
					pullsCalls.Add(1)
					t.Fatalf("unexpected REST fallback for auth failure")
					return nil, nil
				default:
					t.Fatalf("unexpected request path: %s", req.URL.Path)
					return nil, nil
				}
			}),
		},
	}

	_, err := backend.ListRepoPRs(context.Background(), "acme", "rocket", PRQuery{State: "open"})
	if err == nil {
		t.Fatalf("expected auth error")
	}
	if !IsKind(err, ErrAuth) {
		t.Fatalf("expected auth error kind, got %v", err)
	}
	if pullsCalls.Load() != 0 {
		t.Fatalf("expected no REST fallback calls, got %d", pullsCalls.Load())
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
