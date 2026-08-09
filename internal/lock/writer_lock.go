package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func writerLockStatus(threadID string) (bool, error) {
	lockPath, err := threadWriterLockPath(threadID)
	if err != nil {
		return false, err
	}
	return lockStatus(lockPath)
}

// WriterLockStatus reports whether the session's writer lock is currently held.
func WriterLockStatus(threadID string) (bool, error) {
	return writerLockStatus(threadID)
}

func threadWriterLockPath(threadID string) (string, error) {
	if !validThreadID(threadID) {
		return "", fmt.Errorf("invalid thread ID %q", threadID)
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		codexHome = filepath.Join(home, ".codex")
	}
	return filepath.Join(codexHome, "thread-writer-locks", threadID+".lock"), nil
}

func validThreadID(threadID string) bool {
	if threadID == "" {
		return false
	}
	for _, r := range threadID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || strings.ContainsRune("-_", r) {
			continue
		}
		return false
	}
	return true
}

func isLocked(path string) bool {
	locked, err := lockStatus(path)
	return err == nil && locked
}

// IsLocked reports whether the lock at path is held by another process.
func IsLocked(path string) bool {
	return isLocked(path)
}

func lockStatus(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer file.Close()

	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
			return false, err
		}
		return false, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return true, nil
	}
	return false, err
}

// LockStatus returns the lock state, distinguishing an unavailable path from
// an I/O error.
func LockStatus(path string) (bool, error) {
	return lockStatus(path)
}
