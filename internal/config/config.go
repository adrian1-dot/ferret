package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/adrian1-dot/ferret/internal/fsutil"
	"gopkg.in/yaml.v3"
)

const (
	DefaultConfigPath         = ".ferret/config.yaml"
	DefaultOutputDir          = ".ferret"
	DefaultPlanDir            = ".ferret/plans"
	DefaultCatchUpExpandOrder = "balanced"
)

type Config struct {
	Version  int      `yaml:"version" json:"version"`
	Defaults Defaults `yaml:"defaults" json:"defaults"`
	Watch    Watch    `yaml:"watch" json:"watch"`
}

type Defaults struct {
	Host      string          `yaml:"host" json:"host"`
	OutputDir string          `yaml:"output_dir" json:"output_dir"`
	PlanDir   string          `yaml:"plan_dir" json:"plan_dir"`
	Cache     CacheConfig     `yaml:"cache" json:"cache"`
	CatchUp   CatchUpDefaults `yaml:"catch_up" json:"catch_up"`
}

type CacheConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	TTL     string `yaml:"ttl" json:"ttl"`
}

type CatchUpDefaults struct {
	ExpandOrder  string `yaml:"expand_order,omitempty" json:"expand_order,omitempty"`
	ReviewBudget int    `yaml:"review_budget,omitempty" json:"review_budget,omitempty"`
}

type Watch struct {
	Repos    []RepoWatch    `yaml:"repos" json:"repos"`
	Projects []ProjectWatch `yaml:"projects" json:"projects"`
	Items    []ItemWatch    `yaml:"items" json:"items"`
}

// ItemWatch tracks a single issue or PR regardless of whether its repo is watched.
type ItemWatch struct {
	Alias  string `yaml:"alias" json:"alias"`
	Owner  string `yaml:"owner" json:"owner"`
	Repo   string `yaml:"repo" json:"repo"`
	Number int    `yaml:"number" json:"number"`
	Kind   string `yaml:"kind" json:"kind"` // "issue" or "pr"
}

type WatchDefaults struct {
	Filters []string `yaml:"filters,omitempty" json:"filters,omitempty"`
	Since   string   `yaml:"since,omitempty" json:"since,omitempty"`
}

type RepoWatch struct {
	Alias    string        `yaml:"alias" json:"alias"`
	Owner    string        `yaml:"owner" json:"owner"`
	Name     string        `yaml:"name" json:"name"`
	Defaults WatchDefaults `yaml:"defaults,omitempty" json:"defaults,omitempty"`
}

type ProjectOutput struct {
	PlanFile string `yaml:"plan_file,omitempty" json:"plan_file,omitempty"`
}

type ProjectWatch struct {
	Alias       string        `yaml:"alias" json:"alias"`
	Owner       string        `yaml:"owner" json:"owner"`
	Number      int           `yaml:"number" json:"number"`
	LinkedRepos []string      `yaml:"linked_repos,omitempty" json:"linked_repos,omitempty"`
	StatusField string        `yaml:"status_field,omitempty" json:"status_field,omitempty"`
	Defaults    WatchDefaults `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	Output      ProjectOutput `yaml:"output,omitempty" json:"output,omitempty"`
}

type Store interface {
	Load(ctx context.Context) (*Config, error)
	Save(ctx context.Context, cfg *Config) error
	Update(ctx context.Context, mutate func(*Config) error) error
}

type FileStore struct {
	Path string
}

func Default() *Config {
	return &Config{
		Version: 1,
		Defaults: Defaults{
			Host:      "github.com",
			OutputDir: DefaultOutputDir,
			PlanDir:   DefaultPlanDir,
			Cache: CacheConfig{
				Enabled: true,
				TTL:     "15m",
			},
			CatchUp: CatchUpDefaults{
				ExpandOrder: DefaultCatchUpExpandOrder,
			},
		},
	}
}

func (s FileStore) Load(_ context.Context) (*Config, error) {
	path := s.path()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, Validate(cfg)
}

func (s FileStore) Save(_ context.Context, cfg *Config) error {
	if err := Validate(cfg); err != nil {
		return err
	}
	path := s.path()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return fsutil.AtomicWriteFile(path, data)
}

func (s FileStore) Update(ctx context.Context, mutate func(*Config) error) error {
	path := s.path()
	lockPath := path + ".lock"
	lockFile, err := acquireLock(ctx, lockPath)
	if err != nil {
		return err
	}
	defer releaseLock(lockFile)

	cfg, err := s.Load(ctx)
	if err != nil {
		return err
	}
	if err := mutate(cfg); err != nil {
		return err
	}
	return s.Save(ctx, cfg)
}

func (s FileStore) path() string {
	if s.Path == "" {
		return ExpandPath(DefaultConfigPath)
	}
	return ExpandPath(s.Path)
}

func ExpandPath(path string) string {
	if path == "" {
		return ""
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	prefix := "~" + string(os.PathSeparator)
	if strings.HasPrefix(path, prefix) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[len(prefix):])
		}
	}
	return path
}

func Validate(cfg *Config) error {
	if err := validateOutputPath("defaults.output_dir", cfg.Defaults.OutputDir); err != nil {
		return err
	}
	if err := validateOutputPath("defaults.plan_dir", cfg.Defaults.PlanDir); err != nil {
		return err
	}
	if _, err := NormalizeCatchUpExpandOrder(cfg.Defaults.CatchUp.ExpandOrder); err != nil {
		return fmt.Errorf("defaults.catch_up.expand_order: %w", err)
	}
	if cfg.Defaults.CatchUp.ReviewBudget < 0 {
		return fmt.Errorf("defaults.catch_up.review_budget: unsupported value %d (expected 0 or greater)", cfg.Defaults.CatchUp.ReviewBudget)
	}
	aliases := map[string]string{}
	for _, r := range cfg.Watch.Repos {
		if r.Alias == "" || r.Owner == "" || r.Name == "" {
			return fmt.Errorf("repo watch requires alias, owner, and name")
		}
		if prev, ok := aliases[r.Alias]; ok {
			return fmt.Errorf("alias %q already used by %s", r.Alias, prev)
		}
		aliases[r.Alias] = "repo"
	}
	for _, p := range cfg.Watch.Projects {
		if p.Alias == "" || p.Owner == "" || p.Number == 0 {
			return fmt.Errorf("project watch requires alias, owner, and number")
		}
		if prev, ok := aliases[p.Alias]; ok {
			return fmt.Errorf("alias %q already used by %s", p.Alias, prev)
		}
		aliases[p.Alias] = "project"
		for _, alias := range p.LinkedRepos {
			if !hasRepoAlias(cfg, alias) {
				return fmt.Errorf("project %q links unknown repo alias %q", p.Alias, alias)
			}
		}
		if p.Output.PlanFile != "" {
			if err := validateOutputPath("project.output.plan_file", p.Output.PlanFile); err != nil {
				return err
			}
		}
	}
	for _, iw := range cfg.Watch.Items {
		if iw.Alias == "" || iw.Owner == "" || iw.Repo == "" || iw.Number == 0 {
			return fmt.Errorf("item watch requires alias, owner, repo, and number")
		}
		if iw.Kind != "issue" && iw.Kind != "pr" {
			return fmt.Errorf("item watch %q: kind must be \"issue\" or \"pr\", got %q", iw.Alias, iw.Kind)
		}
		if prev, ok := aliases[iw.Alias]; ok {
			return fmt.Errorf("alias %q already used by %s", iw.Alias, prev)
		}
		aliases[iw.Alias] = "item"
	}
	return nil
}

// validateOutputPath rejects paths that contain ".." segments.
// The field parameter names the offending config field for the error message.
func validateOutputPath(field, path string) error {
	if path == "" {
		return nil
	}
	// Split on both slash styles and check each component.
	// filepath.Clean normalises the path; we then check each segment.
	clean := filepath.Clean(filepath.ToSlash(path))
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return fmt.Errorf("config field %q: path %q contains \"..\" segment", field, path)
		}
	}
	return nil
}

func hasRepoAlias(cfg *Config, alias string) bool {
	for _, r := range cfg.Watch.Repos {
		if r.Alias == alias {
			return true
		}
	}
	return false
}

func NormalizeCatchUpExpandOrder(order string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(order)) {
	case "", DefaultCatchUpExpandOrder:
		return DefaultCatchUpExpandOrder, nil
	case "recency":
		return "recency", nil
	default:
		return "", fmt.Errorf("unsupported value %q (expected balanced or recency)", order)
	}
}

func hasProjectAlias(cfg *Config, alias string) bool {
	for _, p := range cfg.Watch.Projects {
		if p.Alias == alias {
			return true
		}
	}
	return false
}

func hasItemAlias(cfg *Config, alias string) bool {
	for _, i := range cfg.Watch.Items {
		if i.Alias == alias {
			return true
		}
	}
	return false
}

func hasAnyAlias(cfg *Config, alias string) bool {
	return hasRepoAlias(cfg, alias) || hasProjectAlias(cfg, alias) || hasItemAlias(cfg, alias)
}

func AddItemWatch(cfg *Config, watch ItemWatch) error {
	if hasAnyAlias(cfg, watch.Alias) {
		return fmt.Errorf("alias %q already exists", watch.Alias)
	}
	cfg.Watch.Items = append(cfg.Watch.Items, watch)
	return nil
}

func RemoveItemWatch(cfg *Config, aliasOrRef string) error {
	for i, iw := range cfg.Watch.Items {
		if iw.Alias == aliasOrRef {
			cfg.Watch.Items = slices.Delete(cfg.Watch.Items, i, i+1)
			return nil
		}
	}
	return fmt.Errorf("unknown item alias %q", aliasOrRef)
}

// RemoveItemWatchByOwnerRepoNumber removes a watched item by owner, repo, and number.
// kind is matched case-insensitively ("issue" or "pr").
func RemoveItemWatchByOwnerRepoNumber(cfg *Config, owner, repo string, number int, kind string) error {
	for i, iw := range cfg.Watch.Items {
		if strings.EqualFold(iw.Owner, owner) && strings.EqualFold(iw.Repo, repo) && iw.Number == number && strings.EqualFold(iw.Kind, kind) {
			cfg.Watch.Items = slices.Delete(cfg.Watch.Items, i, i+1)
			return nil
		}
	}
	return fmt.Errorf("no watched %s found for %s/%s#%d", kind, owner, repo, number)
}

func ResolveItem(cfg *Config, alias string) (*ItemWatch, error) {
	for _, iw := range cfg.Watch.Items {
		if iw.Alias == alias {
			cp := iw
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("unknown item alias %q", alias)
}

func ResolveProject(cfg *Config, alias string) (*ProjectWatch, error) {
	for _, p := range cfg.Watch.Projects {
		if p.Alias == alias {
			cp := p
			if cp.Output.PlanFile == "" {
				cp.Output.PlanFile = filepath.Join(ExpandPath(cfg.Defaults.PlanDir), alias+".md")
			} else {
				cp.Output.PlanFile = ExpandPath(cp.Output.PlanFile)
			}
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("unknown project alias %q", alias)
}

func ResolveRepo(cfg *Config, alias string) (*RepoWatch, error) {
	for _, r := range cfg.Watch.Repos {
		if r.Alias == alias {
			cr := r
			return &cr, nil
		}
	}
	return nil, fmt.Errorf("unknown repo alias %q", alias)
}

func AddProjectWatch(cfg *Config, watch ProjectWatch) error {
	if hasAnyAlias(cfg, watch.Alias) {
		return fmt.Errorf("alias %q already exists", watch.Alias)
	}
	cfg.Watch.Projects = append(cfg.Watch.Projects, watch)
	return nil
}

func AddRepoWatch(cfg *Config, watch RepoWatch) error {
	if hasAnyAlias(cfg, watch.Alias) {
		return fmt.Errorf("alias %q already exists", watch.Alias)
	}
	cfg.Watch.Repos = append(cfg.Watch.Repos, watch)
	return nil
}

func RemoveProjectWatch(cfg *Config, alias string) error {
	idx := -1
	for i, p := range cfg.Watch.Projects {
		if p.Alias == alias {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("unknown project alias %q", alias)
	}
	cfg.Watch.Projects = slices.Delete(cfg.Watch.Projects, idx, idx+1)
	return nil
}

func RemoveRepoWatch(cfg *Config, alias string) error {
	idx := -1
	for i, r := range cfg.Watch.Repos {
		if r.Alias == alias {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("unknown repo alias %q", alias)
	}
	cfg.Watch.Repos = slices.Delete(cfg.Watch.Repos, idx, idx+1)
	for i := range cfg.Watch.Projects {
		var next []string
		for _, linked := range cfg.Watch.Projects[i].LinkedRepos {
			if linked != alias {
				next = append(next, linked)
			}
		}
		cfg.Watch.Projects[i].LinkedRepos = next
	}
	return nil
}
