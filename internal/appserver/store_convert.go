package appserver

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

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
	var object struct {
		Type string `json:"type"`
		Kind string `json:"kind"`
	}
	if json.Unmarshal(raw, &object) == nil {
		if object.Type != "" {
			return object.Type
		}
		if object.Kind != "" {
			return object.Kind
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
		return conversationUserItem(raw)
	case "agentMessage":
		return conversationAssistantItem(raw)
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "webSearch", "collabAgentToolCall", "subAgentActivity":
		return conversationActivityItem(base.Type, raw)
	default:
		return ConversationItem{}
	}
}

func conversationUserItem(raw json.RawMessage) ConversationItem {
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
			Name string `json:"name"`
		}
		if json.Unmarshal(content, &text) != nil {
			continue
		}
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
	return ConversationItem{Kind: ConversationItemKindUser, Text: strings.TrimSpace(strings.Join(parts, "\n"))}
}

func conversationAssistantItem(raw json.RawMessage) ConversationItem {
	var value struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &value)
	return ConversationItem{Kind: ConversationItemKindAssistant, Text: strings.TrimSpace(sanitizeTerminalText(value.Text, true))}
}

func conversationActivityItem(itemType string, raw json.RawMessage) ConversationItem {
	activity := ConversationItem{Kind: ConversationItemKindActivity}
	switch itemType {
	case "commandExecution":
		var value struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(raw, &value)
		activity.Text = "▶ command: " + sanitizeSingleLine(value.Command)
	case "fileChange":
		var value struct {
			Changes []struct {
				Path string `json:"path"`
			} `json:"changes"`
		}
		_ = json.Unmarshal(raw, &value)
		paths := make([]string, 0, len(value.Changes))
		for _, change := range value.Changes {
			paths = append(paths, sanitizeSingleLine(change.Path))
		}
		activity.Text = "✎ file change: " + strings.Join(paths, ", ")
	case "mcpToolCall":
		var value struct {
			Server string `json:"server"`
			Tool   string `json:"tool"`
		}
		_ = json.Unmarshal(raw, &value)
		activity.Text = "⚙ MCP: " + sanitizeSingleLine(value.Server) + "/" + sanitizeSingleLine(value.Tool)
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
		activity.Text = "⚙ tool: " + prefix + sanitizeSingleLine(value.Tool)
	case "webSearch":
		var value struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(raw, &value)
		activity.Text = "⌕ web search: " + sanitizeSingleLine(value.Query)
	case "collabAgentToolCall", "subAgentActivity":
		activity.Text = "◆ sub-agent activity"
	}
	return activity
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
