package lock

import (
	"os"
	"path/filepath"
	"syscall"
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

func TestThreadWriterLockPathUsesDefaultCodexHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	path, err := threadWriterLockPath("fallback")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".codex", "thread-writer-locks", "fallback.lock")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestValidThreadID(t *testing.T) {
	for _, test := range []struct {
		id   string
		want bool
	}{
		{id: "", want: false},
		{id: "abc-123_XYZ", want: true},
		{id: "session/id", want: false},
		{id: "session with spaces", want: false},
		{id: "日本語", want: false},
	} {
		t.Run(test.id, func(t *testing.T) {
			if got := validThreadID(test.id); got != test.want {
				t.Fatalf("validThreadID(%q) = %v, want %v", test.id, got, test.want)
			}
		})
	}
}

func TestWriterLockStatusAndIsLocked(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	locked, err := WriterLockStatus("session")
	if err != nil || locked {
		t.Fatalf("missing lock = locked:%v err:%v", locked, err)
	}

	lockPath := filepath.Join(codexHome, "thread-writer-locks", "session.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	locked, err = LockStatus(lockPath)
	if err != nil || locked {
		t.Fatalf("idle lock = locked:%v err:%v", locked, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	locked, err = WriterLockStatus("session")
	if err != nil || !locked {
		t.Fatalf("active lock = locked:%v err:%v", locked, err)
	}
	if !IsLocked(lockPath) {
		t.Fatal("IsLocked did not detect active lock")
	}
	if IsLocked("\x00") {
		t.Fatal("IsLocked reported an invalid path as locked")
	}
}
