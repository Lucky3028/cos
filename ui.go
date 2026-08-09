package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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

type model struct {
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

func (m *model) nextRequest(id string) requestMeta {
	m.requestGeneration++
	return requestMeta{generation: m.requestGeneration, scope: m.scope, cwd: m.cwd, cursor: m.pendingCursor, query: m.query, searchPages: m.pendingSearchPages, id: id}
}

func (m *model) beginConversation(id string) tea.Cmd {
	meta := m.nextRequest(id)
	ctx, cancel := m.beginAsyncRequest()
	m.hasConversation = false
	return readConversationWithContext(m.store, ctx, cancel, meta)
}

func (m *model) beginListLoad() tea.Cmd {
	m.pageCursor = ""
	m.nextCursor = ""
	m.previousCursors = nil
	m.previousSearchPages = nil
	m.pendingCursor = ""
	m.pendingSearchPages = 0
	m.pageSearchStart = 0
	m.searchPages = 0
	m.selectPageEnd = false
	m.searchIncomplete = false
	meta := m.nextRequest("")
	ctx, cancel := m.beginAsyncRequest()
	m.loading = true
	m.err = nil
	return loadThreadsWithContext(m.store, ctx, cancel, meta)
}

func (m *model) beginPageLoad(cursor string, selectEnd bool) tea.Cmd {
	return m.beginPageLoadWithSearchPages(cursor, selectEnd, m.searchPages)
}

func (m *model) beginPageLoadWithSearchPages(cursor string, selectEnd bool, searchPages int) tea.Cmd {
	m.pendingCursor = cursor
	m.pendingSearchPages = searchPages
	m.selectPageEnd = selectEnd
	meta := m.nextRequest("")
	ctx, cancel := m.beginAsyncRequest()
	m.loading = true
	m.err = nil
	m.hasConversation = false
	m.threads = nil
	m.filtered = nil
	return loadThreadsWithContext(m.store, ctx, cancel, meta)
}

const asyncRequestTimeout = 30 * time.Second

func (m *model) beginAsyncRequest() (context.Context, context.CancelFunc) {
	if m.requestCancel != nil {
		m.requestCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), asyncRequestTimeout)
	m.requestContext = ctx
	m.requestCancel = cancel
	return ctx, cancel
}

func (m *model) clearListState() {
	m.threads = nil
	m.filtered = nil
	m.listOffset = 0
	m.hasConversation = false
	m.conversation = Conversation{}
	m.viewport.SetContent("")
}

func (m model) matchesRequest(meta requestMeta, requireID bool) bool {
	// Zero metadata is retained for small, synchronous unit-test messages and
	// for callers of the legacy command helpers. Every real UI command carries
	// a non-zero generation.
	if meta.generation == 0 {
		if requireID && meta.id != "" {
			selected, ok := m.selectedThread()
			return ok && selected.ID == meta.id
		}
		return true
	}
	if meta.generation != m.requestGeneration || meta.scope != m.scope || meta.cwd != m.cwd || meta.query != m.query {
		return false
	}
	if requireID {
		selected, ok := m.selectedThread()
		return ok && selected.ID == meta.id
	}
	return true
}

func loadThreads(store SessionStore, scope ListScope, cwd string) tea.Cmd {
	return loadThreadsWithMeta(store, requestMeta{scope: scope, cwd: cwd})
}

func loadThreadsWithMeta(store SessionStore, meta requestMeta) tea.Cmd {
	return withAsyncRequest(func(ctx context.Context) tea.Msg {
		return loadThreadsMessage(ctx, store, meta)
	})
}

func loadThreadsWithContext(store SessionStore, ctx context.Context, cancel context.CancelFunc, meta requestMeta) tea.Cmd {
	return func() tea.Msg {
		defer cancel()
		return loadThreadsMessage(ctx, store, meta)
	}
}

func loadThreadsMessage(ctx context.Context, store SessionStore, meta requestMeta) tea.Msg {
	page, err := store.List(ctx, ThreadListRequest{
		Scope: meta.scope, CWD: meta.cwd, Cursor: meta.cursor,
		Limit: defaultThreadPageSize, Query: meta.query, SearchPages: meta.searchPages,
	})
	return threadsLoadedMsg{page: page, err: err, requestMeta: meta}
}

func withAsyncRequest(fn func(context.Context) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), asyncRequestTimeout)
		defer cancel()
		return fn(ctx)
	}
}

func readConversation(store SessionStore, id string) tea.Cmd {
	return readConversationWithMeta(store, requestMeta{id: id})
}

func readConversationWithMeta(store SessionStore, meta requestMeta) tea.Cmd {
	return withAsyncRequest(func(ctx context.Context) tea.Msg {
		return readConversationMessage(ctx, store, meta)
	})
}

func readConversationWithContext(store SessionStore, ctx context.Context, cancel context.CancelFunc, meta requestMeta) tea.Cmd {
	return func() tea.Msg {
		defer cancel()
		return readConversationMessage(ctx, store, meta)
	}
}

func withManagedRequest(ctx context.Context, cancel context.CancelFunc, fn func(context.Context) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		defer cancel()
		return fn(ctx)
	}
}

func readConversationMessage(ctx context.Context, store SessionStore, meta requestMeta) tea.Msg {
	conversation, err := store.Read(ctx, meta.id)
	return conversationLoadedMsg{conversation: conversation, err: err, requestMeta: meta}
}

func checkDelete(store SessionStore, id string) tea.Cmd {
	return checkDeleteWithMeta(store, requestMeta{id: id})
}

func checkDeleteWithMeta(store SessionStore, meta requestMeta) tea.Cmd {
	return withAsyncRequest(func(ctx context.Context) tea.Msg {
		return checkDeleteMessage(ctx, store, meta)
	})
}

func checkDeleteMessage(ctx context.Context, store SessionStore, meta requestMeta) tea.Msg {
	locked, err := writerLockStatus(meta.id)
	if err != nil {
		return deleteCheckMsg{err: fmt.Errorf("check writer lock for session %s: %w", meta.id, err), requestMeta: meta}
	}
	if locked {
		return deleteCheckMsg{conversation: Conversation{Thread: Thread{ID: meta.id, Active: true}}, requestMeta: meta}
	}
	conversation, err := store.Read(ctx, meta.id)
	if err != nil {
		return deleteCheckMsg{err: err, requestMeta: meta}
	}
	if conversation.Thread.Active {
		return deleteCheckMsg{conversation: conversation, requestMeta: meta}
	}
	descendants, err := store.ListDescendants(ctx, meta.id)
	if err != nil {
		return deleteCheckMsg{err: fmt.Errorf("cannot verify descendants of session %s: %w", meta.id, err), requestMeta: meta}
	}
	if len(descendants) > 0 {
		return deleteCheckMsg{err: descendantSessionError(meta.id, descendants), requestMeta: meta}
	}
	return deleteCheckMsg{conversation: conversation, requestMeta: meta}
}

func checkResume(store SessionStore, id string) tea.Cmd {
	return checkResumeWithMeta(store, requestMeta{id: id})
}

func checkResumeWithMeta(store SessionStore, meta requestMeta) tea.Cmd {
	return withAsyncRequest(func(ctx context.Context) tea.Msg {
		return checkResumeMessage(ctx, store, meta)
	})
}

func checkResumeMessage(ctx context.Context, store SessionStore, meta requestMeta) tea.Msg {
	locked, err := writerLockStatus(meta.id)
	if err != nil {
		return resumeCheckMsg{err: fmt.Errorf("check writer lock for session %s: %w", meta.id, err), requestMeta: meta}
	}
	if locked {
		return resumeCheckMsg{conversation: Conversation{Thread: Thread{ID: meta.id, Active: true}}, requestMeta: meta}
	}
	conversation, err := store.Read(ctx, meta.id)
	return resumeCheckMsg{conversation: conversation, err: err, requestMeta: meta}
}

func deleteThread(store SessionStore, scope ListScope, cwd, id string) tea.Cmd {
	return deleteThreadWithMeta(store, requestMeta{scope: scope, cwd: cwd, id: id})
}

func deleteThreadWithMeta(store SessionStore, meta requestMeta) tea.Cmd {
	return withAsyncRequest(func(ctx context.Context) tea.Msg {
		return deleteThreadMessage(ctx, store, meta)
	})
}

func deleteThreadMessage(ctx context.Context, store SessionStore, meta requestMeta) tea.Msg {
	locked, err := writerLockStatus(meta.id)
	if err != nil {
		return deletedMsg{err: fmt.Errorf("check writer lock for session %s: %w", meta.id, err), requestMeta: meta}
	}
	if locked {
		return deletedMsg{err: sessionInUseError(), requestMeta: meta}
	}
	conversation, err := store.Read(ctx, meta.id)
	if err != nil {
		return deletedMsg{err: err, requestMeta: meta}
	}
	if conversation.Thread.Active {
		return deletedMsg{err: sessionInUseError(), requestMeta: meta}
	}
	descendants, err := store.ListDescendants(ctx, meta.id)
	if err != nil {
		return deletedMsg{err: fmt.Errorf("cannot verify descendants of session %s: %w", meta.id, err), requestMeta: meta}
	}
	if len(descendants) > 0 {
		return deletedMsg{err: descendantSessionError(meta.id, descendants), requestMeta: meta}
	}
	// This is the final best-effort check. The app-server delete API has no
	// leaf-only precondition, so another client may still create a descendant
	// before thread/delete runs.
	deleteErr := store.Delete(ctx, meta.id)
	page, listErr := store.List(ctx, ThreadListRequest{
		Scope: meta.scope, CWD: meta.cwd, Cursor: meta.cursor,
		Limit: defaultThreadPageSize, Query: meta.query, SearchPages: meta.searchPages,
	})
	if listErr != nil {
		if deleteErr != nil {
			return deletedMsg{err: fmt.Errorf("delete failed (%v); reload failed: %w", deleteError(deleteErr), listErr), requestMeta: meta}
		}
		return deletedMsg{err: fmt.Errorf("deleted session %s, but reload failed: %w", meta.id, listErr), requestMeta: meta}
	}
	if deleteErr != nil {
		return deletedMsg{page: page, err: deleteError(deleteErr), requestMeta: meta}
	}
	for _, thread := range page.Threads {
		if thread.ID == meta.id {
			return deletedMsg{page: page, err: fmt.Errorf("session %s is still present after deletion", meta.id), requestMeta: meta}
		}
	}
	return deletedMsg{page: page, requestMeta: meta}
}

func descendantSessionError(id string, descendants []Thread) error {
	return fmt.Errorf("session %s has %d descendant session(s); delete is unavailable", id, len(descendants))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeViewport()
		return m, nil
	case tea.MouseMsg:
		return m.updateMouse(msg)
	case threadsLoadedMsg:
		if !m.matchesRequest(msg.requestMeta, false) {
			return m, nil
		}
		m.loading = false
		m.deleting = false
		m.err = msg.err
		if msg.err == nil {
			page := msg.page
			if msg.page.Threads == nil && msg.page.NextCursor == "" && !msg.page.Incomplete {
				page.Threads = msg.threads
			}
			m.threads = append([]Thread(nil), page.Threads...)
			m.pageCursor = msg.requestMeta.cursor
			m.pendingCursor = msg.requestMeta.cursor
			m.pendingSearchPages = msg.requestMeta.searchPages
			m.pageSearchStart = msg.requestMeta.searchPages
			if page.ScannedPages > 0 {
				m.searchPages = msg.requestMeta.searchPages + page.ScannedPages
			} else {
				m.searchPages = msg.requestMeta.searchPages
			}
			m.nextCursor = page.NextCursor
			m.searchIncomplete = page.Incomplete
			if msg.requestMeta.generation != 0 && page.NextCursor != "" &&
				(page.NextCursor == msg.requestMeta.cursor || containsCursor(m.previousCursors, page.NextCursor)) {
				m.err = fmt.Errorf("thread/list cursor cycle detected at %q", page.NextCursor)
				m.threads = nil
				m.filtered = nil
				return m, nil
			}
			m.sortThreads()
			if msg.requestMeta.generation != 0 {
				if m.selectPageEnd && len(m.threads) > 0 {
					m.selected = len(m.threads) - 1
				} else if !m.selectPageEnd {
					m.selected = 0
				}
			}
			m.selectPageEnd = false
			m.applyFilter()
			m.hasConversation = false
			if len(m.filtered) > 0 {
				m.selected = min(m.selected, len(m.filtered)-1)
				if m.selected < 0 {
					m.selected = 0
				}
				return m, m.beginConversation(m.filtered[m.selected].ID)
			}
			m.selected = 0
		}
		return m, nil
	case conversationLoadedMsg:
		if !m.matchesRequest(msg.requestMeta, true) || (msg.err == nil && msg.requestMeta.id != "" && msg.conversation.Thread.ID != msg.requestMeta.id) {
			return m, nil
		}
		m.err = msg.err
		if msg.err == nil {
			if selected, ok := m.selectedThread(); ok && selected.ID == msg.conversation.Thread.ID {
				if msg.conversation.Thread.Title == "" {
					msg.conversation.Thread.Title = selected.Title
				}
				if msg.conversation.Thread.Preview == "" {
					msg.conversation.Thread.Preview = selected.Preview
				}
			}
			m.conversation = msg.conversation
			m.hasConversation = true
			m.viewport.SetContent(m.conversationText())
			m.viewport.GotoTop()
		}
		return m, nil
	case deleteCheckMsg:
		if !m.matchesRequest(msg.requestMeta, true) {
			return m, nil
		}
		m.checkingDelete = false
		m.err = msg.err
		if msg.err == nil {
			if msg.conversation.Thread.Active {
				m.err = sessionInUseError()
			} else {
				m.confirmDelete = true
			}
		}
		return m, nil
	case resumeCheckMsg:
		if !m.matchesRequest(msg.requestMeta, true) {
			return m, nil
		}
		m.checkingResume = false
		m.err = msg.err
		if msg.err == nil {
			if msg.conversation.Thread.Active {
				m.err = sessionInUseError()
				return m, nil
			}
			if thread, ok := m.selectedThread(); ok {
				// Keep the cwd from the list response, which is the saved cwd
				// used to launch the session. Some read responses omit it.
				if thread.CWD == "" {
					thread.CWD = msg.conversation.Thread.CWD
				}
				m.resumeSession = thread
				m.resumeRequested = true
				return m, tea.Quit
			}
		}
		return m, nil
	case deletedMsg:
		if !m.matchesRequest(msg.requestMeta, true) {
			return m, nil
		}
		m.confirmDelete = false
		m.loading = false
		m.deleting = false
		m.err = msg.err
		// A failed delete can still have completed remotely. Apply a refreshed
		// page even when the delete result is an error so the UI never presents
		// an unconditionally stale list.
		page := msg.page
		if msg.page.Threads == nil && msg.page.NextCursor == "" && !msg.page.Incomplete {
			page.Threads = msg.threads
		}
		if page.Threads != nil || msg.err == nil {
			m.threads = append([]Thread(nil), page.Threads...)
			m.nextCursor = page.NextCursor
			m.pageCursor = msg.requestMeta.cursor
			m.pendingCursor = msg.requestMeta.cursor
			m.pendingSearchPages = msg.requestMeta.searchPages
			m.pageSearchStart = msg.requestMeta.searchPages
			if page.ScannedPages > 0 {
				m.searchPages = msg.requestMeta.searchPages + page.ScannedPages
			} else {
				m.searchPages = msg.requestMeta.searchPages
			}
			m.searchIncomplete = page.Incomplete
			m.selected = min(m.selected, max(0, len(m.threads)-1))
			if m.selected < 0 {
				m.selected = 0
			}
			m.selectedByScope[scopeIndex(m.scope)] = m.selected
			m.sortThreads()
			m.applyFilter()
			m.hasConversation = false
			if msg.err == nil && len(m.filtered) > 0 {
				return m, m.beginConversation(m.filtered[m.selected].ID)
			}
		}
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg)
	}

	if m.pane == conversationPane {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" || (key == "q" && !m.searching && !m.confirmDelete && !m.checkingDelete && !m.checkingResume) {
		return m, tea.Quit
	}
	if m.err != nil {
		if key == "enter" || key == "esc" {
			m.err = nil
		}
		return m, nil
	}
	if m.checkingDelete {
		return m, nil
	}
	if m.deleting {
		return m, nil
	}
	if m.checkingResume {
		return m, nil
	}
	if m.confirmDelete {
		switch key {
		case "y":
			if thread, ok := m.selectedThread(); ok && !thread.Active {
				meta := m.nextRequest(thread.ID)
				ctx, cancel := m.beginAsyncRequest()
				m.loading = true
				m.deleting = true
				return m, withManagedRequest(ctx, cancel, func(ctx context.Context) tea.Msg {
					return deleteThreadMessage(ctx, m.store, meta)
				})
			}
		case "n", "esc":
			m.confirmDelete = false
		}
		return m, nil
	}
	if m.searching {
		previousID := m.selectedID()
		switch msg.Type {
		case tea.KeyEsc:
			m.searching = false
			m.query = ""
			m.applyFilter()
			return m, m.beginListLoad()
		case tea.KeyEnter:
			m.searching = false
			return m, m.beginListLoad()
		case tea.KeyBackspace:
			if len(m.query) > 0 {
				m.query = dropLastRune(m.query)
				m.applyFilter()
			}
		case tea.KeyRunes:
			m.query += string(msg.Runes)
			m.applyFilter()
		}
		return m, m.conversationAfterSelectionChange(previousID)
	}
	if key == "enter" {
		if m.loading {
			return m, nil
		}
		thread, ok := m.selectedThread()
		if !ok {
			return m, nil
		}
		if thread.Active {
			m.err = sessionInUseError()
			return m, nil
		}
		m.checkingResume = true
		meta := m.nextRequest(thread.ID)
		ctx, cancel := m.beginAsyncRequest()
		return m, withManagedRequest(ctx, cancel, func(ctx context.Context) tea.Msg {
			return checkResumeMessage(ctx, m.store, meta)
		})
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyTab:
		if m.pane == listPane {
			m.pane = conversationPane
		} else {
			m.pane = listPane
		}
		return m, nil
	case tea.KeyUp:
		return m.moveSelection(-1)
	case tea.KeyDown:
		return m.moveSelection(1)
	case tea.KeyPgUp, tea.KeyPgDown:
		if m.pane == conversationPane {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	case tea.KeyEsc:
		m.err = nil
		return m, nil
	}
	switch key {
	case "j":
		return m.moveSelection(1)
	case "k":
		return m.moveSelection(-1)
	case "/":
		m.searching = true
		m.query = ""
		return m, nil
	case "a":
		currentScope := scopeIndex(m.scope)
		m.selectedByScope[currentScope] = m.selected
		if m.scope == CurrentDirectory {
			m.scope = AllThreads
		} else {
			m.scope = CurrentDirectory
		}
		targetScope := scopeIndex(m.scope)
		if !m.scopeVisited[targetScope] {
			m.selectedByScope[targetScope] = m.selected
			m.scopeVisited[targetScope] = true
		}
		m.selected = m.selectedByScope[targetScope]
		m.clearListState()
		m.loading, m.err = true, nil
		return m, m.beginListLoad()
	case "p":
		m.showConversationPreview = !m.showConversationPreview
		if !m.showConversationPreview {
			m.pane = listPane
		}
		return m, nil
	case "r":
		return m, m.beginListLoad()
	case "d":
		thread, ok := m.selectedThread()
		if !ok {
			return m, nil
		}
		if thread.Active {
			m.err = sessionInUseError()
		} else {
			m.checkingDelete = true
			meta := m.nextRequest(thread.ID)
			ctx, cancel := m.beginAsyncRequest()
			return m, withManagedRequest(ctx, cancel, func(ctx context.Context) tea.Msg {
				return checkDeleteMessage(ctx, m.store, meta)
			})
		}
	}
	return m, nil
}

func (m model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.confirmDelete || m.checkingDelete || m.checkingResume || m.err != nil {
		return m, nil
	}
	leftWidth, _ := m.paneWidths()
	if !m.showConversationPreview {
		leftWidth = m.width
	}
	if tea.MouseEvent(msg).IsWheel() {
		if msg.X < leftWidth {
			m.pane = listPane
			if msg.Button == tea.MouseButtonWheelUp {
				return m.moveSelection(-1)
			}
			if msg.Button == tea.MouseButtonWheelDown {
				return m.moveSelection(1)
			}
			return m, nil
		}
		if m.hasConversation {
			var cmd tea.Cmd
			m.pane = conversationPane
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		return m, nil
	}
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress {
		return m, nil
	}
	if msg.X < leftWidth {
		// Clicking anywhere in the left pane should restore focus, including
		// its border and empty space below the last thread.
		m.pane = listPane
		index := m.listIndexAt(msg.Y)
		if index >= 0 && index < len(m.filtered) {
			return m.selectThread(index)
		}
		return m, nil
	}
	m.pane = conversationPane
	return m, nil
}

func (m model) selectThread(index int) (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(m.filtered) || m.loading {
		return m, nil
	}
	m.selected = index
	m.selectedByScope[scopeIndex(m.scope)] = index
	m.ensureListVisible()
	m.hasConversation = false
	m.err = nil
	return m, m.beginConversation(m.filtered[index].ID)
}

func (m model) listIndexAt(y int) int {
	// The header occupies row 0 and the list border occupies row 1. The
	// first thread's title starts at row 2.
	if y < 2 || y >= 2+m.bodyHeight() {
		return -1
	}
	row := y - 2
	for index := m.listOffset; index < len(m.filtered); index++ {
		rowCount := m.listRowHeight(index)
		if row < rowCount {
			return index
		}
		row -= rowCount
	}
	return -1
}

func (m model) moveSelection(delta int) (tea.Model, tea.Cmd) {
	if len(m.filtered) == 0 || m.loading {
		return m, nil
	}
	next := m.selected + delta
	if next < 0 {
		if len(m.previousCursors) == 0 {
			return m, nil
		}
		last := len(m.previousCursors) - 1
		cursor := m.previousCursors[last]
		m.previousCursors = m.previousCursors[:last]
		searchPages := 0
		if last < len(m.previousSearchPages) {
			searchPages = m.previousSearchPages[last]
			m.previousSearchPages = m.previousSearchPages[:last]
		}
		return m, m.beginPageLoadWithSearchPages(cursor, true, searchPages)
	}
	if next >= len(m.filtered) {
		if m.nextCursor == "" || m.searchIncomplete {
			return m, nil
		}
		m.previousCursors = append(m.previousCursors, m.pageCursor)
		m.previousSearchPages = append(m.previousSearchPages, m.pageSearchStart)
		return m, m.beginPageLoadWithSearchPages(m.nextCursor, false, m.searchPages)
	}
	if next == m.selected {
		return m, nil
	}
	m.selected = next
	m.selectedByScope[scopeIndex(m.scope)] = next
	m.ensureListVisible()
	m.hasConversation = false
	m.err = nil
	return m, m.beginConversation(m.filtered[m.selected].ID)
}

func (m *model) selectedID() string {
	thread, ok := m.selectedThread()
	if !ok {
		return ""
	}
	return thread.ID
}

func (m *model) conversationAfterSelectionChange(previousID string) tea.Cmd {
	currentID := m.selectedID()
	if currentID == previousID {
		return nil
	}
	m.hasConversation = false
	m.err = nil
	if currentID == "" {
		if m.requestCancel != nil {
			m.requestCancel()
			m.requestCancel = nil
		}
		m.requestGeneration++
		return nil
	}
	return m.beginConversation(currentID)
}

func scopeIndex(scope ListScope) int {
	if scope == AllThreads {
		return 1
	}
	return 0
}

func containsCursor(cursors []string, cursor string) bool {
	for _, value := range cursors {
		if value == cursor {
			return true
		}
	}
	return false
}

func (m *model) selectedThread() (Thread, bool) {
	if m.selected < 0 || m.selected >= len(m.filtered) {
		return Thread{}, false
	}
	return m.filtered[m.selected], true
}

func (m *model) applyFilter() {
	query := strings.ToLower(strings.TrimSpace(m.query))
	m.filtered = m.filtered[:0]
	for _, thread := range m.threads {
		if query == "" || strings.Contains(strings.ToLower(thread.Title), query) ||
			strings.Contains(strings.ToLower(thread.Preview), query) || strings.Contains(strings.ToLower(thread.CWD), query) {
			m.filtered = append(m.filtered, thread)
		}
	}
	if len(m.filtered) == 0 {
		m.selected = 0
	} else if m.selected >= len(m.filtered) {
		m.selected = len(m.filtered) - 1
	}
	m.ensureListVisible()
}

func (m *model) sortThreads() {
	sort.SliceStable(m.threads, func(i, j int) bool { return m.threads[i].Updated.After(m.threads[j].Updated) })
}

func (m *model) resizeViewport() {
	left, right := m.paneWidths()
	_ = left
	// The pane width includes the border and horizontal padding. Match the
	// viewport to the actual content area so the outer frame does not wrap it
	// a second time.
	m.viewport.Width = max(1, right-4)
	m.viewport.Height = m.bodyHeight()
	m.ensureListVisible()
	if m.hasConversation {
		m.viewport.SetContent(m.conversationText())
	}
}

func (m *model) ensureListVisible() {
	if len(m.filtered) == 0 {
		m.listOffset = 0
		return
	}
	m.selected = min(max(0, m.selected), len(m.filtered)-1)
	m.listOffset = 0
	for m.listOffset < m.selected && m.listRows(m.listOffset, m.selected+1) > m.bodyHeight() {
		m.listOffset++
	}
}

func (m model) listRows(start, end int) int {
	rows := 0
	for index := start; index < end && index < len(m.filtered); index++ {
		rows += m.listRowHeight(index)
	}
	return rows
}

func (m model) listRowHeight(index int) int {
	if index < 0 || index >= len(m.filtered) {
		return 0
	}
	rows := 2
	if m.hasPreviewRow(m.filtered[index]) {
		rows++
	}
	if index < len(m.filtered)-1 {
		rows++
	}
	return rows
}

// bodyHeight leaves one row for the header and one for the status bar. The
// bordered panes add two rows of their own, so this keeps the final view
// within the terminal height.
func (m model) bodyHeight() int {
	return max(1, m.height-4)
}

func (m model) paneWidths() (int, int) {
	if m.width < 2 {
		return 1, 1
	}
	left := m.width * 38 / 100
	if left < 28 {
		left = 28
	}
	if left > m.width-20 {
		left = max(1, m.width/2)
	}
	return left, max(1, m.width-left-1)
}

func (m model) View() string {
	if m.width == 0 {
		return "Loading cos…"
	}
	base := m.renderBaseView()
	if m.err != nil {
		return overlayPopup(base, m.renderErrorPopup(), m.width, m.height)
	}
	if m.checkingDelete {
		return overlayPopup(base, m.renderDeleteChecking(), m.width, m.height)
	}
	if m.checkingResume {
		return overlayPopup(base, m.renderResumeChecking(), m.width, m.height)
	}
	if m.confirmDelete {
		return overlayPopup(base, m.renderDeleteConfirmation(), m.width, m.height)
	}
	return base
}

func (m model) renderBaseView() string {
	leftWidth, rightWidth := m.paneWidths()
	accent := lipgloss.Color("#F59E0B")
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#777777"))
	title := lipgloss.NewStyle().Bold(true).Foreground(accent).Render("cos")
	scope := "sessions in current working directory: " + sanitizeSingleLine(m.cwd)
	if m.scope == AllThreads {
		scope = "all sessions"
	}
	header := title + "  " + muted.Render(scope)
	if m.searching {
		header += "  /" + sanitizeSingleLine(m.query)
	}
	if m.searchIncomplete {
		header += "  " + muted.Render("search incomplete (100-page limit)")
	}
	header = lipgloss.NewStyle().Width(max(1, m.width)).MaxHeight(1).Render(header)

	left := m.renderList(leftWidth)
	var body string
	if m.showConversationPreview {
		right := m.renderConversation(rightWidth)
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	} else {
		body = m.renderList(m.width)
	}
	status := m.renderStatus()
	status = lipgloss.NewStyle().Width(max(1, m.width)).MaxHeight(1).Render(status)
	return header + "\n" + body + "\n" + status
}

func (m model) renderDeleteConfirmation() string {
	thread, ok := m.selectedThread()
	if !ok {
		return ""
	}
	dialogWidth := min(52, max(1, m.width-8))
	titleWidth := max(1, dialogWidth-4)
	title := lipgloss.NewStyle().Width(titleWidth).Render(displayTitle(thread))
	title = limitLinesWithEllipsis(title, 3, titleWidth)
	actions := lipgloss.NewStyle().Width(titleWidth).Align(lipgloss.Center).Render(
		lipgloss.NewStyle().Bold(true).Render("y") + " delete    " +
			lipgloss.NewStyle().Bold(true).Render("n / Esc") + " cancel",
	)
	content := lipgloss.NewStyle().Bold(true).Render("Delete session?") + "\n\n" +
		lipgloss.NewStyle().Foreground(lipgloss.Color("#F25D94")).Render(title) + "\n\n" +
		"This permanently deletes the session." + "\n\n" +
		actions
	dialog := lipgloss.NewStyle().
		Width(dialogWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#F25D94")).
		Padding(1, 2).
		Render(content)
	return dialog
}

func (m model) renderDeleteChecking() string {
	dialogWidth := min(52, max(1, m.width-8))
	contentWidth := max(1, dialogWidth-4)
	content := lipgloss.NewStyle().Bold(true).Render("Checking session…") + "\n\n" +
		lipgloss.NewStyle().Width(contentWidth).Align(lipgloss.Center).Render("Please wait")
	dialog := lipgloss.NewStyle().
		Width(dialogWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#F59E0B")).
		Padding(1, 2).
		Render(content)
	return dialog
}

func (m model) renderResumeChecking() string {
	dialogWidth := min(52, max(1, m.width-8))
	contentWidth := max(1, dialogWidth-4)
	content := lipgloss.NewStyle().Bold(true).Render("Checking session…") + "\n\n" +
		lipgloss.NewStyle().Width(contentWidth).Align(lipgloss.Center).Render("Preparing resume")
	dialog := lipgloss.NewStyle().
		Width(dialogWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#F59E0B")).
		Padding(1, 2).
		Render(content)
	return dialog
}

func (m model) renderErrorPopup() string {
	dialogWidth := min(72, max(1, m.width-8))
	contentWidth := max(1, dialogWidth-4)
	message := lipgloss.NewStyle().Width(contentWidth).Foreground(lipgloss.Color("#F25D94")).Render(sanitizeTerminalText(m.err.Error(), true))
	actions := lipgloss.NewStyle().Width(contentWidth).Align(lipgloss.Center).Render(
		lipgloss.NewStyle().Bold(true).Render("Enter / Esc") + " close",
	)
	content := lipgloss.NewStyle().Bold(true).Render("Error") + "\n\n" +
		message + "\n\n" +
		actions
	dialog := lipgloss.NewStyle().
		Width(dialogWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#F25D94")).
		Padding(1, 2).
		Render(content)
	return dialog
}

func overlayPopup(base, popup string, width, height int) string {
	if width <= 0 || height <= 0 {
		return popup
	}
	baseLines := strings.Split(base, "\n")
	for len(baseLines) < height {
		baseLines = append(baseLines, "")
	}
	if len(baseLines) > height {
		baseLines = baseLines[:height]
	}
	for i := range baseLines {
		baseLines[i] = fitLine(baseLines[i], width)
	}

	popupLines := strings.Split(popup, "\n")
	popupWidth := 0
	for _, line := range popupLines {
		popupWidth = max(popupWidth, ansi.StringWidth(line))
	}
	popupWidth = min(width, popupWidth)
	popupHeight := min(height, len(popupLines))
	startX := max(0, (width-popupWidth)/2)
	startY := max(0, (height-popupHeight)/2)
	for row := 0; row < popupHeight; row++ {
		line := ansi.Truncate(popupLines[row], popupWidth, "")
		line += strings.Repeat(" ", max(0, popupWidth-ansi.StringWidth(line)))
		left := ansi.Cut(baseLines[startY+row], 0, startX)
		right := ansi.Cut(baseLines[startY+row], startX+popupWidth, width)
		baseLines[startY+row] = left + line + right
	}
	return strings.Join(baseLines, "\n")
}

func fitLine(line string, width int) string {
	line = ansi.Truncate(line, width, "")
	return line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line)))
}

func limitLines(value string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(value, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

func limitLinesWithEllipsis(value string, maxLines, width int) string {
	lines := strings.Split(value, "\n")
	if len(lines) <= maxLines {
		return value
	}
	lines = lines[:maxLines]
	last := ansi.Truncate(lines[maxLines-1], max(1, width-1), "")
	lines[maxLines-1] = last + "…"
	return strings.Join(lines, "\n")
}

func (m model) renderList(width int) string {
	border := lipgloss.NormalBorder()
	bodyHeight := m.bodyHeight()
	contentWidth := max(1, width-4) // pane width minus border and padding
	style := lipgloss.NewStyle().Width(max(1, width-2)).Height(bodyHeight).Border(border).BorderForeground(lipgloss.Color("#444444")).BorderBottom(true).Padding(0, 1)
	if m.pane == listPane {
		style = style.BorderForeground(lipgloss.Color("#F59E0B"))
	}
	var b strings.Builder
	if m.loading {
		b.WriteString("Loading…")
	} else if len(m.filtered) == 0 {
		b.WriteString("No sessions")
	} else {
		for i := m.listOffset; i < len(m.filtered); i++ {
			thread := m.filtered[i]
			marker := "  "
			if i == m.selected {
				marker = "▸ "
			}
			name := displayTitle(thread)
			if thread.Active {
				name = "● " + name
			}
			row := marker + truncate(oneLine(name), max(1, contentWidth-lipgloss.Width(marker)))
			row = fitLine(row, contentWidth)
			if i == m.selected {
				row = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F59E0B")).Background(lipgloss.Color("#4B5563")).Render(row)
			}
			b.WriteString(row + "\n")
			metadata := "   " + mutedText(formatTime(thread.Updated)) + "  " + mutedText(sanitizeSingleLine(thread.CWD))
			b.WriteString(fitLine(metadata, contentWidth) + "\n")
			if m.hasPreviewRow(thread) {
				preview := "   " + mutedText(oneLine(thread.Preview))
				b.WriteString(fitLine(preview, contentWidth) + "\n")
			}
			if i < len(m.filtered)-1 {
				// Leave a small visual gap between sessions.
				b.WriteString("\n")
			}
		}
	}
	content := limitLines(strings.TrimRight(b.String(), "\n"), bodyHeight)
	return style.Render(content)
}

func (m model) renderConversation(width int) string {
	border := lipgloss.NormalBorder()
	bodyHeight := m.bodyHeight()
	style := lipgloss.NewStyle().Width(max(1, width-2)).Height(bodyHeight).Border(border).BorderForeground(lipgloss.Color("#666666")).BorderBottom(true).Padding(0, 1)
	if m.pane == conversationPane {
		style = style.BorderForeground(lipgloss.Color("#F59E0B"))
	}
	if m.loading {
		return style.Render("Loading…")
	}
	if !m.hasConversation {
		if len(m.filtered) == 0 {
			return style.Render("Select a session to inspect its conversation.")
		}
		return style.Render("Reading conversation…")
	}
	return style.Render(limitLines(m.viewport.View(), bodyHeight))
}

func (m model) conversationText() string {
	var b strings.Builder
	name := displayTitle(m.conversation.Thread)
	b.WriteString(name + "\n")
	b.WriteString(mutedText(sanitizeSingleLine(m.conversation.Thread.CWD)+"  "+formatTime(m.conversation.Thread.Updated)) + "\n\n")
	if m.conversation.Truncated {
		b.WriteString(mutedText("Conversation truncated to the latest 100 turns or 1 MiB.") + "\n\n")
	}
	for _, item := range m.conversation.Items {
		label := "assistant"
		if item.Kind == "user" {
			label = "user"
		}
		if item.Kind == "activity" {
			label = "activity"
		}
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(kindColor(item.Kind)).Render(label) + "\n")
		b.WriteString(sanitizeTerminalText(item.Text, true) + "\n\n")
	}
	if len(m.conversation.Items) == 0 {
		b.WriteString(mutedText("No displayable conversation items."))
	}
	return b.String()
}

func kindColor(kind string) lipgloss.Color {
	switch kind {
	case "user":
		return lipgloss.Color("#04B575")
	case "activity":
		return lipgloss.Color("#9CA3AF")
	default:
		return lipgloss.Color("#FBBF24")
	}
}

func (m model) renderStatus() string {
	if m.searching {
		return "type to search  Enter apply  Esc cancel"
	}
	if m.searchIncomplete {
		return mutedText("search incomplete after 100 pages  ") + mutedText("j/k ↑/↓ select  r reload  q quit")
	}
	return mutedText("j/k ↑/↓ select  Tab pane  / search  a scope  p preview  r reload  Enter resume  d delete  q quit")
}

func deleteError(err error) error {
	if strings.Contains(err.Error(), "active writer") {
		return sessionInUseError()
	}
	return err
}

func sessionInUseError() error {
	return fmt.Errorf("this session is currently in use and cannot be deleted or resumed")
}

func mutedText(value string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#777777")).Render(value)
}

func displayTitle(thread Thread) string {
	if strings.TrimSpace(thread.Title) != "" {
		return oneLine(thread.Title)
	}
	if strings.TrimSpace(thread.Preview) != "" {
		return oneLine(thread.Preview)
	}
	return "(untitled)"
}

func (m model) hasPreviewRow(thread Thread) bool {
	return thread.Title != "" && thread.Preview != "" && thread.Preview != thread.Title
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Local().Format("2006-01-02 15:04")
}
func truncate(value string, width int) string {
	if width <= 1 {
		return "…"
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	return string(runes[:width-1]) + "…"
}

func dropLastRune(value string) string {
	_, size := utf8.DecodeLastRuneInString(value)
	if size == 0 {
		return value
	}
	return value[:len(value)-size]
}
