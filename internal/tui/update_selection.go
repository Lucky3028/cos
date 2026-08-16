package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m model) selectThread(index int) (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(m.visibleThreads) || m.loading {
		return m, nil
	}
	m.selectedIndex = index
	m.selectedByScope[scopeIndex(m.scope)] = index
	m.ensureListVisible()
	m.hasConversation = false
	m.err = nil
	return m, m.beginConversationLoad(m.visibleThreads[index].ID)
}

func (m model) listIndexAt(y int) int {
	// The header and list border occupy rows 0 and 1; the first title starts at 2.
	if y < 2 || y >= 2+m.bodyHeight() {
		return -1
	}
	row := y - 2
	for index := m.listOffset; index < len(m.visibleThreads); index++ {
		rowCount := m.listRowHeight(index)
		if row < rowCount {
			return index
		}
		row -= rowCount
	}
	return -1
}

func (m model) moveSelection(delta int) (tea.Model, tea.Cmd) {
	if len(m.visibleThreads) == 0 || m.loading {
		return m, nil
	}
	nextIndex := m.selectedIndex + delta
	if nextIndex < 0 {
		return m.loadPreviousPage()
	}
	if nextIndex >= len(m.visibleThreads) {
		return m.loadNextPage()
	}
	if nextIndex == m.selectedIndex {
		return m, nil
	}
	m.selectedIndex = nextIndex
	m.selectedByScope[scopeIndex(m.scope)] = nextIndex
	return m, m.refreshConversationForSelection()
}

func (m *model) refreshConversationForSelection() tea.Cmd {
	m.ensureListVisible()
	m.hasConversation = false
	m.err = nil
	return m.beginConversationLoad(m.visibleThreads[m.selectedIndex].ID)
}

func (m model) loadPreviousPage() (tea.Model, tea.Cmd) {
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

func (m model) loadNextPage() (tea.Model, tea.Cmd) {
	if m.nextCursor == "" || m.searchIncomplete {
		return m, nil
	}
	m.previousCursors = append(m.previousCursors, m.pageCursor)
	m.previousSearchPages = append(m.previousSearchPages, m.pageSearchStart)
	return m, m.beginPageLoadWithSearchPages(m.nextCursor, false, m.searchPages)
}

func (m *model) selectedThreadID() string {
	thread, ok := m.selectedThread()
	if !ok {
		return ""
	}
	return thread.ID
}

func (m *model) conversationAfterSelectionChange(previousThreadID string) tea.Cmd {
	if currentThreadID := m.selectedThreadID(); currentThreadID != previousThreadID {
		m.hasConversation = false
		m.err = nil
		if currentThreadID == "" {
			m.cancelActiveRequest()
			m.requestGeneration++
			return nil
		}
		return m.beginConversationLoad(currentThreadID)
	}
	return nil
}

func (m *model) cancelActiveRequest() {
	if m.requestCancel != nil {
		m.requestCancel()
		m.requestCancel = nil
	}
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
	if m.selectedIndex < 0 || m.selectedIndex >= len(m.visibleThreads) {
		return Thread{}, false
	}
	return m.visibleThreads[m.selectedIndex], true
}

func (m *model) applyFilter() {
	query := strings.ToLower(strings.TrimSpace(m.query))
	m.visibleThreads = m.visibleThreads[:0]
	for _, thread := range m.threads {
		if query == "" || strings.Contains(strings.ToLower(thread.Title), query) ||
			strings.Contains(strings.ToLower(thread.Preview), query) || strings.Contains(strings.ToLower(thread.CWD), query) {
			m.visibleThreads = append(m.visibleThreads, thread)
		}
	}
	if len(m.visibleThreads) == 0 {
		m.selectedIndex = 0
	} else if m.selectedIndex >= len(m.visibleThreads) {
		m.selectedIndex = len(m.visibleThreads) - 1
	}
	m.ensureListVisible()
}
