package appserver

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
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

func TestConversationItemKindsAndConversions(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		kind ConversationItemKind
		text string
	}{
		{name: "user", raw: `{"type":"userMessage","content":[{"type":"text","text":"hello"}]}`, kind: ConversationItemKindUser, text: "hello"},
		{name: "assistant", raw: `{"type":"agentMessage","text":"world"}`, kind: ConversationItemKindAssistant, text: "world"},
		{name: "activity", raw: `{"type":"commandExecution","command":"go test"}`, kind: ConversationItemKindActivity, text: "▶ command: go test"},
		{name: "unknown", raw: `{"type":"futureItem"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := conversationItem(json.RawMessage(test.raw))
			if item.Kind != test.kind || item.Text != test.text {
				t.Fatalf("item = %#v, want kind:%q text:%q", item, test.kind, test.text)
			}
		})
	}
}

func TestConversationItemKindValues(t *testing.T) {
	if got := []ConversationItemKind{ConversationItemKindUser, ConversationItemKindAssistant, ConversationItemKindActivity}; !reflect.DeepEqual(got, []ConversationItemKind{"user", "assistant", "activity"}) {
		t.Fatalf("conversation item kinds = %#v", got)
	}
	if ConversationItemKind("future") == ConversationItemKindActivity {
		t.Fatal("unknown kind was treated as an activity kind")
	}
}

func TestSourceNameUsesExplicitWireKeys(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "string", raw: `"cli"`, want: "cli"},
		{name: "type", raw: `{"type":"vscode"}`, want: "vscode"},
		{name: "kind", raw: `{"kind":"appServer"}`, want: "appServer"},
		{name: "type preferred", raw: `{"kind":"fallback","type":"preferred"}`, want: "preferred"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := sourceName(json.RawMessage(test.raw)); got != test.want {
				t.Fatalf("sourceName(%s) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestAppServerTextIsSanitizedWithoutDroppingConversationNewlines(t *testing.T) {
	unsafe := "\x1b[31mred\x1b[0m\x1b]0;title\a\x07\b\r\nnext\u0080"
	name := unsafe
	thread := apiThread{Name: &name, Preview: unsafe, CWD: unsafe}.toThread()
	if strings.ContainsAny(thread.Title+thread.Preview+thread.CWD, "\x1b\x07\b\r\u0080") {
		t.Fatalf("thread fields retained control characters: %#v", thread)
	}
	if !strings.Contains(thread.Title, "red") || !strings.Contains(thread.Title, "next") {
		t.Fatalf("sanitized title lost content: %q", thread.Title)
	}
	item := conversationItem(json.RawMessage("{\"type\":\"agentMessage\",\"text\":\"first\\u001b[2J\\nsecond\\r\\b\\u0007\"}"))
	if item.Text != "first\nsecond" {
		t.Fatalf("sanitized conversation = %q", item.Text)
	}
	activity := conversationItem(json.RawMessage("{\"type\":\"mcpToolCall\",\"server\":\"srv\\u001b[31m\",\"tool\":\"tool\\u0007\"}"))
	if strings.ContainsAny(activity.Text, "\x1b\a") {
		t.Fatalf("sanitized activity retained control characters: %q", activity.Text)
	}
	if strings.ContainsAny((&rpcError{Code: 1, Message: unsafe}).Error(), "\x1b\a\b\r\u0080") {
		t.Fatal("sanitized error retained control characters")
	}
}

func TestThreadTitleUsesNameAndPreviewFallback(t *testing.T) {
	name := "Named session"
	withName := apiThread{Name: &name, Preview: "first message"}.toThread()
	if withName.Title != "Named session" {
		t.Fatalf("title = %q", withName.Title)
	}
	withoutName := apiThread{Preview: "first message"}.toThread()
	if withoutName.Title != "" {
		t.Fatalf("preview fallback title = %q", withoutName.Title)
	}
}
