package tui

import (
	"context"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

type pane int

const (
	listPane pane = iota
	conversationPane
)

type requestMeta struct {
	generation  uint64
	scope       ListScope
	cwd         string
	cursor      string
	query       string
	searchPages int
	id          string
}

type threadsLoadedMsg struct {
	threads []Thread
	page    ThreadPage
	err     error
	requestMeta
}

type conversationLoadedMsg struct {
	conversation Conversation
	err          error
	requestMeta
}

type deletedMsg struct {
	threads []Thread
	page    ThreadPage
	err     error
	requestMeta
}

type deleteCheckMsg struct {
	conversation Conversation
	err          error
	requestMeta
}

type resumeCheckMsg struct {
	conversation Conversation
	err          error
	requestMeta
}

type Model struct {
	store SessionStore
	cwd   string
	scope ListScope

	threads                 []Thread
	filtered                []Thread
	selected                int
	listOffset              int
	showConversationPreview bool
	// Keep a cursor per scope so a short result set cannot overwrite the
	// position remembered for the other scope.
	selectedByScope     [2]int
	scopeVisited        [2]bool
	pageCursor          string
	nextCursor          string
	previousCursors     []string
	previousSearchPages []int
	pendingCursor       string
	pendingSearchPages  int
	pageSearchStart     int
	searchPages         int
	selectPageEnd       bool
	searchIncomplete    bool
	pane                pane

	conversation    Conversation
	hasConversation bool
	viewport        viewport.Model

	loading           bool
	deleting          bool
	requestGeneration uint64
	searching         bool
	query             string
	confirmDelete     bool
	checkingDelete    bool
	checkingResume    bool
	resumeRequested   bool
	resumeSession     Thread
	requestContext    context.Context
	requestCancel     context.CancelFunc
	err               error
	width             int
	height            int
}

// model remains an internal alias so the implementation can retain its
// compact names while the package exposes a stable Model type.
type model = Model

// NewModel creates the session browser for the supplied store and launch cwd.
func NewModel(store SessionStore, cwd string) Model {
	return newModel(store, cwd)
}

// ResumeSession returns the session selected by the user for CLI resumption.
func (m Model) ResumeSession() (Thread, bool) {
	return m.resumeSession, m.resumeRequested
}

func newModel(store SessionStore, cwd string) model {
	ctx, cancel := context.WithTimeout(context.Background(), asyncRequestTimeout)
	return model{
		store: store, cwd: cwd, scope: CurrentDirectory, pane: listPane, loading: true,
		viewport: viewport.New(1, 1), scopeVisited: [2]bool{true, false}, showConversationPreview: true,
		requestGeneration: 1, requestContext: ctx, requestCancel: cancel,
	}
}

func (m model) Init() tea.Cmd {
	if m.requestContext != nil && m.requestCancel != nil {
		return loadThreadsWithContext(m.store, m.requestContext, m.requestCancel,
			requestMeta{generation: m.requestGeneration, scope: m.scope, cwd: m.cwd, query: m.query})
	}
	return loadThreadsWithMeta(m.store, requestMeta{generation: m.requestGeneration, scope: m.scope, cwd: m.cwd, query: m.query})
}
