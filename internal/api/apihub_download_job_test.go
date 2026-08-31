package api

import (
	"os"
	"path/filepath"
	"testing"
)

// A failed transfer must not leave its folder behind: uniqueDir steps around
// anything that already exists, so an empty leftover turns the next retry into
// "Title (2)" rather than a clean reuse of the name.
func TestCleanupItemDirRemovesEmptyLeftover(t *testing.T) {
	root := t.TempDir()
	itemDir := filepath.Join(root, "Studio - 2026-08-30 - Title")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(itemDir, "Studio - 2026-08-30 - Title.mp4.part")
	if err := os.WriteFile(part, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanupItemDir(itemDir, part)

	if _, err := os.Stat(itemDir); !os.IsNotExist(err) {
		t.Errorf("item folder survived cleanup: %v", err)
	}
}

// The folder is removed with os.Remove, not RemoveAll, so anything the
// download didn't put there survives — cleanup must never cost a user a file.
func TestCleanupItemDirKeepsNonEmptyFolder(t *testing.T) {
	root := t.TempDir()
	itemDir := filepath.Join(root, "Studio - 2026-08-30 - Title")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(itemDir, "notes.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(itemDir, "Studio - 2026-08-30 - Title.mp4.part")
	if err := os.WriteFile(part, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanupItemDir(itemDir, part)

	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf("partial file survived cleanup: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("unrelated file was removed: %v", err)
	}
}

// Cleanup runs on every failure path, including ones where the transfer never
// created a file, so a missing part must not be treated as an error.
func TestCleanupItemDirToleratesMissingPart(t *testing.T) {
	root := t.TempDir()
	itemDir := filepath.Join(root, "Studio - 2026-08-30 - Title")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cleanupItemDir(itemDir, filepath.Join(itemDir, "never-written.mp4.part"))

	if _, err := os.Stat(itemDir); !os.IsNotExist(err) {
		t.Errorf("item folder survived cleanup: %v", err)
	}
}
