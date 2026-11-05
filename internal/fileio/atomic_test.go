package fileio

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProtectedReplacementAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteProtected(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != "new" {
		t.Fatal("not replaced")
	}
	stat, _ := os.Stat(path)
	if runtime.GOOS != "windows" && stat.Mode().Perm() != 0600 {
		t.Fatal("insecure mode")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatal("temporary file leaked")
	}
}
func TestProtectedFailureLeavesContentsUntouched(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "directory")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(target, "sentinel")
	os.WriteFile(child, []byte("keep"), 0600)
	if err := WriteProtected(target, []byte("replace")); err == nil {
		t.Fatal("directory overwritten")
	}
	content, _ := os.ReadFile(child)
	if string(content) != "keep" {
		t.Fatal("failed write changed target")
	}
}

func TestUnwritableDirectoryPreservesExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("Unix directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	os.WriteFile(path, []byte("old-key"), 0600)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if err := WriteProtected(path, []byte("new-key")); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	content, _ := os.ReadFile(path)
	if string(content) != "old-key" {
		t.Fatal("failed write truncated credential")
	}
}
