package main

import (
	"context"
	"time"
)

// ListScope controls whether a listing is restricted to the launch directory.
type ListScope int

const (
	CurrentDirectory ListScope = iota
	AllThreads
)

// SessionStore is the small boundary between the UI and Codex app-server.
type SessionStore interface {
	List(ctx context.Context, scope ListScope, cwd string) ([]Thread, error)
	Read(ctx context.Context, id string) (Conversation, error)
	Delete(ctx context.Context, id string) error
}

type Thread struct {
	ID      string
	Title   string
	Preview string
	CWD     string
	Updated time.Time
	Active  bool
	Status  string
	Source  string
}

type Conversation struct {
	Thread Thread
	Items  []ConversationItem
}

type ConversationItem struct {
	Kind string // user, assistant, activity
	Text string
}
