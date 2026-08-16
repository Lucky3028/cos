package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

func (m *model) newRequestMeta(threadID string) requestMeta {
	m.requestGeneration++
	return requestMeta{
		generation:  m.requestGeneration,
		scope:       m.scope,
		cwd:         m.cwd,
		cursor:      m.pendingCursor,
		query:       m.query,
		searchPages: m.pendingSearchPages,
		threadID:    threadID,
	}
}

func (m *model) beginConversationLoad(threadID string) tea.Cmd {
	meta := m.newRequestMeta(threadID)
	ctx, cancel := m.beginAsyncRequest()
	m.hasConversation = false
	return readConversationWithContext(m.store, ctx, cancel, meta)
}

func (m *model) beginListLoad() tea.Cmd {
	m.pageCursor = ""
	m.nextCursor = ""
	m.previousCursors = nil
	m.previousSearchPages = nil
	m.pendingCursor = ""
	m.pendingSearchPages = 0
	m.pageSearchStart = 0
	m.searchPages = 0
	m.selectLastOnPage = false
	m.searchIncomplete = false
	meta := m.newRequestMeta("")
	ctx, cancel := m.beginAsyncRequest()
	m.loading = true
	m.err = nil
	return loadThreadsWithContext(m.store, ctx, cancel, meta)
}

func (m *model) beginPageLoad(cursor string, selectLast bool) tea.Cmd {
	return m.beginPageLoadWithSearchPages(cursor, selectLast, m.searchPages)
}

func (m *model) beginPageLoadWithSearchPages(cursor string, selectLast bool, searchPages int) tea.Cmd {
	m.pendingCursor = cursor
	m.pendingSearchPages = searchPages
	m.selectLastOnPage = selectLast
	meta := m.newRequestMeta("")
	ctx, cancel := m.beginAsyncRequest()
	m.loading = true
	m.err = nil
	m.hasConversation = false
	m.threads = nil
	m.visibleThreads = nil
	return loadThreadsWithContext(m.store, ctx, cancel, meta)
}

const asyncRequestTimeout = 30 * time.Second

func (m *model) beginAsyncRequest() (context.Context, context.CancelFunc) {
	if m.requestCancel != nil {
		m.requestCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), asyncRequestTimeout)
	m.requestContext = ctx
	m.requestCancel = cancel
	return ctx, cancel
}

func (m *model) clearListState() {
	m.threads = nil
	m.visibleThreads = nil
	m.listOffset = 0
	m.hasConversation = false
	m.conversation = Conversation{}
	m.viewport.SetContent("")
}

func (m model) matchesRequest(meta requestMeta, requireThread bool) bool {
	// Zero metadata is retained for small, synchronous unit-test messages and
	// for callers of the legacy command helpers. Every real UI command carries
	// a non-zero generation.
	if meta.generation == 0 {
		if requireThread && meta.threadID != "" {
			selected, ok := m.selectedThread()
			return ok && selected.ID == meta.threadID
		}
		return true
	}
	if meta.generation != m.requestGeneration || meta.scope != m.scope || meta.cwd != m.cwd || meta.query != m.query {
		return false
	}
	if requireThread {
		selected, ok := m.selectedThread()
		return ok && selected.ID == meta.threadID
	}
	return true
}

func loadThreadsWithMeta(store SessionStore, meta requestMeta) tea.Cmd {
	return withAsyncRequest(func(ctx context.Context) tea.Msg {
		return loadThreadsMessage(ctx, store, meta)
	})
}

func loadThreadsWithContext(store SessionStore, ctx context.Context, cancel context.CancelFunc, meta requestMeta) tea.Cmd {
	return withManagedRequest(ctx, cancel, func(ctx context.Context) tea.Msg {
		return loadThreadsMessage(ctx, store, meta)
	})
}

func loadThreadsMessage(ctx context.Context, store SessionStore, meta requestMeta) tea.Msg {
	page, err := store.List(ctx, threadListRequest(meta))
	return threadsLoadedMsg{page: page, err: err, requestMeta: meta}
}

func withAsyncRequest(fn func(context.Context) tea.Msg) tea.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), asyncRequestTimeout)
	return withManagedRequest(ctx, cancel, fn)
}

func readConversationWithContext(store SessionStore, ctx context.Context, cancel context.CancelFunc, meta requestMeta) tea.Cmd {
	return withManagedRequest(ctx, cancel, func(ctx context.Context) tea.Msg {
		return readConversationMessage(ctx, store, meta)
	})
}

func withManagedRequest(ctx context.Context, cancel context.CancelFunc, fn func(context.Context) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		defer cancel()
		return fn(ctx)
	}
}

func readConversationMessage(ctx context.Context, store SessionStore, meta requestMeta) tea.Msg {
	conversation, err := store.Read(ctx, meta.threadID)
	return conversationLoadedMsg{conversation: conversation, err: err, requestMeta: meta}
}

func threadListRequest(meta requestMeta) ThreadListRequest {
	return ThreadListRequest{
		Scope: meta.scope, CWD: meta.cwd, Cursor: meta.cursor,
		Limit: defaultThreadPageSize, Query: meta.query, SearchPages: meta.searchPages,
	}
}

func validateIdleSession(ctx context.Context, store SessionStore, sessionID string, checkDescendants bool) (Conversation, error) {
	locked, err := writerLockStatus(sessionID)
	if err != nil {
		return Conversation{}, fmt.Errorf("check writer lock for session %s: %w", sessionID, err)
	}
	if locked {
		return Conversation{Thread: Thread{ID: sessionID, Active: true}}, sessionInUseError()
	}
	conversation, err := store.Read(ctx, sessionID)
	if err != nil {
		return Conversation{}, err
	}
	if conversation.Thread.Active {
		return conversation, sessionInUseError()
	}
	if !checkDescendants {
		return conversation, nil
	}
	descendants, err := store.ListDescendants(ctx, sessionID)
	if err != nil {
		return Conversation{}, fmt.Errorf("cannot verify descendants of session %s: %w", sessionID, err)
	}
	if len(descendants) > 0 {
		return Conversation{}, descendantSessionError(sessionID, descendants)
	}
	return conversation, nil
}

func checkDeleteMessage(ctx context.Context, store SessionStore, meta requestMeta) tea.Msg {
	conversation, err := validateIdleSession(ctx, store, meta.threadID, true)
	if err != nil {
		return deleteCheckMsg{err: err, requestMeta: meta}
	}
	return deleteCheckMsg{conversation: conversation, requestMeta: meta}
}

func checkResumeMessage(ctx context.Context, store SessionStore, meta requestMeta) tea.Msg {
	conversation, err := validateIdleSession(ctx, store, meta.threadID, false)
	return resumeCheckMsg{conversation: conversation, err: err, requestMeta: meta}
}

func deleteThreadMessage(ctx context.Context, store SessionStore, meta requestMeta) tea.Msg {
	if _, err := validateIdleSession(ctx, store, meta.threadID, true); err != nil {
		return deletedMsg{err: err, requestMeta: meta}
	}
	deleteErr := store.Delete(ctx, meta.threadID)
	page, listErr := store.List(ctx, threadListRequest(meta))
	if listErr != nil {
		if deleteErr != nil {
			return deletedMsg{err: fmt.Errorf("delete failed (%v); reload failed: %w", deleteError(deleteErr), listErr), requestMeta: meta}
		}
		return deletedMsg{err: fmt.Errorf("deleted session %s, but reload failed: %w", meta.threadID, listErr), requestMeta: meta}
	}
	if deleteErr != nil {
		return deletedMsg{page: page, err: deleteError(deleteErr), requestMeta: meta}
	}
	for _, thread := range page.Threads {
		if thread.ID == meta.threadID {
			return deletedMsg{page: page, err: fmt.Errorf("session %s is still present after deletion", meta.threadID), requestMeta: meta}
		}
	}
	return deletedMsg{page: page, requestMeta: meta}
}

func descendantSessionError(sessionID string, descendants []Thread) error {
	return fmt.Errorf("session %s has %d descendant session(s); delete is unavailable", sessionID, len(descendants))
}
