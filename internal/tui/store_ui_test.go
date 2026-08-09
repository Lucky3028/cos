package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestFilteringUsesTitlePreviewAndExactCWD(t *testing.T) {
	m := newModel(nil, "/background-preview")
	m.threads = []Thread{
		{ID: "1", Title: "Deploy", Preview: "release", CWD: "/work/project"},
		{ID: "2", Title: "Debug", Preview: "database", CWD: "/work/project/sub"},
	}
	m.query = "release"
	m.applyFilter()
	if len(m.visibleThreads) != 1 || m.visibleThreads[0].ID != "1" {
		t.Fatalf("preview filter = %#v", m.visibleThreads)
	}
	m.query = "/work/project"
	m.applyFilter()
	if len(m.visibleThreads) != 2 {
		t.Fatalf("cwd search = %#v", m.visibleThreads)
	}
}

func TestThreadTitleUsesNameAndPreviewFallback(t *testing.T) {
	withName := Thread{Title: "Named session", Preview: "first message"}
	if displayTitle(withName) != "Named session" {
		t.Fatalf("title = %q", displayTitle(withName))
	}
	withoutName := Thread{Preview: "first message"}
	if displayTitle(withoutName) != "first message" {
		t.Fatalf("preview fallback = %q", displayTitle(withoutName))
	}
}

func TestHeaderSanitizesLaunchCWDOnlyForDisplay(t *testing.T) {
	m := newModel(nil, "/work/\x1b]0;unsafe\a\nproject")
	m.width, m.height, m.loading = 80, 12, false
	view := ansi.Strip(m.View())
	header := strings.Split(view, "\n")[0]
	if strings.ContainsAny(header, "\x1b\a\r") {
		t.Fatalf("header retained terminal control characters: %q", header)
	}
	if !strings.Contains(header, "/work/") || !strings.Contains(header, "project") {
		t.Fatalf("sanitized cwd lost display content: %q", header)
	}
}

func TestTruncateUsesTerminalDisplayWidth(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		width int
		want  string
	}{
		{name: "japanese", value: "日本語", width: 5, want: "日本…"},
		{name: "emoji", value: "🙂🙂🙂", width: 5, want: "🙂🙂…"},
		{name: "combining", value: "e\u0301abc", width: 3, want: "e\u0301a…"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := truncate(test.value, test.width)
			if got != test.want {
				t.Fatalf("truncate(%q, %d) = %q, want %q", test.value, test.width, got, test.want)
			}
			if ansi.StringWidth(got) > test.width {
				t.Fatalf("truncated display width = %d, want <= %d", ansi.StringWidth(got), test.width)
			}
		})
	}
}

type fakeStore struct {
	threads           []Thread
	listPages         map[string]ThreadPage
	listRequests      []ThreadListRequest
	readConversation  Conversation
	readByID          map[string]Conversation
	readIDs           []string
	readErr           error
	descendants       []Thread
	descendantResults [][]Thread
	descendantCalls   int
	descendantsErr    error
	deleted           []string
	deleteErr         error
}

type cancellationStore struct {
	readStarted  chan string
	readCanceled chan string
	listStarted  chan struct{}
	listCanceled chan struct{}
}

func (s *cancellationStore) List(ctx context.Context, _ ThreadListRequest) (ThreadPage, error) {
	select {
	case s.listStarted <- struct{}{}:
	case <-ctx.Done():
		return ThreadPage{}, ctx.Err()
	}
	<-ctx.Done()
	s.listCanceled <- struct{}{}
	return ThreadPage{}, ctx.Err()
}

func (s *cancellationStore) Read(ctx context.Context, id string) (Conversation, error) {
	select {
	case s.readStarted <- id:
	case <-ctx.Done():
		return Conversation{}, ctx.Err()
	}
	<-ctx.Done()
	s.readCanceled <- id
	return Conversation{}, ctx.Err()
}

func (s *cancellationStore) ListDescendants(context.Context, string) ([]Thread, error) {
	return nil, nil
}

func (s *cancellationStore) Delete(context.Context, string) error { return nil }

func (f *fakeStore) List(_ context.Context, request ThreadListRequest) (ThreadPage, error) {
	f.listRequests = append(f.listRequests, request)
	if page, ok := f.listPages[request.Cursor]; ok {
		return page, nil
	}
	return ThreadPage{Threads: append([]Thread(nil), f.threads...)}, nil
}

func (f *fakeStore) Read(_ context.Context, id string) (Conversation, error) {
	f.readIDs = append(f.readIDs, id)
	if conversation, ok := f.readByID[id]; ok {
		return conversation, f.readErr
	}
	return f.readConversation, f.readErr
}
func (f *fakeStore) ListDescendants(context.Context, string) ([]Thread, error) {
	if f.descendantCalls < len(f.descendantResults) {
		result := f.descendantResults[f.descendantCalls]
		f.descendantCalls++
		return append([]Thread(nil), result...), nil
	}
	f.descendantCalls++
	return append([]Thread(nil), f.descendants...), f.descendantsErr
}
func (f *fakeStore) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return f.deleteErr
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
	if result.err == nil || !strings.Contains(result.err.Error(), "currently in use") {
		t.Fatalf("error = %v", result.err)
	}
}

func TestIdleThreadEnterRequestsResumeAndQuits(t *testing.T) {
	store := &fakeStore{
		threads:          []Thread{{ID: "session", Title: "session", CWD: "/saved/work"}},
		readConversation: Conversation{Thread: Thread{ID: "session", CWD: "/saved/work"}},
	}
	m := newModel(store, "/current/work")
	m.width, m.height, m.loading = 80, 20, false
	m.threads = store.threads
	m.applyFilter()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd == nil || !m.checkingResume {
		t.Fatalf("resume check was not started: checking=%v cmd=%v", m.checkingResume, cmd != nil)
	}
	updated, quit := m.Update(cmd().(resumeCheckMsg))
	m = updated.(model)
	if quit == nil || !m.resumeRequested {
		t.Fatalf("resume state = requested:%v quit:%v", m.resumeRequested, quit != nil)
	}
	if m.resumeSession.ID != "session" || m.resumeSession.CWD != "/saved/work" {
		t.Fatalf("resume session = %#v", m.resumeSession)
	}
}

func TestActiveThreadCannotBeResumed(t *testing.T) {
	store := &fakeStore{threads: []Thread{{ID: "active", Active: true}}}
	m := newModel(store, "/work")
	m.loading = false
	m.threads = store.threads
	m.applyFilter()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd != nil || m.resumeRequested || m.checkingResume {
		t.Fatalf("active resume state = requested:%v checking:%v cmd:%v", m.resumeRequested, m.checkingResume, cmd != nil)
	}
	if m.err == nil || !strings.Contains(m.err.Error(), "currently in use") {
		t.Fatalf("error = %v", m.err)
	}
}

func TestWriterLockPreventsResume(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	lockDir := filepath.Join(codexHome, "thread-writer-locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(lockDir, "session.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	store := &fakeStore{threads: []Thread{{ID: "session"}}}
	m := newModel(store, "/work")
	m.loading = false
	m.threads = store.threads
	m.applyFilter()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd == nil || !m.checkingResume {
		t.Fatalf("resume check was not started: checking=%v cmd=%v", m.checkingResume, cmd != nil)
	}
	updated, quit := m.Update(cmd().(resumeCheckMsg))
	m = updated.(model)
	if quit != nil || m.resumeRequested || m.err == nil {
		t.Fatalf("locked resume state = requested:%v err:%v quit:%v", m.resumeRequested, m.err, quit != nil)
	}
}

func TestValidateIdleSessionRejectsWriterLock(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	lockDir := filepath.Join(codexHome, "thread-writer-locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(lockDir, "session.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	_, err = validateIdleSession(context.Background(), &fakeStore{}, "session", false)
	if err == nil || !strings.Contains(err.Error(), "currently in use") {
		t.Fatalf("writer lock validation error = %v", err)
	}
}

func TestValidateIdleSessionRejectsActiveRead(t *testing.T) {
	store := &fakeStore{readConversation: Conversation{Thread: Thread{ID: "session", Active: true}}}
	_, err := validateIdleSession(context.Background(), store, "session", false)
	if err == nil || !strings.Contains(err.Error(), "currently in use") {
		t.Fatalf("active validation error = %v", err)
	}
}

func TestValidateIdleSessionPreservesReadFailure(t *testing.T) {
	want := errors.New("read failed")
	store := &fakeStore{readErr: want}
	_, err := validateIdleSession(context.Background(), store, "session", false)
	if !errors.Is(err, want) {
		t.Fatalf("read validation error = %v, want %v", err, want)
	}
}

func TestValidateIdleSessionRejectsDescendantsWhenRequested(t *testing.T) {
	store := &fakeStore{
		descendants:      []Thread{{ID: "child"}},
		readConversation: Conversation{Thread: Thread{ID: "session"}},
	}
	_, err := validateIdleSession(context.Background(), store, "session", true)
	if err == nil || !strings.Contains(err.Error(), "descendant") {
		t.Fatalf("descendant validation error = %v", err)
	}
}

func TestEnterDoesNotResumeDuringModalOrSearch(t *testing.T) {
	store := &fakeStore{threads: []Thread{{ID: "session"}}}
	for name, setup := range map[string]func(*model){
		"search":              func(m *model) { m.searching = true },
		"delete confirmation": func(m *model) { m.confirmDelete = true },
		"error":               func(m *model) { m.err = errors.New("error") },
	} {
		t.Run(name, func(t *testing.T) {
			m := newModel(store, "/work")
			m.loading = false
			m.threads = store.threads
			m.applyFilter()
			setup(&m)
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			result := updated.(model)
			if result.resumeRequested || result.checkingResume {
				t.Fatalf("resume started: requested:%v checking:%v cmd:%v", result.resumeRequested, result.checkingResume, cmd != nil)
			}
		})
	}
}

func TestDeleteChecksForActiveThreadBeforeConfirmation(t *testing.T) {
	store := &fakeStore{
		threads:          []Thread{{ID: "session", Title: "session"}},
		readConversation: Conversation{Thread: Thread{ID: "session", Active: true}},
	}
	m := newModel(store, "/work")
	m.width, m.height, m.loading = 80, 20, false
	m.threads = store.threads
	m.applyFilter()

	updated, cmd := m.Update(keyMsg("d"))
	m = updated.(model)
	if cmd == nil || !m.checkingDelete {
		t.Fatalf("delete check was not started: checking=%v cmd=%v", m.checkingDelete, cmd != nil)
	}
	updated, _ = m.Update(cmd().(deleteCheckMsg))
	m = updated.(model)
	if m.confirmDelete || m.checkingDelete || m.err == nil {
		t.Fatalf("delete check state = confirm:%v checking:%v err:%v", m.confirmDelete, m.checkingDelete, m.err)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("active session was deleted: %#v", store.deleted)
	}
	if !strings.Contains(m.View(), "currently in use") {
		t.Fatalf("in-use error modal missing: %q", m.View())
	}
}

func TestDescendantSessionCannotBeDeleted(t *testing.T) {
	store := &fakeStore{
		threads:          []Thread{{ID: "parent", Title: "parent"}},
		descendants:      []Thread{{ID: "child"}},
		readConversation: Conversation{Thread: Thread{ID: "parent"}},
	}
	m := newModel(store, "/work")
	m.width, m.height, m.loading = 80, 20, false
	m.threads = store.threads
	m.applyFilter()

	updated, cmd := m.Update(keyMsg("d"))
	m = updated.(model)
	if cmd == nil || !m.checkingDelete {
		t.Fatalf("descendant check was not started: checking=%v cmd=%v", m.checkingDelete, cmd != nil)
	}
	updated, _ = m.Update(cmd().(deleteCheckMsg))
	m = updated.(model)
	if m.confirmDelete || m.checkingDelete || m.err == nil {
		t.Fatalf("descendant delete state = confirm:%v checking:%v err:%v", m.confirmDelete, m.checkingDelete, m.err)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("descendant-bearing session was deleted: %#v", store.deleted)
	}
}

func TestDescendantVerificationFailureIsFailClosed(t *testing.T) {
	store := &fakeStore{
		threads:          []Thread{{ID: "parent"}},
		readConversation: Conversation{Thread: Thread{ID: "parent"}},
		descendantsErr:   errors.New("list unavailable"),
	}
	m := newModel(store, "/work")
	m.loading = false
	m.threads = store.threads
	m.applyFilter()
	updated, cmd := m.Update(keyMsg("d"))
	m = updated.(model)
	updated, _ = m.Update(cmd().(deleteCheckMsg))
	m = updated.(model)
	if m.confirmDelete || m.err == nil || len(store.deleted) != 0 {
		t.Fatalf("verification failure was not fail-closed: confirm=%v err=%v deleted=%v", m.confirmDelete, m.err, store.deleted)
	}
}

func TestDeleteRechecksDescendantsBeforeExecuting(t *testing.T) {
	store := &fakeStore{
		threads:          []Thread{{ID: "parent", Title: "parent"}},
		readConversation: Conversation{Thread: Thread{ID: "parent"}},
		descendantResults: [][]Thread{
			{},
			{{ID: "child"}},
		},
	}
	m := newModel(store, "/work")
	m.width, m.height, m.loading = 80, 20, false
	m.threads = store.threads
	m.applyFilter()

	updated, cmd := m.Update(keyMsg("d"))
	m = updated.(model)
	updated, _ = m.Update(cmd().(deleteCheckMsg))
	m = updated.(model)
	if !m.confirmDelete {
		t.Fatal("delete confirmation did not open")
	}

	updated, cmd = m.Update(keyMsg("y"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("delete command was not started")
	}
	updated, _ = m.Update(cmd().(deletedMsg))
	m = updated.(model)
	if len(store.deleted) != 0 || m.err == nil || !strings.Contains(m.err.Error(), "descendant") {
		t.Fatalf("late descendant was not blocked: deleted=%v err=%v", store.deleted, m.err)
	}
}

func TestStaleListAndConversationResponsesAreDiscarded(t *testing.T) {
	store := &fakeStore{}
	m := newModel(store, "/work")
	m.loading = true
	m.requestGeneration = 5
	m.scope = AllThreads
	m.threads = []Thread{{ID: "new", Title: "new"}}
	m.applyFilter()
	old := threadsLoadedMsg{threads: []Thread{{ID: "old"}}, requestMeta: requestMeta{generation: 4, scope: CurrentDirectory, cwd: "/work"}}
	updated, cmd := m.Update(old)
	m = updated.(model)
	if cmd != nil || len(m.threads) != 1 || m.threads[0].ID != "new" || !m.loading {
		t.Fatalf("stale list changed model: loading=%v threads=%#v cmd=%v", m.loading, m.threads, cmd != nil)
	}

	m.visibleThreads = []Thread{{ID: "new"}}
	m.selectedIndex = 0
	m.hasConversation = true
	m.conversation = Conversation{Thread: Thread{ID: "new"}}
	stale := conversationLoadedMsg{
		conversation: Conversation{Thread: Thread{ID: "new", Title: "old conversation"}},
		requestMeta:  requestMeta{generation: 4, scope: CurrentDirectory, cwd: "/work", threadID: "new"},
	}
	updated, cmd = m.Update(stale)
	m = updated.(model)
	if cmd != nil || m.conversation.Thread.Title != "" {
		// The original conversation title is empty; a stale response must not
		// replace it.
		t.Fatalf("stale conversation changed model: conversation=%#v cmd=%v", m.conversation, cmd != nil)
	}
}

func TestSelectionMovesAcrossPagesWithoutWrapping(t *testing.T) {
	store := &fakeStore{listPages: map[string]ThreadPage{
		"next": {Threads: []Thread{{ID: "second"}}, NextCursor: ""},
		"":     {Threads: []Thread{{ID: "first"}}, NextCursor: "next"},
	}}
	m := newModel(store, "/work")
	m.loading = false
	m.threads = store.listPages[""].Threads
	m.nextCursor = "next"
	m.pageCursor = ""
	m.applyFilter()

	updated, cmd := m.Update(keyMsg("j"))
	m = updated.(model)
	if cmd == nil || !m.loading || len(m.previousCursors) != 1 {
		t.Fatalf("next page not requested: loading=%v previous=%v command=%v", m.loading, m.previousCursors, cmd != nil)
	}
	pageMessage := cmd().(threadsLoadedMsg)
	if request := store.listRequests[0]; request.Cursor != "next" {
		t.Fatalf("next page cursor = %q", request.Cursor)
	}
	updated, _ = m.Update(pageMessage)
	m = updated.(model)
	if m.selectedThreadID() != "second" || m.nextCursor != "" {
		t.Fatalf("second page state = selected:%q next:%q", m.selectedThreadID(), m.nextCursor)
	}

	updated, cmd = m.Update(keyMsg("k"))
	m = updated.(model)
	if cmd == nil || !m.loading || len(m.previousCursors) != 0 {
		t.Fatalf("previous page not requested: loading=%v previous=%v command=%v", m.loading, m.previousCursors, cmd != nil)
	}
	pageMessage = cmd().(threadsLoadedMsg)
	if request := store.listRequests[1]; request.Cursor != "" {
		t.Fatalf("previous page cursor = %q", request.Cursor)
	}
	updated, _ = m.Update(pageMessage)
	m = updated.(model)
	if m.selectedThreadID() != "first" {
		t.Fatalf("previous page selection = %q", m.selectedThreadID())
	}
	updated, cmd = m.Update(keyMsg("j"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("next page could not be requested after returning to the first page")
	}
}

func TestSelectionChangeCancelsPreviousConversationRequest(t *testing.T) {
	store := &cancellationStore{readStarted: make(chan string, 2), readCanceled: make(chan string, 1)}
	m := newModel(store, "/work")
	m.loading = false
	m.threads = []Thread{{ID: "a"}, {ID: "b"}}
	m.applyFilter()
	first := m.beginConversationLoad("a")
	go first()
	select {
	case id := <-store.readStarted:
		if id != "a" {
			t.Fatalf("first read id = %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("first conversation request did not start")
	}
	updated, second := m.Update(keyMsg("j"))
	m = updated.(model)
	if second == nil || m.selectedThreadID() != "b" {
		t.Fatalf("selection change = selected:%q command:%v", m.selectedThreadID(), second != nil)
	}
	select {
	case id := <-store.readCanceled:
		if id != "a" {
			t.Fatalf("canceled read id = %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("previous conversation request was not canceled")
	}
}

func TestScopeChangeCancelsPreviousListRequest(t *testing.T) {
	store := &cancellationStore{listStarted: make(chan struct{}, 2), listCanceled: make(chan struct{}, 1)}
	m := newModel(store, "/work")
	first := m.beginListLoad()
	go first()
	select {
	case <-store.listStarted:
	case <-time.After(time.Second):
		t.Fatal("first list request did not start")
	}
	updated, second := m.Update(keyMsg("a"))
	m = updated.(model)
	if second == nil || m.scope != AllThreads {
		t.Fatalf("scope change = scope:%v command:%v", m.scope, second != nil)
	}
	select {
	case <-store.listCanceled:
	case <-time.After(time.Second):
		t.Fatal("previous list request was not canceled")
	}
}

func TestSearchReloadsConversationForNewSelection(t *testing.T) {
	store := &fakeStore{readByID: map[string]Conversation{
		"a": {Thread: Thread{ID: "a"}},
		"b": {Thread: Thread{ID: "b"}},
	}}
	m := newModel(store, "/work")
	m.loading = false
	m.threads = []Thread{{ID: "a", Title: "alpha"}, {ID: "b", Title: "beta"}}
	m.applyFilter()
	m.hasConversation = true
	m.conversation = Conversation{Thread: Thread{ID: "a"}}
	updated, _ := m.Update(keyMsg("/"))
	m = updated.(model)
	updated, cmd := m.Update(keyMsg("b"))
	m = updated.(model)
	if cmd == nil || m.selectedThreadID() != "b" {
		t.Fatalf("search did not select b: selected=%s cmd=%v", m.selectedThreadID(), cmd != nil)
	}
	conversation := cmd().(conversationLoadedMsg)
	if conversation.conversation.Thread.ID != "b" {
		t.Fatalf("search read session = %q", conversation.conversation.Thread.ID)
	}
}

func TestSearchBackspaceRemovesOneUnicodeRune(t *testing.T) {
	m := newModel(nil, "/work")
	m.loading = false
	m.searching = true
	m.query = "日本"
	m.threads = []Thread{{ID: "session", Title: "日本語"}}
	m.applyFilter()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(model)
	if m.query != "日" || len(m.visibleThreads) != 1 || m.visibleThreads[0].ID != "session" {
		t.Fatalf("unicode backspace state = query:%q filtered:%#v", m.query, m.visibleThreads)
	}
}

func TestHiddenPreviewUsesFullWidthForMouseSelection(t *testing.T) {
	m := newModel(&fakeStore{}, "/work")
	m.width, m.height, m.loading = 80, 20, false
	m.showConversationPreview = false
	m.threads = makeTestThreads(3)
	m.applyFilter()
	updated, _ := m.Update(tea.MouseMsg{X: 70, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m = updated.(model)
	if m.pane != listPane || m.selectedIndex != 1 {
		t.Fatalf("full-width click did not select list item: pane=%v selected=%d", m.pane, m.selectedIndex)
	}
}

func TestActiveWriterLockIsDetectedBeforeConfirmation(t *testing.T) {
	lockDir := t.TempDir()
	lockPath := filepath.Join(lockDir, "thread.lock")
	file, err := os.Create(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	if !isLocked(lockPath) {
		t.Fatal("active writer lock was not detected")
	}
}

func TestWriterLockStatusDistinguishesMissingIdleActiveAndIOError(t *testing.T) {
	dir := t.TempDir()
	locked, err := lockStatus(filepath.Join(dir, "missing.lock"))
	if err != nil || locked {
		t.Fatalf("missing lock = locked:%v err:%v", locked, err)
	}
	path := filepath.Join(dir, "idle.lock")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	locked, err = lockStatus(path)
	if err != nil || locked {
		t.Fatalf("idle lock = locked:%v err:%v", locked, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	locked, err = lockStatus(path)
	if err != nil || !locked {
		t.Fatalf("active lock = locked:%v err:%v", locked, err)
	}
	if locked, err = lockStatus("\x00"); err == nil || locked {
		t.Fatalf("I/O lock = locked:%v err:%v", locked, err)
	}
}

func TestDeleteErrorIsRenderedAsModal(t *testing.T) {
	store := &fakeStore{
		threads:   []Thread{{ID: "session", Title: "session"}},
		deleteErr: errors.New("app-server error (-32600): thread already has an active writer"),
	}
	m := newModel(store, "/work")
	m.width, m.height, m.loading = 80, 20, false
	m.threads = store.threads
	m.applyFilter()

	updated, cmd := m.Update(keyMsg("d"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("delete check was not started")
	}
	updated, _ = m.Update(cmd().(deleteCheckMsg))
	m = updated.(model)
	if !m.confirmDelete {
		t.Fatal("delete confirmation did not open")
	}

	updated, cmd = m.Update(keyMsg("y"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("delete command was not started")
	}
	updated, _ = m.Update(cmd().(deletedMsg))
	m = updated.(model)
	view := m.View()
	if m.confirmDelete || m.err == nil {
		t.Fatalf("delete error state = confirmDelete:%v err:%v", m.confirmDelete, m.err)
	}
	if !strings.Contains(view, "Error") || !strings.Contains(view, "currently in use") {
		t.Fatalf("error modal missing: %q", view)
	}

	updated, _ = m.Update(keyMsg("esc"))
	m = updated.(model)
	if m.err != nil {
		t.Fatalf("error modal was not dismissed: %v", m.err)
	}
}

func TestScopeSwitchRestoresIndependentSelection(t *testing.T) {
	store := &fakeStore{}
	m := newModel(store, "/work")
	m.loading = false
	m.threads = makeTestThreads(10)
	m.applyFilter()
	m.selectedIndex = 6 // seventh item in the cwd-scoped list
	m.selectedByScope[scopeIndex(CurrentDirectory)] = 6

	updated, _ := m.Update(keyMsg("a"))
	m = updated.(model)
	if m.scope != AllThreads || m.selectedIndex != 6 {
		t.Fatalf("all scope selection = %d", m.selectedIndex)
	}
	updated, _ = m.Update(threadsLoadedMsg{threads: makeTestThreads(3)})
	m = updated.(model)

	updated, _ = m.Update(keyMsg("a"))
	m = updated.(model)
	if m.scope != CurrentDirectory || m.selectedIndex != 6 {
		t.Fatalf("restored cwd selection = %d, want 6", m.selectedIndex)
	}
	updated, _ = m.Update(threadsLoadedMsg{threads: makeTestThreads(10)})
	m = updated.(model)
	if m.selectedIndex != 6 {
		t.Fatalf("loaded cwd selection = %d, want 6", m.selectedIndex)
	}
}

func TestScopeSwitchErrorClearsPreviousScopeState(t *testing.T) {
	m := newModel(&fakeStore{}, "/work")
	m.width, m.height, m.loading = 80, 20, false
	m.threads = []Thread{{ID: "old", Title: "old"}}
	m.applyFilter()
	m.hasConversation = true
	m.conversation = Conversation{Thread: Thread{ID: "old"}}

	updated, _ := m.Update(keyMsg("a"))
	m = updated.(model)
	if m.scope != AllThreads || len(m.threads) != 0 || len(m.visibleThreads) != 0 || m.hasConversation {
		t.Fatalf("scope switch retained old state: scope=%v threads=%#v filtered=%#v conversation=%v", m.scope, m.threads, m.visibleThreads, m.hasConversation)
	}

	updated, _ = m.Update(threadsLoadedMsg{
		err:         errors.New("all threads unavailable"),
		requestMeta: requestMeta{generation: m.requestGeneration, scope: m.scope, cwd: m.cwd},
	})
	m = updated.(model)
	if m.err == nil || len(m.threads) != 0 || len(m.visibleThreads) != 0 || m.hasConversation {
		t.Fatalf("scope error exposed stale state: err=%v threads=%#v filtered=%#v conversation=%v", m.err, m.threads, m.visibleThreads, m.hasConversation)
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
	if m.selectedIndex != 1 || m.pane != listPane {
		t.Fatalf("mouse selected = %d, pane = %v", m.selectedIndex, m.pane)
	}
	m.pane = conversationPane
	updated, _ = m.Update(tea.MouseMsg{X: 2, Y: 5, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = updated.(model)
	if m.pane != listPane || m.selectedIndex != 2 {
		t.Fatalf("left wheel did not select list pane: pane=%v selected=%d", m.pane, m.selectedIndex)
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

func TestMouseClickOutsideVisibleListDoesNotSelectHiddenThread(t *testing.T) {
	m := newModel(&fakeStore{}, "/work")
	m.width, m.height, m.loading = 80, 12, false
	m.threads = makeTestThreads(20)
	m.applyFilter()
	m.selectedIndex = 0

	for _, y := range []int{m.height - 2, m.height - 1} {
		updated, _ := m.Update(tea.MouseMsg{X: 2, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		m = updated.(model)
		if m.selectedIndex != 0 {
			t.Fatalf("click at y=%d selected hidden thread %d", y, m.selectedIndex)
		}
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
	m.selectedIndex = 8
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
