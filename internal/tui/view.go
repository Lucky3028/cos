package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

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
			row := marker + truncate(sanitizeSingleLine(name), max(1, contentWidth-lipgloss.Width(marker)))
			row = fitLine(row, contentWidth)
			if i == m.selected {
				row = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F59E0B")).Background(lipgloss.Color("#4B5563")).Render(row)
			}
			b.WriteString(row + "\n")
			metadata := "   " + mutedText(formatTime(thread.Updated)) + "  " + mutedText(sanitizeSingleLine(thread.CWD))
			b.WriteString(fitLine(metadata, contentWidth) + "\n")
			if m.hasPreviewRow(thread) {
				preview := "   " + mutedText(sanitizeSingleLine(thread.Preview))
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
		if item.Kind == ConversationItemKindUser {
			label = "user"
		}
		if item.Kind == ConversationItemKindActivity {
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

func kindColor(kind ConversationItemKind) lipgloss.Color {
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
		return sanitizeSingleLine(thread.Title)
	}
	if strings.TrimSpace(thread.Preview) != "" {
		return sanitizeSingleLine(thread.Preview)
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
	if ansi.StringWidth(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "…")
}

func dropLastRune(value string) string {
	_, size := utf8.DecodeLastRuneInString(value)
	if size == 0 {
		return value
	}
	return value[:len(value)-size]
}
