package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func hasActiveWriter(threadID string) bool {
	lockPath, err := threadWriterLockPath(threadID)
	if err != nil {
		return false
	}
	return isLocked(lockPath)
}

func threadWriterLockPath(threadID string) (string, error) {
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

func isLocked(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		return false
	}
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
