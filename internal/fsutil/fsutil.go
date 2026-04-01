// Package fsutil provides safe filesystem helpers for Ferret output files.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AtomicWriteFile writes data to path using a temp-file + fsync + rename
// pattern so that readers never see a partially written file.
func AtomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fsutil: create dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".ferret-tmp-*")
	if err != nil {
		return fmt.Errorf("fsutil: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Always clean up the temp file if something goes wrong.
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsutil: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsutil: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fsutil: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("fsutil: rename to %s: %w", path, err)
	}
	success = true
	return nil
}

// ResolveSafeWritePath resolves path and verifies it is inside root.
// It rejects paths that contain ".." segments or that resolve (after following
// symlinks at each component) to a location outside root.
// root itself must already exist and be a real directory.
func ResolveSafeWritePath(path, root string) (string, error) {
	// Reject any ".." components before resolving.
	clean := filepath.Clean(path)
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == ".." {
			return "", fmt.Errorf("fsutil: path %q contains '..' segment", path)
		}
	}

	absRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("fsutil: resolve root %s: %w", root, err)
	}

	// Resolve as many leading components as exist to detect symlink escapes.
	// We walk from the root downward so that each resolved prefix is anchored.
	absPath := filepath.Join(absRoot, clean)

	// Walk each existing component to detect symlinks that escape root.
	if err := checkSymlinkEscape(absPath, absRoot); err != nil {
		return "", err
	}

	return absPath, nil
}

// checkSymlinkEscape evaluates symlinks for each directory component of path
// that is a subdirectory of root (i.e. after we have passed root in the walk),
// verifying that no symlink leads outside root.
func checkSymlinkEscape(path, root string) error {
	// path should already start with root (it is built as filepath.Join(root, rel)).
	// We only need to evaluate symlinks for components *inside* root.
	// rel is the portion of path after root.
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("fsutil: cannot compute relative path: %w", err)
	}
	// Walk each component of rel starting from root.
	parts := strings.Split(rel, string(filepath.Separator))
	current := root
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		next := filepath.Join(current, part)
		resolved, err := filepath.EvalSymlinks(next)
		if err != nil {
			// Component doesn't exist yet — nothing to follow, stop here.
			break
		}
		// Check that the resolved path is still inside root.
		if !strings.HasPrefix(resolved+string(filepath.Separator), root+string(filepath.Separator)) &&
			resolved != root {
			return fmt.Errorf("fsutil: path %q resolves outside root %q via symlink", path, root)
		}
		current = next
	}
	return nil
}
