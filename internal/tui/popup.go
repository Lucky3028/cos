package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

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
