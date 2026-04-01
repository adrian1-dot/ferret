package github

import "testing"

func TestClassifyErrorKinds(t *testing.T) {
	t.Parallel()
	if !IsKind(classifyError("requires project scope", []string{"api"}), ErrMissingScope) {
		t.Fatal("expected missing scope classification")
	}
	if !IsKind(classifyError("authentication failed", []string{"api"}), ErrAuth) {
		t.Fatal("expected auth classification")
	}
}

func TestClassifyErrorTreatsUserResolutionAsNotFound(t *testing.T) {
	t.Parallel()
	err := classifyError("Could not resolve to a User with the login of 'Andamio-Platform'.", []string{"api", "graphql"})
	if !IsKind(err, ErrNotFound) {
		t.Fatalf("expected not_found classification, got %#v", err)
	}
}
