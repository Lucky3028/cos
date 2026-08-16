package tui

import (
	tea "charm.land/bubbletea/v2"
)

func (m model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.confirmDelete || m.checkingDelete || m.checkingResume || m.err != nil {
		return m, nil
	}
	leftWidth, _ := m.paneWidths()
	if !m.showConversationPreview {
		leftWidth = m.width
	}
	mouse := msg.Mouse()
	if _, ok := msg.(tea.MouseWheelMsg); ok {
		return m.updateWheel(msg, leftWidth)
	}
	if _, ok := msg.(tea.MouseClickMsg); !ok || mouse.Button != tea.MouseLeft {
		return m, nil
	}
	if mouse.X < leftWidth {
		// Clicking the left pane also restores focus on its border and empty area.
		m.pane = listPane
		index := m.listIndexAt(mouse.Y)
		if index >= 0 && index < len(m.visibleThreads) {
			return m.selectThread(index)
		}
		return m, nil
	}
	m.pane = conversationPane
	return m, nil
}

func (m model) updateWheel(msg tea.MouseMsg, leftWidth int) (tea.Model, tea.Cmd) {
	mouse := msg.Mouse()
	if mouse.X < leftWidth {
		m.pane = listPane
		switch mouse.Button {
		case tea.MouseWheelUp:
			return m.moveSelection(-1)
		case tea.MouseWheelDown:
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
