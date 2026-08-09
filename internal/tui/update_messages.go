package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

func (m model) updateThreadsLoaded(msg threadsLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.matchesRequest(msg.requestMeta, false) {
		return m, nil
	}

	m.loading = false
	m.deleting = false
	m.err = msg.err
	if msg.err != nil {
		return m, nil
	}

	page := pageFromMessage(msg.page, msg.threads)
	if err := m.installThreadPage(msg.requestMeta, page); err != nil {
		m.err = err
		return m, nil
	}
	if msg.requestMeta.generation != 0 {
		m.selectPageBoundary()
	}
	m.selectLastOnPage = false
	m.applyFilter()
	m.hasConversation = false
	return m.beginSelectedConversation()
}

func (m model) updateConversationLoaded(msg conversationLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.matchesRequest(msg.requestMeta, true) ||
		(msg.err == nil && msg.requestMeta.threadID != "" && msg.conversation.Thread.ID != msg.requestMeta.threadID) {
		return m, nil
	}

	m.err = msg.err
	if msg.err != nil {
		return m, nil
	}

	msg.conversation = m.addListMetadata(msg.conversation)
	m.conversation = msg.conversation
	m.hasConversation = true
	m.viewport.SetContent(m.conversationText())
	m.viewport.GotoTop()
	return m, nil
}

func (m model) updateDeleteCheck(msg deleteCheckMsg) (tea.Model, tea.Cmd) {
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
}

func (m model) updateResumeCheck(msg resumeCheckMsg) (tea.Model, tea.Cmd) {
	if !m.matchesRequest(msg.requestMeta, true) {
		return m, nil
	}

	m.checkingResume = false
	m.err = msg.err
	if msg.err != nil {
		return m, nil
	}
	if msg.conversation.Thread.Active {
		m.err = sessionInUseError()
		return m, nil
	}

	thread, ok := m.selectedThread()
	if !ok {
		return m, nil
	}
	// Some read responses omit cwd. The list response retains the saved cwd
	// used to launch the session, so only fill the missing value here.
	if thread.CWD == "" {
		thread.CWD = msg.conversation.Thread.CWD
	}
	m.resumeSession = thread
	m.resumeRequested = true
	return m, tea.Quit
}

func (m model) updateDeleted(msg deletedMsg) (tea.Model, tea.Cmd) {
	if !m.matchesRequest(msg.requestMeta, true) {
		return m, nil
	}

	m.confirmDelete = false
	m.loading = false
	m.deleting = false
	m.err = msg.err
	// A failed delete can still have completed remotely. Apply the refreshed
	// page whenever it is available so the UI does not keep an unconditional
	// stale list.
	page := pageFromMessage(msg.page, msg.threads)
	if page.Threads == nil && msg.err != nil {
		return m, nil
	}
	if err := m.installThreadPage(msg.requestMeta, page); err != nil {
		m.err = err
		return m, nil
	}
	m.selectedIndex = min(m.selectedIndex, max(0, len(m.threads)-1))
	m.selectedByScope[scopeIndex(m.scope)] = m.selectedIndex
	m.applyFilter()
	m.hasConversation = false
	if msg.err == nil {
		return m.beginSelectedConversation()
	}
	return m, nil
}

func pageFromMessage(page ThreadPage, legacyThreads []Thread) ThreadPage {
	if page.Threads == nil && page.NextCursor == "" && !page.Incomplete {
		page.Threads = legacyThreads
	}
	return page
}

func (m *model) installThreadPage(meta requestMeta, page ThreadPage) error {
	m.threads = append([]Thread(nil), page.Threads...)
	m.pageCursor = meta.cursor
	m.pendingCursor = meta.cursor
	m.pendingSearchPages = meta.searchPages
	m.pageSearchStart = meta.searchPages
	m.searchPages = meta.searchPages
	if page.ScannedPages > 0 {
		m.searchPages += page.ScannedPages
	}
	m.nextCursor = page.NextCursor
	m.searchIncomplete = page.Incomplete

	if meta.generation != 0 && page.NextCursor != "" &&
		(page.NextCursor == meta.cursor || containsCursor(m.previousCursors, page.NextCursor)) {
		m.threads = nil
		m.visibleThreads = nil
		return fmt.Errorf("thread/list cursor cycle detected at %q", page.NextCursor)
	}
	m.sortThreads()
	return nil
}

func (m *model) selectPageBoundary() {
	if m.selectLastOnPage && len(m.threads) > 0 {
		m.selectedIndex = len(m.threads) - 1
	} else if !m.selectLastOnPage {
		m.selectedIndex = 0
	}
}

func (m model) beginSelectedConversation() (tea.Model, tea.Cmd) {
	if len(m.visibleThreads) == 0 {
		m.selectedIndex = 0
		return m, nil
	}
	m.selectedIndex = min(max(0, m.selectedIndex), len(m.visibleThreads)-1)
	return m, m.beginConversationLoad(m.visibleThreads[m.selectedIndex].ID)
}

func (m model) addListMetadata(conversation Conversation) Conversation {
	selectedThread, ok := m.selectedThread()
	if !ok || selectedThread.ID != conversation.Thread.ID {
		return conversation
	}
	if conversation.Thread.Title == "" {
		conversation.Thread.Title = selectedThread.Title
	}
	if conversation.Thread.Preview == "" {
		conversation.Thread.Preview = selectedThread.Preview
	}
	return conversation
}
