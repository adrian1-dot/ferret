package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := FileStore{Path: filepath.Join(dir, "config.yaml")}
	cfg := Default()
	if err := AddRepoWatch(cfg, RepoWatch{Alias: "api", Owner: "acme", Name: "api"}); err != nil {
		t.Fatal(err)
	}
	if err := AddProjectWatch(cfg, ProjectWatch{Alias: "delivery", Owner: "acme", Number: 12, LinkedRepos: []string{"api"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Watch.Repos) != 1 || got.Watch.Repos[0].Alias != "api" {
		t.Fatalf("unexpected repos: %#v", got.Watch.Repos)
	}
	if len(got.Watch.Projects) != 1 || got.Watch.Projects[0].Alias != "delivery" {
		t.Fatalf("unexpected projects: %#v", got.Watch.Projects)
	}
}

func TestValidateRejectsDoubleDotInOutputDir(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Defaults.OutputDir = "../../etc"
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for '..' in output_dir")
	}
	if !strings.Contains(err.Error(), "output_dir") {
		t.Fatalf("expected error to name the field, got: %s", err.Error())
	}
}

func TestValidateAcceptsNormalOutputDir(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Defaults.OutputDir = ".ferret/output"
	if err := Validate(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRejectsInvalidCatchUpExpandOrder(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Defaults.CatchUp.ExpandOrder = "mine"
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for invalid catch-up expand order")
	}
	if !strings.Contains(err.Error(), "expand_order") {
		t.Fatalf("expected error to name the field, got: %s", err.Error())
	}
}

func TestNormalizeCatchUpExpandOrder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"", DefaultCatchUpExpandOrder},
		{"balanced", DefaultCatchUpExpandOrder},
		{"BALANCED", DefaultCatchUpExpandOrder},
		{"recency", "recency"},
	}
	for _, tc := range cases {
		got, err := NormalizeCatchUpExpandOrder(tc.in)
		if err != nil {
			t.Fatalf("NormalizeCatchUpExpandOrder(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("NormalizeCatchUpExpandOrder(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := NormalizeCatchUpExpandOrder("watched"); err == nil {
		t.Fatal("expected invalid expansion order to fail")
	}
}

func TestValidateItemWatchRequiresKind(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Watch.Items = []ItemWatch{
		{Alias: "foo", Owner: "acme", Repo: "api", Number: 1, Kind: "unknown"},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for invalid item kind")
	}
	if !strings.Contains(err.Error(), "kind") {
		t.Fatalf("expected error to mention kind, got: %s", err.Error())
	}
}

func TestValidateItemWatchAliasUniqueness(t *testing.T) {
	t.Parallel()
	cfg := Default()
	if err := AddRepoWatch(cfg, RepoWatch{Alias: "api", Owner: "acme", Name: "api"}); err != nil {
		t.Fatal(err)
	}
	// Adding item with same alias as repo should fail.
	err := AddItemWatch(cfg, ItemWatch{Alias: "api", Owner: "acme", Repo: "api", Number: 1, Kind: "issue"})
	if err == nil {
		t.Fatal("expected alias conflict error")
	}
}

func TestAddItemWatchAndRemove(t *testing.T) {
	t.Parallel()
	cfg := Default()
	if err := AddItemWatch(cfg, ItemWatch{Alias: "my-issue", Owner: "acme", Repo: "api", Number: 42, Kind: "issue"}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Watch.Items) != 1 {
		t.Fatalf("expected 1 item watch, got %d", len(cfg.Watch.Items))
	}
	if err := RemoveItemWatch(cfg, "my-issue"); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Watch.Items) != 0 {
		t.Fatalf("expected 0 item watches after removal, got %d", len(cfg.Watch.Items))
	}
}

func TestFileStateStoreRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := FileStateStore{Path: filepath.Join(dir, "state.yaml")}
	state := DefaultState()
	state.Cursors["catch-up:all"] = "2026-03-28T10:00:00Z"
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Cursors["catch-up:all"] != "2026-03-28T10:00:00Z" {
		t.Fatalf("unexpected cursors: %#v", got.Cursors)
	}
}

func TestExpandPathExpandsTildePrefix(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got := ExpandPath("~/ferret/config.yaml")
	want := filepath.Join(home, "ferret", "config.yaml")
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestFileStoreUpdateSerializesConcurrentMutations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := FileStore{Path: filepath.Join(dir, "config.yaml")}

	aliases := []string{"api", "web", "atlas", "ferret"}
	var wg sync.WaitGroup
	for _, alias := range aliases {
		alias := alias
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.Update(context.Background(), func(cfg *Config) error {
				return AddRepoWatch(cfg, RepoWatch{Alias: alias, Owner: "acme", Name: alias})
			}); err != nil {
				t.Errorf("update %s: %v", alias, err)
			}
		}()
	}
	wg.Wait()

	cfg, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Watch.Repos) != len(aliases) {
		t.Fatalf("expected %d repos after concurrent updates, got %#v", len(aliases), cfg.Watch.Repos)
	}
}
