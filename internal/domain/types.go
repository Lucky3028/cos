package domain

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// ListScope controls whether a listing is restricted to the launch directory.
type ListScope int

const (
	CurrentDirectory ListScope = iota
	AllThreads
)

const (
	// DefaultThreadPageSize is the maximum number of sessions requested per
	// app-server page.
	DefaultThreadPageSize = 100
	// MaxSearchPages bounds the number of app-server pages scanned by search.
	MaxSearchPages = 100
)

// ThreadListRequest describes one page of a thread/list query. Cursor is the
// opaque cursor returned by the previous page and must not be interpreted by
// the UI or store.
type ThreadListRequest struct {
	Scope       ListScope
	CWD         string
	Cursor      string
	Limit       int
	Query       string
	SearchPages int // Number of app-server pages already scanned for Query.
}

type ThreadPage struct {
	Threads      []Thread
	NextCursor   string
	Incomplete   bool
	ScannedPages int // Number of app-server pages scanned to produce this page.
}

// SessionStore is the small boundary between the UI and Codex app-server.
type SessionStore interface {
	List(ctx context.Context, request ThreadListRequest) (ThreadPage, error)
	ListDescendants(ctx context.Context, id string) ([]Thread, error)
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
	// Truncated is true when the app-server returned more history than the UI
	// deliberately loaded for the conversation preview.
	Truncated bool
}

type ConversationItemKind string

const (
	ConversationItemKindUser      ConversationItemKind = "user"
	ConversationItemKindAssistant ConversationItemKind = "assistant"
	ConversationItemKindActivity  ConversationItemKind = "activity"
)

type ConversationItem struct {
	Kind ConversationItemKind
	Text string
}

// sanitizeTerminalText removes terminal escape sequences and control
// characters from app-server data before it reaches the TUI. Newlines are
// retained for conversation bodies; callers rendering a single line should
// pass preserveNewlines=false.
func SanitizeTerminalText(value string, preserveNewlines bool) string {
	value = ansi.Strip(value)
	return strings.Map(func(r rune) rune {
		if preserveNewlines && r == '\n' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, value)
}

func SanitizeSingleLine(value string) string {
	return strings.Join(strings.Fields(SanitizeTerminalText(value, false)), " ")
}
