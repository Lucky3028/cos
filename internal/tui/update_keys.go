package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

func (m model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" || (key == "q" && !m.searching && !m.confirmDelete && !m.checkingDelete && !m.checkingResume) {
		return m, tea.Quit
	}
	if m.err != nil {
		return m.updateErrorKey(key)
	}
	if m.checkingDelete || m.deleting || m.checkingResume {
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
		thread, ok := m.selectedThread()
		if ok && !thread.Active {
			meta := m.newRequestMeta(thread.ID)
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
	previousThreadID := m.selectedThreadID()
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
	return m, m.conversationAfterSelectionChange(previousThreadID)
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
	meta := m.newRequestMeta(thread.ID)
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
		m.pane = oppositePane(m.pane)
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
	case "a":
		return m.switchScope()
	case "p":
		m.showConversationPreview = !m.showConversationPreview
		if !m.showConversationPreview {
			m.pane = listPane
		}
	case "r":
		return m, m.beginListLoad()
	case "d":
		return m.beginDeleteCheck()
	}
	return m, nil
}

func oppositePane(current pane) pane {
	if current == listPane {
		return conversationPane
	}
	return listPane
}

func (m model) switchScope() (tea.Model, tea.Cmd) {
	currentScope := scopeIndex(m.scope)
	m.selectedByScope[currentScope] = m.selectedIndex
	if m.scope == CurrentDirectory {
		m.scope = AllThreads
	} else {
		m.scope = CurrentDirectory
	}
	targetScope := scopeIndex(m.scope)
	if !m.scopeVisited[targetScope] {
		m.selectedByScope[targetScope] = m.selectedIndex
		m.scopeVisited[targetScope] = true
	}
	m.selectedIndex = m.selectedByScope[targetScope]
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
	meta := m.newRequestMeta(thread.ID)
	ctx, cancel := m.beginAsyncRequest()
	return m, withManagedRequest(ctx, cancel, func(ctx context.Context) tea.Msg {
		return checkDeleteMessage(ctx, m.store, meta)
	})
}
