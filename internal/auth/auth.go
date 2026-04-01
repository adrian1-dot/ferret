package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ferretOAuthClientID is the GitHub OAuth App client ID for the device flow.
// Leave empty to fall back to PAT prompt. Set at build time:
//
//	go build -ldflags "-X github.com/adrian1-dot/ferret/internal/auth.ferretOAuthClientID=<id>" ./cmd/ferret
var ferretOAuthClientID = ""

// Source describes where a resolved token came from.
type Source string

const (
	SourceStored Source = "stored"  // loaded from ~/.ferret/auth.yaml
	SourceEnv    Source = "env"     // read from GITHUB_TOKEN env var
	SourceGH     Source = "gh"      // extracted from gh CLI
	SourcePAT    Source = "entered" // typed in by the user
)

// Result holds a resolved GitHub token and its source.
type Result struct {
	Token  string
	Source Source
}

type storedAuth struct {
	Token  string `yaml:"token"`
	Source Source `yaml:"source"`
}

// AuthFilePath returns the path to the stored auth file (~/.ferret/auth.yaml).
func AuthFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".ferret", "auth.yaml")
	}
	return filepath.Join(home, ".ferret", "auth.yaml")
}

// Load reads the stored token from ~/.ferret/auth.yaml.
// Returns an empty Result (no error) when the file does not exist or is empty.
func Load() (Result, error) {
	data, err := os.ReadFile(AuthFilePath())
	if errors.Is(err, os.ErrNotExist) {
		return Result{}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("read auth file: %w", err)
	}
	var s storedAuth
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Result{}, fmt.Errorf("parse auth file: %w", err)
	}
	if s.Token == "" {
		return Result{}, nil
	}
	return Result{Token: s.Token, Source: SourceStored}, nil
}

// Save writes a token to ~/.ferret/auth.yaml with 0600 permissions.
func Save(token string, source Source) error {
	path := AuthFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create auth dir: %w", err)
	}
	data, err := yaml.Marshal(storedAuth{Token: token, Source: source})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Logout removes the stored auth file. Not an error if the file does not exist.
func Logout() error {
	err := os.Remove(AuthFilePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Check returns whatever token is currently available without prompting the user.
// Used by doctor and other diagnostic paths.
func Check(ctx context.Context) Result {
	if res, err := Load(); err == nil && res.Token != "" {
		return res
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return Result{Token: token, Source: SourceEnv}
	}
	if token, err := ghToken(ctx); err == nil && token != "" {
		return Result{Token: token, Source: SourceGH}
	}
	return Result{}
}

// Resolve returns a valid GitHub token, prompting the user interactively when
// necessary. On first run it will ask before using an existing token from the
// environment or gh CLI, then save the answer so subsequent runs are silent.
//
// Resolution order:
//  1. ~/.ferret/auth.yaml — used silently (already consented)
//  2. GITHUB_TOKEN env var — asks for consent in interactive mode; auto-uses in CI
//  3. gh auth token — asks for consent in interactive mode; auto-uses in CI
//  4. PAT prompt (or OAuth device flow if ferretOAuthClientID is set)
func Resolve(ctx context.Context, w io.Writer, r io.Reader) (Result, error) {
	// 1. Stored token — always silent.
	if res, err := Load(); err == nil && res.Token != "" {
		return res, nil
	}

	interactive := isTerminal()
	scanner := bufio.NewScanner(r)

	// 2. GITHUB_TOKEN env var.
	if envToken := os.Getenv("GITHUB_TOKEN"); envToken != "" {
		if !interactive || confirm(w, scanner, "Found GITHUB_TOKEN in environment. Use it for Ferret?") {
			if err := Save(envToken, SourceEnv); err != nil {
				return Result{}, fmt.Errorf("save token: %w", err)
			}
			if interactive {
				fmt.Fprintln(w, "Token saved to ~/.ferret/auth.yaml")
			}
			return Result{Token: envToken, Source: SourceEnv}, nil
		}
	}

	// 3. gh auth token (only if gh is installed).
	if token, err := ghToken(ctx); err == nil && token != "" {
		if !interactive || confirm(w, scanner, "Found a token from the GitHub CLI (gh). Use it for Ferret?") {
			if err := Save(token, SourceGH); err != nil {
				return Result{}, fmt.Errorf("save token: %w", err)
			}
			if interactive {
				fmt.Fprintln(w, "Token saved to ~/.ferret/auth.yaml")
			}
			return Result{Token: token, Source: SourceGH}, nil
		}
	}

	// 4. Non-interactive: nothing left to try.
	if !interactive {
		return Result{}, fmt.Errorf("no GitHub token found; set GITHUB_TOKEN or run ferret interactively to authenticate")
	}

	// 4. Interactive: device flow or PAT prompt.
	if ferretOAuthClientID != "" {
		return runDeviceFlow(ctx, w, r, scanner)
	}
	return promptPAT(w, scanner)
}

// confirm prints a prompt and returns true unless the user types n or no.
func confirm(w io.Writer, scanner *bufio.Scanner, prompt string) bool {
	fmt.Fprintf(w, "%s [Y/n]: ", prompt)
	if !scanner.Scan() {
		return true // default yes on EOF
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer != "n" && answer != "no"
}

func ghToken(ctx context.Context) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func promptPAT(w io.Writer, scanner *bufio.Scanner) (Result, error) {
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "No GitHub token found.")
	fmt.Fprintln(w, "Create a Personal Access Token at: https://github.com/settings/tokens")
	fmt.Fprintln(w, "Required scopes: repo, read:org")
	fmt.Fprintln(w, "Add the project scope for GitHub Projects support.")
	fmt.Fprintln(w, "")
	fmt.Fprint(w, "Enter token: ")
	if !scanner.Scan() {
		return Result{}, fmt.Errorf("no token entered")
	}
	token := strings.TrimSpace(scanner.Text())
	if token == "" {
		return Result{}, fmt.Errorf("no token entered")
	}
	if err := Save(token, SourcePAT); err != nil {
		return Result{}, fmt.Errorf("save token: %w", err)
	}
	fmt.Fprintln(w, "Token saved to ~/.ferret/auth.yaml")
	return Result{Token: token, Source: SourcePAT}, nil
}

// runDeviceFlow runs the GitHub OAuth 2.0 device authorization flow.
// Only called when ferretOAuthClientID is configured.
func runDeviceFlow(ctx context.Context, w io.Writer, r io.Reader, scanner *bufio.Scanner) (Result, error) {
	// TODO: implement full OAuth device flow once an OAuth App is registered.
	// ferretOAuthClientID holds the client_id; no client_secret is required for device flow.
	// For now, fall back to PAT prompt.
	_ = ctx
	_ = r
	return promptPAT(w, scanner)
}

// isTerminal reports whether os.Stdin is connected to an interactive terminal.
func isTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
