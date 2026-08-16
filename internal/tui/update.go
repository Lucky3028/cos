package tui

import (
	"sort"

	tea "charm.land/bubbletea/v2"
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
		return m.updateThreadsLoaded(msg)
	case conversationLoadedMsg:
		return m.updateConversationLoaded(msg)
	case deleteCheckMsg:
		return m.updateDeleteCheck(msg)
	case resumeCheckMsg:
		return m.updateResumeCheck(msg)
	case deletedMsg:
		return m.updateDeleted(msg)
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

func (m *model) sortThreads() {
	sort.SliceStable(m.threads, func(i, j int) bool { return m.threads[i].Updated.After(m.threads[j].Updated) })
}

func (m *model) resizeViewport() {
	_, right := m.paneWidths()
	// The pane width includes the border and horizontal padding. Match the
	// viewport to the actual content area so the outer frame does not wrap it
	// a second time.
	m.viewport.SetWidth(max(1, right-4))
	m.viewport.SetHeight(m.bodyHeight())
	m.ensureListVisible()
	if m.hasConversation {
		m.viewport.SetContent(m.conversationText())
	}
}

func (m *model) ensureListVisible() {
	if len(m.visibleThreads) == 0 {
		m.listOffset = 0
		return
	}
	m.selectedIndex = min(max(0, m.selectedIndex), len(m.visibleThreads)-1)
	m.listOffset = 0
	for m.listOffset < m.selectedIndex && m.listRows(m.listOffset, m.selectedIndex+1) > m.bodyHeight() {
		m.listOffset++
	}
}

func (m model) listRows(start, end int) int {
	rows := 0
	for index := start; index < end && index < len(m.visibleThreads); index++ {
		rows += m.listRowHeight(index)
	}
	return rows
}

func (m model) listRowHeight(index int) int {
	if index < 0 || index >= len(m.visibleThreads) {
		return 0
	}
	rows := 2
	if m.hasPreviewRow(m.visibleThreads[index]) {
		rows++
	}
	if index < len(m.visibleThreads)-1 {
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
