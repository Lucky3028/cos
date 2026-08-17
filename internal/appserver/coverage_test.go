package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type coverageWriteCloser struct {
	writes   [][]byte
	writeErr error
	short    bool
	closed   bool
}

func (w *coverageWriteCloser) Write(data []byte) (int, error) {
	w.writes = append(w.writes, append([]byte(nil), data...))
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	if w.short {
		return len(data) - 1, nil
	}
	return len(data), nil
}

func (w *coverageWriteCloser) Close() error {
	w.closed = true
	return nil
}

func newCoverageRPCClient(input io.WriteCloser) *rpcClient {
	return &rpcClient{
		input: input, pending: make(map[int64]*rpcPending), done: make(chan struct{}),
	}
}

func TestAPIThreadListResponseAcceptsBothWireShapes(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		items      int
		nextCursor string
	}{
		{name: "missing data", input: "{}", items: 0},
		{name: "null data", input: "{\"data\":null}", items: 0},
		{name: "array data", input: "{\"data\":[{\"id\":\"legacy\"}],\"nextCursor\":\"outer\"}", items: 1, nextCursor: "outer"},
		{name: "object data", input: "{\"data\":{\"items\":[{\"id\":\"modern\"}],\"nextCursor\":\"inner\"}}", items: 1, nextCursor: "inner"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var response apiThreadListResponse
			if err := json.Unmarshal([]byte(test.input), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Data.Items) != test.items {
				t.Fatalf("items = %d, want %d", len(response.Data.Items), test.items)
			}
			if response.Data.NextCursor != nil && *response.Data.NextCursor != test.nextCursor {
				t.Fatalf("data cursor = %q, want %q", *response.Data.NextCursor, test.nextCursor)
			}
			if response.Data.NextCursor == nil && response.NextCursor != nil && *response.NextCursor != test.nextCursor {
				t.Fatalf("outer cursor = %q, want %q", *response.NextCursor, test.nextCursor)
			}
		})
	}
}

func TestAPIThreadListResponseRejectsInvalidData(t *testing.T) {
	for _, input := range []string{
		"{\"data\":1}",
		"{\"data\":\"invalid\"}",
		"{\"data\":true}",
	} {
		t.Run(input, func(t *testing.T) {
			var response apiThreadListResponse
			if err := json.Unmarshal([]byte(input), &response); err == nil || !strings.Contains(err.Error(), "array or object") {
				t.Fatalf("error = %v, want invalid data shape error", err)
			}
		})
	}

	var response apiThreadListResponse
	if err := json.Unmarshal([]byte("{\"data\":"), &response); err == nil {
		t.Fatal("malformed envelope was accepted")
	}
}

func TestWireValueConversionsHandleFallbacks(t *testing.T) {
	if got := statusType(json.RawMessage("\"active\"")); got != "active" {
		t.Fatalf("string status = %q", got)
	}
	if got := statusType(json.RawMessage("{\"type\":\"idle\"}")); got != "idle" {
		t.Fatalf("object status = %q", got)
	}
	if got := statusType(json.RawMessage("{\"unexpected\":true}")); got != "" {
		t.Fatalf("unknown status = %q", got)
	}

	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: "\"cli\"", want: "cli"},
		{raw: "{\"type\":\"vscode\",\"kind\":\"fallback\"}", want: "vscode"},
		{raw: "{\"kind\":\"appServer\"}", want: "appServer"},
		{raw: "{\"other\":\"value\"}", want: "unknown"},
	} {
		if got := sourceName(json.RawMessage(test.raw)); got != test.want {
			t.Fatalf("sourceName(%s) = %q, want %q", test.raw, got, test.want)
		}
	}

	legacyTitle := "legacy title"
	thread := (apiThread{ID: "id", LegacyTitle: &legacyTitle, Status: json.RawMessage("{\"type\":\"active\"}")}).toThread()
	if thread.Title != legacyTitle || !thread.Active || thread.Status != "active" {
		t.Fatalf("legacy thread conversion = %#v", thread)
	}
}

func TestConversationUserAndActivityVariants(t *testing.T) {
	user := conversationItem(json.RawMessage("{" +
		"\"type\":\"userMessage\"," +
		"\"content\":[" +
		"{\"type\":\"text\",\"text\":\"hello\"}," +
		"{\"type\":\"localImage\",\"path\":\"/tmp/image\"}," +
		"{\"type\":\"localAudio\",\"path\":\"/tmp/audio\"}," +
		"{\"type\":\"image\"}," +
		"{\"type\":\"audio\"}," +
		"{\"type\":\"skill\",\"path\":\"demo\"}," +
		"{\"type\":\"mention\",\"name\":\"alice\"}," +
		"{\"type\":\"ignored\"}," +
		"1]}"))
	want := "hello\n[localImage: /tmp/image]\n[localAudio: /tmp/audio]\n[image]\n[audio]\n[skill: demo]\n[mention: alice]"
	if user.Kind != ConversationItemKindUser || user.Text != want {
		t.Fatalf("user item = %#v, want %q", user, want)
	}
	if item := conversationItem(json.RawMessage("{invalid}")); item != (ConversationItem{}) {
		t.Fatalf("invalid item = %#v", item)
	}

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "file change", raw: "{\"type\":\"fileChange\",\"changes\":[]}", want: "✎ file change: "},
		{name: "dynamic tool", raw: "{\"type\":\"dynamicToolCall\",\"namespace\":\"ns\",\"tool\":\"run\"}", want: "⚙ tool: ns/run"},
		{name: "dynamic tool without namespace", raw: "{\"type\":\"dynamicToolCall\",\"tool\":\"run\"}", want: "⚙ tool: run"},
		{name: "web search", raw: "{\"type\":\"webSearch\",\"query\":\"go testing\"}", want: "⌕ web search: go testing"},
		{name: "collab", raw: "{\"type\":\"collabAgentToolCall\"}", want: "◆ sub-agent activity"},
		{name: "sub-agent", raw: "{\"type\":\"subAgentActivity\"}", want: "◆ sub-agent activity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := conversationItem(json.RawMessage(test.raw)); got.Text != test.want {
				t.Fatalf("activity = %q, want %q", got.Text, test.want)
			}
		})
	}
}

func TestConversationItemsLimitedTruncatesAndPreservesUTF8(t *testing.T) {
	largeText := strings.Repeat("a", maxConversationBytes-1)
	turns := []apiTurn{
		{Items: []json.RawMessage{json.RawMessage("{\"type\":\"agentMessage\",\"text\":\"" + largeText + "\"}")}},
		{Items: []json.RawMessage{json.RawMessage("{\"type\":\"agentMessage\",\"text\":\"xy\"}")}},
	}
	items, truncated := conversationItemsLimited(turns)
	if !truncated || len(items) != 2 || items[1].Text != "x" {
		t.Fatalf("limited items = len:%d truncated:%v last:%q", len(items), truncated, items[len(items)-1].Text)
	}

	for _, test := range []struct {
		value string
		limit int
		want  string
	}{
		{value: "日本語", limit: 0, want: ""},
		{value: "日本語", limit: 4, want: "日"},
		{value: "日本語", limit: 20, want: "日本語"},
	} {
		if got := truncateUTF8(test.value, test.limit); got != test.want {
			t.Fatalf("truncateUTF8(%q, %d) = %q, want %q", test.value, test.limit, got, test.want)
		}
	}
}

func TestSelectTurnsNewestFirstStopsAtTurnLimit(t *testing.T) {
	turns := make([]apiTurn, maxConversationTurns+1)
	selected, truncated := selectTurnsNewestFirst(turns)
	if len(selected) != maxConversationTurns || !truncated {
		t.Fatalf("selected = %d truncated:%v", len(selected), truncated)
	}
}

func TestRPCWriteAndResponseErrors(t *testing.T) {
	t.Run("marshal error", func(t *testing.T) {
		client := newCoverageRPCClient(&coverageWriteCloser{})
		if err := client.write(func() {}); err == nil {
			t.Fatal("unsupported JSON value was accepted")
		}
	})

	t.Run("write error", func(t *testing.T) {
		client := newCoverageRPCClient(&coverageWriteCloser{writeErr: errors.New("broken pipe")})
		if err := client.write(map[string]string{"method": "test"}); !errors.Is(err, errRPCClosed) {
			t.Fatalf("error = %v, want closed transport error", err)
		}
	})

	t.Run("short write", func(t *testing.T) {
		client := newCoverageRPCClient(&coverageWriteCloser{short: true})
		if err := client.write(map[string]string{"method": "test"}); !errors.Is(err, errRPCClosed) {
			t.Fatalf("error = %v, want short-write transport error", err)
		}
	})

	t.Run("apply response", func(t *testing.T) {
		client := newCoverageRPCClient(&coverageWriteCloser{})
		if err := client.applyRPCResponse(rpcResponse{Error: &rpcError{Code: 1, Message: "failed"}}, nil); err == nil {
			t.Fatal("RPC error was ignored")
		}
		if err := client.applyRPCResponse(rpcResponse{Result: json.RawMessage("null")}, nil); err != nil {
			t.Fatalf("null result error = %v", err)
		}
		var result map[string]string
		if err := client.applyRPCResponse(rpcResponse{Result: json.RawMessage("{\"value\":\"ok\"}")}, &result); err != nil || result["value"] != "ok" {
			t.Fatalf("decoded result = %#v err:%v", result, err)
		}
		if err := client.applyRPCResponse(rpcResponse{Result: json.RawMessage("{")}, &result); err == nil {
			t.Fatal("malformed result was accepted")
		}
	})
}

func TestRPCNotificationAndClosedClientPaths(t *testing.T) {
	writer := &coverageWriteCloser{}
	client := newCoverageRPCClient(writer)
	if err := client.notify("initialized", map[string]bool{"ready": true}); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(writer.writes[0], &payload); err != nil {
		t.Fatal(err)
	}
	if payload["method"] != "initialized" || payload["jsonrpc"] != "2.0" {
		t.Fatalf("notification payload = %#v", payload)
	}

	wantErr := errors.New("connection failed")
	closed := newCoverageRPCClient(&coverageWriteCloser{})
	closed.finish(wantErr)
	if err := closed.notifyContext(context.Background(), "ignored", nil); !errors.Is(err, wantErr) {
		t.Fatalf("closed notification error = %v, want %v", err, wantErr)
	}
	if err := closed.addPending(1, &rpcPending{responseCh: make(chan rpcResponse, 1)}); !errors.Is(err, wantErr) {
		t.Fatalf("closed pending error = %v, want %v", err, wantErr)
	}

	unwritten := newCoverageRPCClient(&coverageWriteCloser{})
	unwritten.finish(wantErr)
	pending := &rpcPending{responseCh: make(chan rpcResponse, 1)}
	if err := unwritten.waitForResponse(context.Background(), 1, pending, nil); !errors.Is(err, wantErr) {
		t.Fatalf("unwritten wait error = %v, want %v", err, wantErr)
	}
}

func TestRPCHelpersHandleUnsetState(t *testing.T) {
	client := newCoverageRPCClient(&coverageWriteCloser{})
	if !errors.Is(client.connectionError(), errRPCClosed) {
		t.Fatal("connectionError did not return default closed error")
	}
	ctxErr := context.Canceled
	if err := client.timeoutAfterWrite(&rpcPending{}, ctxErr); !errors.Is(err, ctxErr) {
		t.Fatalf("unwritten timeout = %v", err)
	}
	if err := client.timeoutAfterWrite(&rpcPending{written: true}, ctxErr); !errors.Is(err, ctxErr) {
		t.Fatalf("written timeout = %v", err)
	}
	if _, err := responseID(rpcMessage{ID: json.RawMessage("\"invalid\"")}); err == nil {
		t.Fatal("non-numeric response ID was accepted")
	}
}

func TestStoreArgumentAndErrorHelpers(t *testing.T) {
	store := NewAppServerStore("unused")
	if _, err := store.List(context.Background(), ThreadListRequest{Scope: ListScope(99)}); err == nil {
		t.Fatal("unknown list scope was accepted")
	}

	for _, test := range []struct {
		input int
		want  int
	}{
		{input: -1, want: defaultThreadPageSize},
		{input: 0, want: defaultThreadPageSize},
		{input: 7, want: 7},
		{input: defaultThreadPageSize + 1, want: defaultThreadPageSize},
	} {
		if got := threadPageLimit(test.input); got != test.want {
			t.Fatalf("threadPageLimit(%d) = %d, want %d", test.input, got, test.want)
		}
	}

	for _, searchPages := range []int{-1, maxSearchPages + 1} {
		if _, err := listSearchPage(context.Background(), nil, map[string]any{}, ThreadListRequest{Query: "query", SearchPages: searchPages}); err == nil {
			t.Fatalf("search page count %d was accepted", searchPages)
		}
	}

	for _, test := range []struct {
		thread Thread
		query  string
		want   bool
	}{
		{thread: Thread{Title: "Deploy"}, query: " deploy ", want: true},
		{thread: Thread{Preview: "Database migration"}, query: "MIGRATION", want: true},
		{thread: Thread{CWD: "/work/project"}, query: "PROJECT", want: true},
		{thread: Thread{Title: "Deploy"}, query: "missing", want: false},
	} {
		if got := matchesThreadQuery(test.thread, test.query); got != test.want {
			t.Fatalf("matchesThreadQuery(%#v, %q) = %v, want %v", test.thread, test.query, got, test.want)
		}
	}
}

func TestStartAppServerAndProcessNilPaths(t *testing.T) {
	if _, err := startAppServer(context.Background(), t.TempDir()+"/missing-command"); err == nil {
		t.Fatal("missing app-server command was accepted")
	}
	var process *appServerProcess
	if err := process.close(); err != nil {
		t.Fatalf("nil process close = %v", err)
	}
	if err := process.wait(); err != nil {
		t.Fatalf("nil process wait = %v", err)
	}
}
