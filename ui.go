package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

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

type threadsLoadedMsg struct {
	threads []Thread
	err     error
}

type conversationLoadedMsg struct {
	conversation Conversation
	err          error
}

type deletedMsg struct {
	threads []Thread
	err     error
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
	selectedByScope [2]int
	scopeVisited    [2]bool
	pane            pane

	conversation    Conversation
	hasConversation bool
	viewport        viewport.Model

	loading       bool
	searching     bool
	query         string
	confirmDelete bool
	status        string
	err           error
	width         int
	height        int
}

func newModel(store SessionStore, cwd string) model {
	return model{
		store: store, cwd: cwd, scope: CurrentDirectory, pane: listPane, loading: true,
		viewport: viewport.New(1, 1), scopeVisited: [2]bool{true, false}, showConversationPreview: true,
	}
}

func (m model) Init() tea.Cmd {
	return loadThreads(m.store, m.scope, m.cwd)
}

func loadThreads(store SessionStore, scope ListScope, cwd string) tea.Cmd {
	return func() tea.Msg {
		threads, err := store.List(context.Background(), scope, cwd)
		return threadsLoadedMsg{threads: threads, err: err}
	}
}

func readConversation(store SessionStore, id string) tea.Cmd {
	return func() tea.Msg {
		conversation, err := store.Read(context.Background(), id)
		return conversationLoadedMsg{conversation: conversation, err: err}
	}
}

func deleteThread(store SessionStore, scope ListScope, cwd, id string) tea.Cmd {
	return func() tea.Msg {
		if err := store.Delete(context.Background(), id); err != nil {
			return deletedMsg{err: err}
		}
		threads, err := store.List(context.Background(), scope, cwd)
		if err != nil {
			return deletedMsg{err: fmt.Errorf("deleted session %s, but reload failed: %w", id, err)}
		}
		for _, thread := range threads {
			if thread.ID == id {
				return deletedMsg{err: fmt.Errorf("session %s is still present after deletion", id)}
			}
		}
		return deletedMsg{threads: threads}
	}
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
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.threads = msg.threads
			m.sortThreads()
			m.applyFilter()
			m.hasConversation = false
			if len(m.filtered) > 0 {
				m.selected = min(m.selected, len(m.filtered)-1)
				if m.selected < 0 {
					m.selected = 0
				}
				return m, readConversation(m.store, m.filtered[m.selected].ID)
			}
			m.selected = 0
		}
		return m, nil
	case conversationLoadedMsg:
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
	case deletedMsg:
		m.confirmDelete = false
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.threads = msg.threads
			m.selected = min(m.selected, max(0, len(msg.threads)-1))
			if m.selected < 0 {
				m.selected = 0
			}
			m.selectedByScope[scopeIndex(m.scope)] = m.selected
			m.applyFilter()
			m.hasConversation = false
			m.status = "Session deleted"
			if len(m.filtered) > 0 {
				return m, readConversation(m.store, m.filtered[m.selected].ID)
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
	if key == "ctrl+c" || (key == "q" && !m.searching && !m.confirmDelete) {
		return m, tea.Quit
	}
	if m.confirmDelete {
		switch key {
		case "y":
			if thread, ok := m.selectedThread(); ok && !thread.Active {
				m.loading = true
				m.status = "Deleting…"
				return m, deleteThread(m.store, m.scope, m.cwd, thread.ID)
			}
		case "n", "esc":
			m.confirmDelete = false
			m.status = "Delete cancelled"
		}
		return m, nil
	}
	if m.searching {
		switch msg.Type {
		case tea.KeyEsc:
			m.searching = false
			m.query = ""
			m.applyFilter()
		case tea.KeyEnter:
			m.searching = false
		case tea.KeyBackspace:
			if len(m.query) > 0 {
				m.query = m.query[:len(m.query)-1]
				m.applyFilter()
			}
		case tea.KeyRunes:
			m.query += string(msg.Runes)
			m.applyFilter()
		}
		return m, nil
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
		m.status, m.err = "", nil
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
		m.loading, m.status, m.err = true, "", nil
		return m, loadThreads(m.store, m.scope, m.cwd)
	case "p":
		m.showConversationPreview = !m.showConversationPreview
		if m.showConversationPreview {
			m.status = "Conversation preview shown"
		} else {
			m.status = "Conversation preview hidden"
			m.pane = listPane
		}
		return m, nil
	case "r":
		m.loading, m.status, m.err = true, "", nil
		return m, loadThreads(m.store, m.scope, m.cwd)
	case "d":
		thread, ok := m.selectedThread()
		if !ok {
			return m, nil
		}
		if thread.Active {
			m.err = fmt.Errorf("active session cannot be deleted")
		} else {
			m.confirmDelete = true
			m.err = nil
		}
	}
	return m, nil
}

func (m model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.confirmDelete {
		return m, nil
	}
	leftWidth, _ := m.paneWidths()
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
	return m, readConversation(m.store, m.filtered[index].ID)
}

func (m model) listIndexAt(y int) int {
	// The header occupies row 0 and the list border occupies row 1. The
	// first thread's title starts at row 2.
	row := y - 2
	if row < 0 {
		return -1
	}
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
		next = len(m.filtered) - 1
	}
	if next >= len(m.filtered) {
		next = 0
	}
	if next == m.selected {
		return m, nil
	}
	m.selected = next
	m.selectedByScope[scopeIndex(m.scope)] = next
	m.ensureListVisible()
	m.hasConversation = false
	m.err = nil
	return m, readConversation(m.store, m.filtered[m.selected].ID)
}

func scopeIndex(scope ListScope) int {
	if scope == AllThreads {
		return 1
	}
	return 0
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
	scope := "sessions in current working directory: " + m.cwd
	if m.scope == AllThreads {
		scope = "all sessions"
	}
	header := title + "  " + muted.Render(scope)
	if m.searching {
		header += "  /" + m.query
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
			metadata := "   " + mutedText(formatTime(thread.Updated)) + "  " + mutedText(thread.CWD)
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
	b.WriteString(mutedText(m.conversation.Thread.CWD+"  "+formatTime(m.conversation.Thread.Updated)) + "\n\n")
	for _, item := range m.conversation.Items {
		label := "assistant"
		if item.Kind == "user" {
			label = "user"
		}
		if item.Kind == "activity" {
			label = "activity"
		}
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(kindColor(item.Kind)).Render(label) + "\n")
		b.WriteString(item.Text + "\n\n")
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
	muted := mutedText("j/k ↑/↓ select  Tab pane  / search  a scope  p preview  r reload  d delete  q quit")
	if m.searching {
		return "type to search  Enter apply  Esc cancel"
	}
	if m.err != nil {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#F25D94")).Render("Error: " + m.err.Error())
	}
	if m.status != "" {
		return m.status + "  " + muted
	}
	return muted
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
