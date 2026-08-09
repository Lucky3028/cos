package lock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestThreadWriterLockPathRejectsTraversal(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	for _, id := range []string{"../escape", "/absolute", "nested/session", "session with spaces", "session.lock"} {
		t.Run(id, func(t *testing.T) {
			if _, err := threadWriterLockPath(id); err == nil {
				t.Fatalf("threadWriterLockPath(%q) accepted invalid ID", id)
			}
		})
	}
}

func TestThreadWriterLockPathAcceptsNormalID(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	path, err := threadWriterLockPath("thread-123_abc")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(codexHome, "thread-writer-locks", "thread-123_abc.lock")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("path lookup unexpectedly touched lock directory: %v", err)
	}
}
