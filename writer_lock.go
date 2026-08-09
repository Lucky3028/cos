package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func hasActiveWriter(threadID string) (bool, error) {
	return writerLockStatus(threadID)
}

func writerLockStatus(threadID string) (bool, error) {
	lockPath, err := threadWriterLockPath(threadID)
	if err != nil {
		return false, err
	}
	return lockStatus(lockPath)
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
	locked, err := lockStatus(path)
	return err == nil && locked
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
