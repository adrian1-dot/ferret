package fsutil_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/adrian1-dot/ferret/internal/fsutil"
)

func TestAtomicWriteFileRoundtrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "out.md")
	data := []byte("hello ferret\n")
	if err := fsutil.AtomicWriteFile(path, data); err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("expected %q, got %q", data, got)
	}
}

func TestAtomicWriteFileCreatesParentDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "out.md")
	if err := fsutil.AtomicWriteFile(path, []byte("x")); err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
}

func TestResolveSafeWritePathRejectsDoubleDot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := fsutil.ResolveSafeWritePath("../../etc/passwd", dir)
	if err == nil {
		t.Fatal("expected error for path with '..'")
	}
}

func TestResolveSafeWritePathAcceptsPathInsideRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	resolved, err := fsutil.ResolveSafeWritePath("sub/file.md", dir)
	if err != nil {
		t.Fatalf("ResolveSafeWritePath: %v", err)
	}
	want := filepath.Join(dir, "sub", "file.md")
	if resolved != want {
		t.Fatalf("expected %q, got %q", want, resolved)
	}
}

func TestResolveSafeWritePathRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	outside := t.TempDir()
	// Create a symlink inside dir that points outside.
	link := filepath.Join(dir, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	_, err := fsutil.ResolveSafeWritePath("escape/secret.md", dir)
	if err == nil {
		t.Fatal("expected error for symlink that escapes root")
	}
}

func TestResolveSafeWritePathRejectsDoubleDotInMiddle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := fsutil.ResolveSafeWritePath("a/../../../etc/passwd", dir)
	if err == nil {
		t.Fatal("expected error for '..' in middle of path")
	}
}

func TestResolveApprovedWritePathRejectsEscapeOutsideRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root := filepath.Join(dir, ".ferret")
	_, err := fsutil.ResolveApprovedWritePath(filepath.Join(dir, "notes.md"), root)
	if err == nil {
		t.Fatal("expected path outside approved root to be rejected")
	}
}

func TestResolveApprovedWritePathAcceptsAbsolutePathInsideRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root := filepath.Join(dir, ".ferret")
	path := filepath.Join(root, "plans", "next.md")
	got, err := fsutil.ResolveApprovedWritePath(path, root)
	if err != nil {
		t.Fatalf("ResolveApprovedWritePath: %v", err)
	}
	if got != path {
		t.Fatalf("expected %q, got %q", path, got)
	}
}
