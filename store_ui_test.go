package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestConversationItemsHideReasoningAndSummarizeActivities(t *testing.T) {
	turn := apiTurn{Items: []json.RawMessage{
		json.RawMessage(`{"type":"userMessage","content":[{"type":"text","text":"hello"}]}`),
		json.RawMessage(`{"type":"reasoning","content":["secret"]}`),
		json.RawMessage(`{"type":"agentMessage","text":"world"}`),
		json.RawMessage(`{"type":"commandExecution","command":"go test ./...","status":"completed"}`),
		json.RawMessage(`{"type":"fileChange","changes":[{"path":"main.go","kind":"edit","diff":"huge"}]}`),
		json.RawMessage(`{"type":"mcpToolCall","server":"docs","tool":"search","arguments":{}}`),
	}}
	items := conversationItems([]apiTurn{turn})
	if len(items) != 5 {
		t.Fatalf("item count = %d, want 5", len(items))
	}
	if items[0].Kind != "user" || items[0].Text != "hello" {
		t.Fatalf("user = %#v", items[0])
	}
	if strings.Contains(items[1].Text, "secret") || items[1].Text != "world" {
		t.Fatalf("reasoning leaked or assistant missing: %#v", items[1])
	}
	if !strings.Contains(items[2].Text, "go test ./...") || !strings.Contains(items[3].Text, "main.go") || !strings.Contains(items[4].Text, "docs/search") {
		t.Fatalf("activities = %#v", items[2:])
	}
}

func TestThreadTitleUsesNameAndPreviewFallback(t *testing.T) {
	name := "Named session"
	withName := apiThread{Name: &name, Preview: "first message"}.toThread()
	if displayTitle(withName) != "Named session" {
		t.Fatalf("title = %q", displayTitle(withName))
	}
	withoutName := apiThread{Preview: "first message"}.toThread()
	if displayTitle(withoutName) != "first message" {
		t.Fatalf("preview fallback = %q", displayTitle(withoutName))
	}
}

func TestFilteringUsesTitlePreviewAndExactCWD(t *testing.T) {
	m := newModel(nil, "/background-preview")
	m.threads = []Thread{
		{ID: "1", Title: "Deploy", Preview: "release", CWD: "/work/project"},
		{ID: "2", Title: "Debug", Preview: "database", CWD: "/work/project/sub"},
	}
	m.query = "release"
	m.applyFilter()
	if len(m.filtered) != 1 || m.filtered[0].ID != "1" {
		t.Fatalf("preview filter = %#v", m.filtered)
	}
	m.query = "/work/project"
	m.applyFilter()
	if len(m.filtered) != 2 {
		t.Fatalf("cwd search = %#v", m.filtered)
	}
}

type fakeStore struct {
	threads []Thread
	deleted []string
}

func (f *fakeStore) List(context.Context, ListScope, string) ([]Thread, error) {
	return append([]Thread(nil), f.threads...), nil
}
func (f *fakeStore) Read(context.Context, string) (Conversation, error) { return Conversation{}, nil }
func (f *fakeStore) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func TestActiveThreadCannotBeDeleted(t *testing.T) {
	store := &fakeStore{threads: []Thread{{ID: "active", Active: true}}}
	m := newModel(store, "/work")
	m.threads = store.threads
	m.applyFilter()
	updated, _ := m.Update(keyMsg("d"))
	result := updated.(model)
	if result.confirmDelete {
		t.Fatal("active thread opened delete confirmation")
	}
	if result.err == nil || !strings.Contains(result.err.Error(), "active") {
		t.Fatalf("error = %v", result.err)
	}
}

func TestScopeSwitchRestoresIndependentSelection(t *testing.T) {
	store := &fakeStore{}
	m := newModel(store, "/work")
	m.loading = false
	m.threads = makeTestThreads(10)
	m.applyFilter()
	m.selected = 6 // seventh item in the cwd-scoped list
	m.selectedByScope[scopeIndex(CurrentDirectory)] = 6

	updated, _ := m.Update(keyMsg("a"))
	m = updated.(model)
	if m.scope != AllThreads || m.selected != 6 {
		t.Fatalf("all scope selection = %d", m.selected)
	}
	updated, _ = m.Update(threadsLoadedMsg{threads: makeTestThreads(3)})
	m = updated.(model)

	updated, _ = m.Update(keyMsg("a"))
	m = updated.(model)
	if m.scope != CurrentDirectory || m.selected != 6 {
		t.Fatalf("restored cwd selection = %d, want 6", m.selected)
	}
	updated, _ = m.Update(threadsLoadedMsg{threads: makeTestThreads(10)})
	m = updated.(model)
	if m.selected != 6 {
		t.Fatalf("loaded cwd selection = %d, want 6", m.selected)
	}
}

func TestMouseClickSelectsThreadAndWheelScrollsConversation(t *testing.T) {
	store := &fakeStore{}
	m := newModel(store, "/work")
	m.width, m.height, m.loading = 80, 20, false
	m.threads = makeTestThreads(3)
	m.applyFilter()

	updated, _ := m.Update(tea.MouseMsg{X: 2, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = updated.(model)
	if m.selected != 1 || m.pane != listPane {
		t.Fatalf("mouse selected = %d, pane = %v", m.selected, m.pane)
	}
	m.pane = conversationPane
	updated, _ = m.Update(tea.MouseMsg{X: 2, Y: 5, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = updated.(model)
	if m.pane != listPane || m.selected != 2 {
		t.Fatalf("left wheel did not select list pane: pane=%v selected=%d", m.pane, m.selected)
	}
	m.pane = conversationPane
	updated, _ = m.Update(tea.MouseMsg{X: 2, Y: 19, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = updated.(model)
	if m.pane != listPane {
		t.Fatal("clicking the left pane's empty area did not restore focus")
	}

	m.hasConversation = true
	m.viewport.Height = 2
	m.viewport.SetContent(strings.Repeat("conversation\n", 20))
	updated, _ = m.Update(tea.MouseMsg{X: 50, Y: 5, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = updated.(model)
	if m.pane != conversationPane || m.viewport.YOffset == 0 {
		t.Fatalf("mouse wheel did not scroll: pane=%v offset=%d", m.pane, m.viewport.YOffset)
	}
}

func makeTestThreads(count int) []Thread {
	threads := make([]Thread, count)
	for i := range threads {
		threads[i] = Thread{ID: string(rune('a' + i)), Title: "thread"}
	}
	return threads
}

func TestViewFitsTerminalHeightWithManyThreads(t *testing.T) {
	m := newModel(nil, "/background-preview")
	m.width, m.height, m.loading = 80, 14, false
	for i := 0; i < 30; i++ {
		m.threads = append(m.threads, Thread{ID: string(rune('a' + i)), Title: "thread"})
	}
	m.applyFilter()
	if height := lipgloss.Height(m.View()); height > m.height {
		t.Fatalf("view height = %d, terminal height = %d", height, m.height)
	}

	// The conversation-loaded state must be bounded too; this is the state
	// that previously expanded after the initial Loading view was rendered.
	m.hasConversation = true
	for i := 0; i < 50; i++ {
		m.conversation.Items = append(m.conversation.Items, ConversationItem{Kind: "assistant", Text: "a long conversation line that should scroll inside the viewport"})
	}
	m.viewport.SetContent(m.conversationText())
	if height := lipgloss.Height(m.View()); height > m.height {
		t.Fatalf("conversation view height = %d, terminal height = %d", height, m.height)
	}

	m.confirmDelete = true
	if height := lipgloss.Height(m.View()); height != m.height {
		t.Fatalf("confirmation height = %d, terminal height = %d", height, m.height)
	}
	view := m.View()
	if !strings.Contains(view, "Delete session?") || !strings.Contains(view, "y") {
		t.Fatal("confirmation popup is missing")
	}
	if !strings.Contains(view, "/background-preview") {
		t.Fatal("background view was replaced by confirmation popup")
	}
}

func TestConversationPaneHasBottomBorder(t *testing.T) {
	m := newModel(nil, "/work")
	m.width, m.height, m.loading = 80, 20, false
	m.hasConversation = true
	m.conversation = Conversation{Thread: Thread{Title: "session", CWD: "/work"}}
	m.viewport.SetContent(strings.Repeat("conversation\n", 100))

	_, rightWidth := m.paneWidths()
	lines := strings.Split(ansi.Strip(m.renderConversation(rightWidth)), "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "┌") {
		t.Fatalf("conversation top border missing; first line = %q", lines[0])
	}
	if len(lines) == 0 || !strings.Contains(lines[len(lines)-1], "┘") {
		t.Fatalf("conversation bottom border missing; last line = %q", lines[len(lines)-1])
	}
	fullLines := strings.Split(ansi.Strip(m.View()), "\n")
	if !strings.Contains(fullLines[len(fullLines)-2], "┘") {
		t.Fatalf("full view right bottom border missing; line = %q", fullLines[len(fullLines)-2])
	}
}

func TestViewFitsAfterWindowResize(t *testing.T) {
	for _, size := range []struct{ width, height int }{{40, 12}, {60, 16}, {80, 20}, {120, 30}} {
		m := newModel(nil, "/work/project")
		m.width, m.height, m.loading = size.width, size.height, false
		m.threads = makeTestThreads(6)
		m.applyFilter()
		m.resizeViewport()

		lines := strings.Split(ansi.Strip(m.View()), "\n")
		if len(lines) > size.height {
			t.Fatalf("size %dx%d produced %d rows", size.width, size.height, len(lines))
		}
		for row, line := range lines {
			if lipgloss.Width(line) > size.width {
				t.Fatalf("size %dx%d row %d has width %d", size.width, size.height, row, lipgloss.Width(line))
			}
		}
		leftWidth, _ := m.paneWidths()
		listLines := strings.Split(ansi.Strip(m.renderList(leftWidth)), "\n")
		if len(listLines) == 0 || !strings.Contains(listLines[len(listLines)-1], "┘") {
			t.Fatalf("size %dx%d left pane bottom border missing", size.width, size.height)
		}
	}
}

func TestResizeKeepsSelectedListItemVisible(t *testing.T) {
	m := newModel(nil, "/work")
	m.width, m.height, m.loading = 80, 30, false
	m.threads = makeTestThreads(10)
	for i := range m.threads {
		m.threads[i].Title = "thread-" + string(rune('A'+i))
	}
	m.applyFilter()
	m.selected = 8
	m.resizeViewport()
	m.height = 12
	m.resizeViewport()

	if m.listOffset == 0 {
		t.Fatal("list did not scroll after shrinking the window")
	}
	leftWidth, _ := m.paneWidths()
	if !strings.Contains(ansi.Strip(m.renderList(leftWidth)), "thread-I") {
		t.Fatal("selected thread is outside the rendered list")
	}
}

func TestConversationPreviewCanBeToggled(t *testing.T) {
	m := newModel(nil, "/work")
	m.width, m.height, m.loading = 80, 20, false
	m.threads = []Thread{{ID: "1", Title: "Title", Preview: "preview text"}}
	m.applyFilter()
	m.hasConversation = true
	m.conversation = Conversation{Thread: m.threads[0], Items: []ConversationItem{{Kind: "assistant", Text: "conversation preview"}}}
	m.resizeViewport()
	m.viewport.SetContent(m.conversationText())
	if !strings.Contains(ansi.Strip(m.View()), "conversation preview") {
		t.Fatal("conversation preview is not shown by default")
	}
	updated, _ := m.Update(keyMsg("p"))
	m = updated.(model)
	if m.showConversationPreview || strings.Contains(ansi.Strip(m.View()), "conversation preview") {
		t.Fatal("conversation preview was not hidden")
	}
}

func keyMsg(key string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)} }
