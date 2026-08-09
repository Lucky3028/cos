package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

func (m model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.confirmDelete || m.checkingDelete || m.checkingResume || m.err != nil {
		return m, nil
	}
	leftWidth, _ := m.paneWidths()
	if !m.showConversationPreview {
		leftWidth = m.width
	}
	if tea.MouseEvent(msg).IsWheel() {
		return m.updateWheel(msg, leftWidth)
	}
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress {
		return m, nil
	}
	if msg.X < leftWidth {
		// Clicking the left pane also restores focus on its border and empty area.
		m.pane = listPane
		index := m.listIndexAt(msg.Y)
		if index >= 0 && index < len(m.visibleThreads) {
			return m.selectThread(index)
		}
		return m, nil
	}
	m.pane = conversationPane
	return m, nil
}

func (m model) updateWheel(msg tea.MouseMsg, leftWidth int) (tea.Model, tea.Cmd) {
	if msg.X < leftWidth {
		m.pane = listPane
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			return m.moveSelection(-1)
		case tea.MouseButtonWheelDown:
			return m.moveSelection(1)
		default:
			return m, nil
		}
	}
	if !m.hasConversation {
		return m, nil
	}
	var cmd tea.Cmd
	m.pane = conversationPane
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}
