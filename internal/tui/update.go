package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

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
		return m.updateErrorKey(key)
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
		return m.updateDeleteModalKey(key)
	}
	if m.searching {
		return m.updateSearchKey(msg)
	}
	if key == "enter" {
		return m.beginResumeCheck()
	}
	return m.updateNavigationKey(msg)
}

func (m model) updateErrorKey(key string) (tea.Model, tea.Cmd) {
	if key == "enter" || key == "esc" {
		m.err = nil
	}
	return m, nil
}

func (m model) updateDeleteModalKey(key string) (tea.Model, tea.Cmd) {
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

func (m model) updateSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m model) beginResumeCheck() (tea.Model, tea.Cmd) {
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

func (m model) updateNavigationKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
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
		return m.switchScope()
	case "p":
		m.showConversationPreview = !m.showConversationPreview
		if !m.showConversationPreview {
			m.pane = listPane
		}
		return m, nil
	case "r":
		return m, m.beginListLoad()
	case "d":
		return m.beginDeleteCheck()
	}
	return m, nil
}

func (m model) switchScope() (tea.Model, tea.Cmd) {
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
}

func (m model) beginDeleteCheck() (tea.Model, tea.Cmd) {
	thread, ok := m.selectedThread()
	if !ok {
		return m, nil
	}
	if thread.Active {
		m.err = sessionInUseError()
		return m, nil
	}
	m.checkingDelete = true
	meta := m.nextRequest(thread.ID)
	ctx, cancel := m.beginAsyncRequest()
	return m, withManagedRequest(ctx, cancel, func(ctx context.Context) tea.Msg {
		return checkDeleteMessage(ctx, m.store, meta)
	})
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
