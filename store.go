package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxConversationTurns      = 100
	maxConversationBytes      = 1 << 20
	deleteVerificationTimeout = 5 * time.Second
)

var errDeleteOutcomeUnknown = errors.New("thread/delete outcome is unknown")

type AppServerStore struct {
	command string
	args    []string

	mu      sync.Mutex
	process *appServerProcess
}

func NewAppServerStore(command string, args ...string) *AppServerStore {
	return &AppServerStore{command: command, args: append([]string(nil), args...)}
}

func NewDefaultStore() *AppServerStore {
	return NewAppServerStore("codex", "app-server", "--stdio")
}

func (s *AppServerStore) Close() error {
	s.mu.Lock()
	process := s.process
	s.process = nil
	s.mu.Unlock()
	if process == nil {
		return nil
	}
	return process.close()
}

func (s *AppServerStore) client(ctx context.Context) (*rpcClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.process != nil {
		select {
		case <-s.process.client.done:
			process := s.process
			s.process = nil
			_ = process.close()
		default:
			return s.process.client, nil
		}
	}
	process, err := startAppServer(ctx, s.command, s.args...)
	if err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	if err := process.client.request(ctx, "initialize", map[string]any{
		"clientInfo": map[string]string{
			"name": "cos", "title": "cos", "version": version,
		},
		"capabilities": map[string]any{"experimentalApi": true},
	}, nil); err != nil {
		_ = process.close()
		return nil, fmt.Errorf("initialize codex app-server: %w", err)
	}
	if err := process.client.notifyContext(ctx, "initialized", map[string]any{}); err != nil {
		_ = process.close()
		return nil, fmt.Errorf("initialize codex app-server: %w", err)
	}
	s.process = process
	return process.client, nil
}

func (s *AppServerStore) List(ctx context.Context, request ThreadListRequest) (ThreadPage, error) {
	if request.Scope != CurrentDirectory && request.Scope != AllThreads {
		return ThreadPage{}, fmt.Errorf("unknown list scope %d", request.Scope)
	}
	client, err := s.client(ctx)
	if err != nil {
		return ThreadPage{}, err
	}
	params := map[string]any{
		"archived":      false,
		"sourceKinds":   []string{"cli", "vscode", "appServer"},
		"sortKey":       "updated_at",
		"sortDirection": "desc",
		"limit":         threadPageLimit(request.Limit),
	}
	if request.Cursor != "" {
		params["cursor"] = request.Cursor
	}
	switch request.Scope {
	case CurrentDirectory:
		params["cwd"] = request.CWD
	case AllThreads:
	}

	if request.Query == "" {
		return listThreadPage(ctx, client, params)
	}
	return listSearchPage(ctx, client, params, request)
}

func (s *AppServerStore) ListDescendants(ctx context.Context, id string) ([]Thread, error) {
	client, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	for _, archived := range []bool{false, true} {
		page, err := listThreadPage(ctx, client, map[string]any{
			"ancestorThreadId": id,
			"archived":         archived,
			"sortKey":          "updated_at",
			"sortDirection":    "desc",
			"limit":            1,
		})
		if err != nil {
			return nil, fmt.Errorf("list descendants (archived=%t): %w", archived, err)
		}
		if len(page.Threads) > 0 {
			return page.Threads, nil
		}
	}
	return nil, nil
}

func threadPageLimit(limit int) int {
	if limit <= 0 || limit > defaultThreadPageSize {
		return defaultThreadPageSize
	}
	return limit
}

func listThreadPage(ctx context.Context, client *rpcClient, params map[string]any) (ThreadPage, error) {
	var response apiThreadListResponse
	if err := client.request(ctx, "thread/list", params, &response); err != nil {
		return ThreadPage{}, err
	}
	page := ThreadPage{Threads: make([]Thread, 0, len(response.Data.Items))}
	for _, item := range response.Data.Items {
		page.Threads = append(page.Threads, item.toThread())
	}
	if nextCursor := response.Data.NextCursor; nextCursor != nil {
		page.NextCursor = *nextCursor
	} else if response.NextCursor != nil {
		page.NextCursor = *response.NextCursor
	}
	return page, nil
}

func listSearchPage(ctx context.Context, client *rpcClient, params map[string]any, request ThreadListRequest) (ThreadPage, error) {
	result := ThreadPage{Threads: make([]Thread, 0, threadPageLimit(request.Limit))}
	seen := make(map[string]struct{})
	cursor := request.Cursor
	if cursor != "" {
		seen[cursor] = struct{}{}
	}
	searchedPages := request.SearchPages
	if searchedPages < 0 || searchedPages > maxSearchPages {
		return ThreadPage{}, fmt.Errorf("invalid search page count %d", searchedPages)
	}
	for searchedPages < maxSearchPages {
		if cursor == "" {
			delete(params, "cursor")
		} else {
			params["cursor"] = cursor
		}
		page, err := listThreadPage(ctx, client, params)
		if err != nil {
			return ThreadPage{}, err
		}
		searchedPages++
		result.ScannedPages++
		matches := make([]Thread, 0, len(page.Threads))
		for _, thread := range page.Threads {
			if matchesThreadQuery(thread, request.Query) {
				matches = append(matches, thread)
			}
		}
		// Keep an entire matching app-server page. Returning only the first
		// matches would make the rest of this page unreachable on the next UI
		// request because the opaque cursor advances past it.
		if len(matches) > 0 {
			result.Threads = matches
			result.NextCursor = page.NextCursor
			result.Incomplete = searchedPages >= maxSearchPages && page.NextCursor != ""
			return result, nil
		}
		if page.NextCursor == "" {
			return result, nil
		}
		if _, ok := seen[page.NextCursor]; ok {
			return ThreadPage{}, fmt.Errorf("thread/list cursor cycle detected at %q", page.NextCursor)
		}
		seen[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	result.Incomplete = true
	return result, nil
}

func matchesThreadQuery(thread Thread, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	return strings.Contains(strings.ToLower(thread.Title), query) ||
		strings.Contains(strings.ToLower(thread.Preview), query) ||
		strings.Contains(strings.ToLower(thread.CWD), query)
}

func (s *AppServerStore) Read(ctx context.Context, id string) (Conversation, error) {
	client, err := s.client(ctx)
	if err != nil {
		return Conversation{}, err
	}
	var response struct {
		Thread apiThread `json:"thread"`
	}
	truncated := false
	if err := client.request(ctx, "thread/read", map[string]any{
		"threadId": id, "includeTurns": true,
	}, &response); err != nil {
		// Some app-server versions reject includeTurns for paginated threads.
		// Retry with a metadata-only read and hydrate the turns through the
		// pagination API, while retaining compatibility with older servers that
		// only support the original thread/read request.
		var metadata struct {
			Thread apiThread `json:"thread"`
		}
		if metadataErr := client.request(ctx, "thread/read", map[string]any{
			"threadId": id,
		}, &metadata); metadataErr != nil {
			return Conversation{}, err
		}
		turns, turnsTruncated, turnsErr := listThreadTurns(ctx, client, id)
		if turnsErr != nil {
			return Conversation{}, err
		}
		metadata.Thread.Turns = turns
		truncated = turnsTruncated
		response.Thread = metadata.Thread
	}
	limitedTurns, turnsTruncated := limitTurnsChronological(response.Thread.Turns)
	response.Thread.Turns = limitedTurns
	truncated = truncated || turnsTruncated
	thread := response.Thread.toThread()
	items, itemsTruncated := conversationItemsLimited(response.Thread.Turns)
	return Conversation{Thread: thread, Items: items, Truncated: truncated || itemsTruncated}, nil
}

func listThreadTurns(ctx context.Context, client *rpcClient, id string) ([]apiTurn, bool, error) {
	var newestFirst []apiTurn
	var cursor string
	seenCursors := make(map[string]struct{})
	for {
		params := map[string]any{
			"threadId":      id,
			"limit":         100,
			"sortDirection": "desc",
			"itemsView":     "full",
		}
		if cursor != "" {
			params["cursor"] = cursor
		}

		var page apiTurnsListResponse
		if err := client.request(ctx, "thread/turns/list", params, &page); err != nil {
			return nil, false, err
		}
		for _, turn := range page.Data {
			if len(newestFirst) >= maxConversationTurns {
				return reverseTurns(newestFirst), true, nil
			}
			turnBytes := turnDisplayBytes(turn)
			if len(newestFirst) > 0 && turnDisplayBytesSum(newestFirst)+turnBytes > maxConversationBytes {
				return reverseTurns(newestFirst), true, nil
			}
			newestFirst = append(newestFirst, turn)
			if turnBytes > maxConversationBytes {
				return reverseTurns(newestFirst), true, nil
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return reverseTurns(newestFirst), false, nil
		}
		if len(newestFirst) >= maxConversationTurns || turnDisplayBytesSum(newestFirst) >= maxConversationBytes {
			return reverseTurns(newestFirst), true, nil
		}
		nextCursor := *page.NextCursor
		if nextCursor == cursor {
			return nil, false, fmt.Errorf("thread/turns/list cursor cycle detected at %q", nextCursor)
		}
		if _, seen := seenCursors[nextCursor]; seen {
			return nil, false, fmt.Errorf("thread/turns/list cursor cycle detected at %q", nextCursor)
		}
		seenCursors[nextCursor] = struct{}{}
		cursor = nextCursor
	}
}

func (s *AppServerStore) Delete(ctx context.Context, id string) error {
	client, err := s.client(ctx)
	if err != nil {
		return err
	}
	if err := client.request(ctx, "thread/delete", map[string]string{"threadId": id}, nil); err != nil {
		if !isWrittenRPCOutcomeUnknown(err) {
			return err
		}
		verifyCtx, cancel := context.WithTimeout(context.Background(), deleteVerificationTimeout)
		defer cancel()
		verifyClient, reconnectErr := s.reconnect(verifyCtx)
		if reconnectErr != nil {
			return fmt.Errorf("%w: unable to reconnect to verify session %s: %v", errDeleteOutcomeUnknown, id, reconnectErr)
		}
		absent, verifyErr := s.verifyThreadAbsent(verifyCtx, verifyClient, id)
		if verifyErr == nil && absent {
			return nil
		}
		if verifyErr != nil {
			return fmt.Errorf("%w: unable to verify session %s after uncertain result: %v", errDeleteOutcomeUnknown, id, verifyErr)
		}
		return fmt.Errorf("%w: session %s may still exist", errDeleteOutcomeUnknown, id)
	}
	return nil
}

func isWrittenRPCOutcomeUnknown(err error) bool {
	var timeout *rpcRequestTimeout
	var unknown *rpcRequestOutcomeUnknown
	return errors.As(err, &timeout) || errors.As(err, &unknown)
}

func (s *AppServerStore) reconnect(ctx context.Context) (*rpcClient, error) {
	s.mu.Lock()
	process := s.process
	s.process = nil
	s.mu.Unlock()
	if process != nil {
		_ = process.close()
	}
	return s.client(ctx)
}

func (s *AppServerStore) verifyThreadAbsent(ctx context.Context, client *rpcClient, id string) (bool, error) {
	var response struct {
		Thread apiThread `json:"thread"`
	}
	if err := client.request(ctx, "thread/read", map[string]string{"threadId": id}, &response); err != nil {
		if isThreadNotFoundError(err) {
			return true, nil
		}
		return false, err
	}
	if response.Thread.ID != id {
		return false, fmt.Errorf("thread/read returned unexpected session %q", response.Thread.ID)
	}
	return false, nil
}

func isThreadNotFoundError(err error) bool {
	var rpcErr *rpcError
	if errors.As(err, &rpcErr) {
		err = errors.New(rpcErr.Message)
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not found") || strings.Contains(message, "does not exist") || strings.Contains(message, "unknown thread")
}

type apiThreadListResponse struct {
	Data struct {
		Items      []apiThread `json:"items"`
		NextCursor *string     `json:"nextCursor"`
	} `json:"data"`
	NextCursor *string `json:"nextCursor"`
}

type apiTurnsListResponse struct {
	Data       []apiTurn `json:"data"`
	NextCursor *string   `json:"nextCursor"`
}

// Older app-server versions return data as an array, while newer versions
// wrap it in {items, nextCursor}. Keep the wire compatibility at this edge.
func (r *apiThreadListResponse) UnmarshalJSON(data []byte) error {
	var envelope struct {
		Data       json.RawMessage `json:"data"`
		NextCursor *string         `json:"nextCursor"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	r.NextCursor = envelope.NextCursor
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}

	var first byte
	for _, value := range envelope.Data {
		if value > ' ' {
			first = value
			break
		}
	}
	switch first {
	case '[':
		return json.Unmarshal(envelope.Data, &r.Data.Items)
	case '{':
		return json.Unmarshal(envelope.Data, &r.Data)
	default:
		return fmt.Errorf("thread/list data must be an array or object")
	}
}

type apiThread struct {
	ID   string  `json:"id"`
	Name *string `json:"name"`
	// title was used by some older app-server responses.
	LegacyTitle *string         `json:"title"`
	Preview     string          `json:"preview"`
	CWD         string          `json:"cwd"`
	UpdatedAt   int64           `json:"updatedAt"`
	Status      json.RawMessage `json:"status"`
	Source      json.RawMessage `json:"source"`
	Turns       []apiTurn       `json:"turns"`
}

type apiTurn struct {
	Items []json.RawMessage `json:"items"`
}

func (t apiThread) toThread() Thread {
	status := sanitizeSingleLine(statusType(t.Status))
	title := sanitizeTerminalText(stringValue(t.Name), true)
	if title == "" {
		title = sanitizeTerminalText(stringValue(t.LegacyTitle), true)
	}
	return Thread{
		ID: sanitizeSingleLine(t.ID), Title: title, Preview: sanitizeTerminalText(t.Preview, true), CWD: sanitizeSingleLine(t.CWD),
		Updated: time.Unix(t.UpdatedAt, 0), Active: status == "active", Status: status,
		Source: sanitizeSingleLine(sourceName(t.Source)),
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func statusType(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var object struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &object)
	return object.Type
}

func sourceName(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil {
		for key := range object {
			return key
		}
	}
	return "unknown"
}

func conversationItems(turns []apiTurn) []ConversationItem {
	items, _ := conversationItemsLimited(turns)
	return items
}

func conversationItemsLimited(turns []apiTurn) ([]ConversationItem, bool) {
	var result []ConversationItem
	usedBytes := 0
	for _, turn := range turns {
		for _, raw := range turn.Items {
			item := conversationItem(raw)
			if item.Text != "" {
				if usedBytes+len(item.Text) > maxConversationBytes {
					remaining := maxConversationBytes - usedBytes
					if remaining > 0 {
						item.Text = truncateUTF8(item.Text, remaining)
						if item.Text != "" {
							result = append(result, item)
						}
					}
					return result, true
				}
				result = append(result, item)
				usedBytes += len(item.Text)
			}
		}
	}
	return result, false
}

func conversationItem(raw json.RawMessage) ConversationItem {
	var base struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &base) != nil {
		return ConversationItem{}
	}
	switch base.Type {
	case "userMessage":
		var value struct {
			Content []json.RawMessage `json:"content"`
		}
		_ = json.Unmarshal(raw, &value)
		var parts []string
		for _, content := range value.Content {
			var text struct {
				Type string `json:"type"`
				Text string `json:"text"`
				Path string `json:"path"`
				URL  string `json:"url"`
				Name string `json:"name"`
			}
			if json.Unmarshal(content, &text) == nil {
				switch text.Type {
				case "text":
					parts = append(parts, sanitizeTerminalText(text.Text, true))
				case "localImage", "localAudio":
					parts = append(parts, "["+text.Type+": "+sanitizeSingleLine(text.Path)+"]")
				case "image", "audio":
					parts = append(parts, "["+text.Type+"]")
				case "skill":
					parts = append(parts, "[skill: "+sanitizeSingleLine(text.Path)+"]")
				case "mention":
					parts = append(parts, "[mention: "+sanitizeSingleLine(text.Name)+"]")
				}
			}
		}
		return ConversationItem{Kind: "user", Text: strings.TrimSpace(strings.Join(parts, "\n"))}
	case "agentMessage":
		var value struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &value)
		return ConversationItem{Kind: "assistant", Text: strings.TrimSpace(sanitizeTerminalText(value.Text, true))}
	case "commandExecution":
		var value struct {
			Command string          `json:"command"`
			Status  json.RawMessage `json:"status"`
		}
		_ = json.Unmarshal(raw, &value)
		return ConversationItem{Kind: "activity", Text: "▶ command: " + oneLine(value.Command)}
	case "fileChange":
		var value struct {
			Changes []struct {
				Path string          `json:"path"`
				Kind json.RawMessage `json:"kind"`
			} `json:"changes"`
		}
		_ = json.Unmarshal(raw, &value)
		paths := make([]string, 0, len(value.Changes))
		for _, change := range value.Changes {
			paths = append(paths, sanitizeSingleLine(change.Path))
		}
		return ConversationItem{Kind: "activity", Text: "✎ file change: " + strings.Join(paths, ", ")}
	case "mcpToolCall":
		var value struct {
			Server string `json:"server"`
			Tool   string `json:"tool"`
		}
		_ = json.Unmarshal(raw, &value)
		return ConversationItem{Kind: "activity", Text: "⚙ MCP: " + sanitizeSingleLine(value.Server) + "/" + sanitizeSingleLine(value.Tool)}
	case "dynamicToolCall":
		var value struct {
			Namespace *string `json:"namespace"`
			Tool      string  `json:"tool"`
		}
		_ = json.Unmarshal(raw, &value)
		prefix := ""
		if value.Namespace != nil && *value.Namespace != "" {
			prefix = sanitizeSingleLine(*value.Namespace) + "/"
		}
		return ConversationItem{Kind: "activity", Text: "⚙ tool: " + prefix + sanitizeSingleLine(value.Tool)}
	case "webSearch":
		var value struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(raw, &value)
		return ConversationItem{Kind: "activity", Text: "⌕ web search: " + oneLine(value.Query)}
	case "collabAgentToolCall", "subAgentActivity":
		return ConversationItem{Kind: "activity", Text: "◆ sub-agent activity"}
	default:
		return ConversationItem{}
	}
}

func oneLine(value string) string {
	return sanitizeSingleLine(value)
}

func limitTurnsChronological(turns []apiTurn) ([]apiTurn, bool) {
	newestFirst := make([]apiTurn, 0, len(turns))
	for index := len(turns) - 1; index >= 0; index-- {
		newestFirst = append(newestFirst, turns[index])
	}
	selected, truncated := selectTurnsNewestFirst(newestFirst)
	return reverseTurns(selected), truncated
}

func selectTurnsNewestFirst(turns []apiTurn) ([]apiTurn, bool) {
	selected := make([]apiTurn, 0, min(len(turns), maxConversationTurns))
	usedBytes := 0
	for index, turn := range turns {
		if len(selected) >= maxConversationTurns {
			return selected, true
		}
		turnBytes := turnDisplayBytes(turn)
		if len(selected) > 0 && usedBytes+turnBytes > maxConversationBytes {
			return selected, true
		}
		selected = append(selected, turn)
		usedBytes += turnBytes
		if turnBytes > maxConversationBytes {
			return selected, true
		}
		if usedBytes >= maxConversationBytes && index+1 < len(turns) {
			return selected, true
		}
	}
	return selected, len(selected) < len(turns)
}

func reverseTurns(turns []apiTurn) []apiTurn {
	for left, right := 0, len(turns)-1; left < right; left, right = left+1, right-1 {
		turns[left], turns[right] = turns[right], turns[left]
	}
	return turns
}

func turnDisplayBytes(turn apiTurn) int {
	items, _ := conversationItemsLimited([]apiTurn{turn})
	total := 0
	for _, item := range items {
		total += len(item.Text)
	}
	return total
}

func turnDisplayBytesSum(turns []apiTurn) int {
	total := 0
	for _, turn := range turns {
		total += turnDisplayBytes(turn)
	}
	return total
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
