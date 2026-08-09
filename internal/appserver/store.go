package appserver

import (
	"context"
	"fmt"
	"sync"
)

type AppServerStore struct {
	command string
	args    []string

	mu      sync.Mutex
	process *appServerProcess
}

func NewAppServerStore(command string, args ...string) *AppServerStore {
	return &AppServerStore{command: command, args: append([]string(nil), args...)}
}

func NewDefaultStore() *AppServerStore {
	return NewAppServerStore("codex", "app-server", "--stdio")
}

func (s *AppServerStore) Close() error {
	s.mu.Lock()
	process := s.process
	s.process = nil
	s.mu.Unlock()
	if process == nil {
		return nil
	}
	return process.close()
}

func (s *AppServerStore) client(ctx context.Context) (*rpcClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.process != nil {
		select {
		case <-s.process.client.done:
			process := s.process
			s.process = nil
			_ = process.close()
		default:
			return s.process.client, nil
		}
	}
	process, err := startAppServer(ctx, s.command, s.args...)
	if err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	if err := process.client.request(ctx, "initialize", map[string]any{
		"clientInfo": map[string]string{
			"name": "cos", "title": "cos", "version": Version,
		},
		"capabilities": map[string]any{"experimentalApi": true},
	}, nil); err != nil {
		_ = process.close()
		return nil, fmt.Errorf("initialize codex app-server: %w", err)
	}
	if err := process.client.notifyContext(ctx, "initialized", map[string]any{}); err != nil {
		_ = process.close()
		return nil, fmt.Errorf("initialize codex app-server: %w", err)
	}
	s.process = process
	return process.client, nil
}
