package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

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
	_ = process.client.close()
	return process.cmd.Wait()
}

func (s *AppServerStore) client(ctx context.Context) (*rpcClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.process != nil {
		select {
		case <-s.process.client.done:
			s.process = nil
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
			"name": "cos", "title": "Codex Session Organizer", "version": version,
		},
		"capabilities": map[string]any{"experimentalApi": true},
	}, nil); err != nil {
		_ = process.client.close()
		_ = process.cmd.Wait()
		return nil, fmt.Errorf("initialize codex app-server: %w", err)
	}
	if err := process.client.notify("initialized", map[string]any{}); err != nil {
		_ = process.client.close()
		_ = process.cmd.Wait()
		return nil, fmt.Errorf("initialize codex app-server: %w", err)
	}
	s.process = process
	return process.client, nil
}

func (s *AppServerStore) List(ctx context.Context, scope ListScope, cwd string) ([]Thread, error) {
	client, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	params := map[string]any{
		"archived":      false,
		"sourceKinds":   []string{"cli", "vscode", "appServer"},
		"sortKey":       "updated_at",
		"sortDirection": "desc",
		"limit":         100,
	}
	switch scope {
	case CurrentDirectory:
		params["cwd"] = cwd
	case AllThreads:
	default:
		return nil, fmt.Errorf("unknown list scope %d", scope)
	}

	var all []Thread
	for {
		var page apiThreadListResponse
		if err := client.request(ctx, "thread/list", params, &page); err != nil {
			return nil, err
		}
		for _, item := range page.Data.Items {
			all = append(all, item.toThread())
		}
		nextCursor := page.Data.NextCursor
		if nextCursor == nil {
			nextCursor = page.NextCursor
		}
		if nextCursor == nil || *nextCursor == "" {
			break
		}
		params["cursor"] = *nextCursor
	}
	return all, nil
}

func (s *AppServerStore) Read(ctx context.Context, id string) (Conversation, error) {
	client, err := s.client(ctx)
	if err != nil {
		return Conversation{}, err
	}
	var response struct {
		Thread apiThread `json:"thread"`
	}
	if err := client.request(ctx, "thread/read", map[string]any{
		"threadId": id, "includeTurns": true,
	}, &response); err != nil {
		return Conversation{}, err
	}
	thread := response.Thread.toThread()
	return Conversation{Thread: thread, Items: conversationItems(response.Thread.Turns)}, nil
}

func (s *AppServerStore) Delete(ctx context.Context, id string) error {
	client, err := s.client(ctx)
	if err != nil {
		return err
	}
	if err := client.request(ctx, "thread/delete", map[string]string{"threadId": id}, nil); err != nil {
		return err
	}
	return nil
}

type apiThreadListResponse struct {
	Data struct {
		Items      []apiThread `json:"items"`
		NextCursor *string     `json:"nextCursor"`
	} `json:"data"`
	NextCursor *string `json:"nextCursor"`
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
	status := statusType(t.Status)
	title := stringValue(t.Name)
	if title == "" {
		title = stringValue(t.LegacyTitle)
	}
	return Thread{
		ID: t.ID, Title: title, Preview: t.Preview, CWD: t.CWD,
		Updated: time.Unix(t.UpdatedAt, 0), Active: status == "active", Status: status,
		Source: sourceName(t.Source),
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
	var result []ConversationItem
	for _, turn := range turns {
		for _, raw := range turn.Items {
			item := conversationItem(raw)
			if item.Text != "" {
				result = append(result, item)
			}
		}
	}
	return result
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
					parts = append(parts, text.Text)
				case "localImage", "localAudio":
					parts = append(parts, "["+text.Type+": "+text.Path+"]")
				case "image", "audio":
					parts = append(parts, "["+text.Type+"]")
				case "skill":
					parts = append(parts, "[skill: "+text.Path+"]")
				case "mention":
					parts = append(parts, "[mention: "+text.Name+"]")
				}
			}
		}
		return ConversationItem{Kind: "user", Text: strings.TrimSpace(strings.Join(parts, "\n"))}
	case "agentMessage":
		var value struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &value)
		return ConversationItem{Kind: "assistant", Text: strings.TrimSpace(value.Text)}
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
			paths = append(paths, change.Path)
		}
		return ConversationItem{Kind: "activity", Text: "✎ file change: " + strings.Join(paths, ", ")}
	case "mcpToolCall":
		var value struct {
			Server string `json:"server"`
			Tool   string `json:"tool"`
		}
		_ = json.Unmarshal(raw, &value)
		return ConversationItem{Kind: "activity", Text: "⚙ MCP: " + value.Server + "/" + value.Tool}
	case "dynamicToolCall":
		var value struct {
			Namespace *string `json:"namespace"`
			Tool      string  `json:"tool"`
		}
		_ = json.Unmarshal(raw, &value)
		prefix := ""
		if value.Namespace != nil && *value.Namespace != "" {
			prefix = *value.Namespace + "/"
		}
		return ConversationItem{Kind: "activity", Text: "⚙ tool: " + prefix + value.Tool}
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
	return strings.Join(strings.Fields(value), " ")
}
