package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("COS_TEST_APP_SERVER_HELPER") == "1" {
		runTestAppServerHelper()
		return
	}
	os.Exit(m.Run())
}

func runTestAppServerHelper() {
	marker := os.Getenv("COS_TEST_APP_SERVER_MARKER")
	_, err := os.Stat(marker)
	first := os.IsNotExist(err)
	if first {
		if err := os.WriteFile(marker, []byte("started"), 0o600); err != nil {
			return
		}
	}

	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	var request struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
	}
	if err := decoder.Decode(&request); err != nil || request.Method != "initialize" {
		return
	}
	if err := encoder.Encode(map[string]any{
		"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{},
	}); err != nil {
		return
	}
	if err := decoder.Decode(&request); err != nil || request.Method != "initialized" {
		return
	}
	if os.Getenv("COS_TEST_APP_SERVER_DELETE_HELPER") == "1" {
		if first {
			// Consume the delete request but do not answer it. The client must
			// classify the request as written-but-unknown and reconnect.
			for {
				if err := decoder.Decode(&request); err != nil {
					return
				}
			}
		}
		for {
			if err := decoder.Decode(&request); err != nil {
				return
			}
			if request.Method != "thread/read" {
				if methodMarker := os.Getenv("COS_TEST_APP_SERVER_METHOD_MARKER"); methodMarker != "" {
					_ = os.WriteFile(methodMarker, []byte(request.Method), 0o600)
				}
				continue
			}
			if os.Getenv("COS_TEST_APP_SERVER_VERIFY_PRESENT") == "1" {
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"result": map[string]any{"thread": map[string]any{"id": "present"}},
				})
			} else {
				_ = encoder.Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"error": map[string]any{"code": -32602, "message": "thread not found"},
				})
			}
		}
	}
	if first {
		return
	}

	for {
		if err := decoder.Decode(&request); err != nil {
			return
		}
		if request.Method != "thread/list" {
			continue
		}
		if err := encoder.Encode(map[string]any{
			"jsonrpc": "2.0", "id": request.ID,
			"result": map[string]any{"data": []any{}},
		}); err != nil {
			return
		}
	}
}

func TestAppServerStoreReconnectWaitsForEOFProcess(t *testing.T) {
	marker := t.TempDir() + "/started"
	t.Setenv("COS_TEST_APP_SERVER_HELPER", "1")
	t.Setenv("COS_TEST_APP_SERVER_MARKER", marker)

	store := NewAppServerStore(os.Args[0])
	t.Cleanup(func() { _ = store.Close() })
	firstClient, err := store.client(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstProcess := store.process
	select {
	case <-firstClient.done:
	case <-time.After(5 * time.Second):
		t.Fatal("first app-server did not reach EOF")
	}

	if _, err := store.client(context.Background()); err != nil {
		t.Fatal(err)
	}
	if firstProcess.cmd.ProcessState == nil {
		t.Fatal("old app-server process was not waited after EOF")
	}

	if _, err := store.List(context.Background(), ThreadListRequest{Scope: AllThreads}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestThreadListResponseAcceptsArrayData(t *testing.T) {
	var response apiThreadListResponse
	if err := json.Unmarshal([]byte(`{"data":[{"id":"legacy","preview":"hello","cwd":"/work","updatedAt":10,"status":{"type":"idle"},"source":"cli"}],"nextCursor":"next"}`), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Items) != 1 || response.Data.Items[0].ID != "legacy" {
		t.Fatalf("items = %#v", response.Data.Items)
	}
	if response.NextCursor == nil || *response.NextCursor != "next" {
		t.Fatalf("next cursor = %v", response.NextCursor)
	}
}

func TestAppServerStoreListPagesAndScopesCWD(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	store := &AppServerStore{process: &appServerProcess{client: client}}
	defer client.close()

	serverDone := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		for page, cursor := range []string{"", "next"} {
			var request struct {
				ID     int64          `json:"id"`
				Params map[string]any `json:"params"`
			}
			if err := decoder.Decode(&request); err != nil {
				serverDone <- err
				return
			}
			if request.Params["cwd"] != "/work/project" {
				serverDone <- &testError{"cwd missing or incorrect"}
				return
			}
			if page == 1 && request.Params["cursor"] != cursor {
				serverDone <- &testError{"cursor missing"}
				return
			}
			items := []map[string]any{{"id": "t" + string(rune('1'+page)), "preview": "preview", "cwd": "/work/project", "updatedAt": int64(10 - page), "status": map[string]any{"type": "idle"}, "source": "cli"}}
			result := map[string]any{"data": map[string]any{"items": items}}
			if page == 0 {
				result["data"].(map[string]any)["nextCursor"] = "next"
			}
			response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}
			data, _ := json.Marshal(response)
			if _, err := serverConn.Write(append(data, '\n')); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	first, err := store.List(context.Background(), ThreadListRequest{Scope: CurrentDirectory, CWD: "/work/project"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.List(context.Background(), ThreadListRequest{Scope: CurrentDirectory, CWD: "/work/project", Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	threads := append(first.Threads, second.Threads...)
	if len(threads) != 2 || threads[0].ID != "t1" || threads[1].ID != "t2" {
		t.Fatalf("threads = %#v", threads)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestAppServerStoreSearchKeepsAllMatchesFromEachServerPage(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	store := &AppServerStore{process: &appServerProcess{client: client}}
	defer client.close()

	serverDone := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		for page := 0; page < 2; page++ {
			var request struct {
				Params map[string]any `json:"params"`
				ID     int64          `json:"id"`
			}
			if err := decoder.Decode(&request); err != nil {
				serverDone <- err
				return
			}
			if page == 0 && request.Params["cursor"] != nil {
				serverDone <- &testError{"first search request unexpectedly had a cursor"}
				return
			}
			if page == 1 && request.Params["cursor"] != "next" {
				serverDone <- &testError{"second search request did not resume at the server page boundary"}
				return
			}
			items := make([]map[string]any, 0, 100)
			if page == 0 {
				for i := 0; i < 99; i++ {
					items = append(items, map[string]any{"id": fmt.Sprintf("first-%02d", i), "name": "needle", "updatedAt": i})
				}
				items = append(items, map[string]any{"id": "first-non-match", "name": "other", "updatedAt": 100})
			} else {
				for i := 0; i < 3; i++ {
					items = append(items, map[string]any{"id": fmt.Sprintf("second-%d", i), "name": "needle", "updatedAt": i})
				}
			}
			result := map[string]any{"data": map[string]any{"items": items}}
			if page == 0 {
				result["data"].(map[string]any)["nextCursor"] = "next"
			} else {
				result["data"].(map[string]any)["nextCursor"] = "end"
			}
			if err := json.NewEncoder(serverConn).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	first, err := store.List(context.Background(), ThreadListRequest{Scope: AllThreads, Query: "needle", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Threads) != 99 || first.NextCursor != "next" || first.ScannedPages != 1 {
		t.Fatalf("first search page = count:%d next:%q scanned:%d", len(first.Threads), first.NextCursor, first.ScannedPages)
	}
	second, err := store.List(context.Background(), ThreadListRequest{Scope: AllThreads, Query: "needle", Cursor: first.NextCursor, SearchPages: first.ScannedPages, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Threads) != 3 || second.Threads[0].ID != "second-0" || second.NextCursor != "end" {
		t.Fatalf("second search page = %#v next:%q", second.Threads, second.NextCursor)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestAppServerStoreSearchStopsAtOneHundredPages(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	store := &AppServerStore{process: &appServerProcess{client: client}}
	defer client.close()

	serverDone := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		for page := 0; page < maxSearchPages; page++ {
			var request struct {
				ID     int64          `json:"id"`
				Params map[string]any `json:"params"`
			}
			if err := decoder.Decode(&request); err != nil {
				serverDone <- err
				return
			}
			next := fmt.Sprintf("cursor-%d", page+1)
			if err := json.NewEncoder(serverConn).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"data": map[string]any{"items": []any{}, "nextCursor": next}},
			}); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	page, err := store.List(context.Background(), ThreadListRequest{Scope: AllThreads, Query: "never"})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Incomplete || page.ScannedPages != maxSearchPages || page.NextCursor != "" {
		t.Fatalf("incomplete search page = %#v", page)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestAppServerStoreSearchRejectsCursorCycle(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	store := &AppServerStore{process: &appServerProcess{client: client}}
	defer client.close()

	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		for i := 0; i < 2; i++ {
			var request struct {
				ID int64 `json:"id"`
			}
			if decoder.Decode(&request) != nil {
				return
			}
			_ = json.NewEncoder(serverConn).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"data": map[string]any{"items": []any{}, "nextCursor": "cycle"}},
			})
		}
	}()

	_, err := store.List(context.Background(), ThreadListRequest{Scope: AllThreads, Query: "never"})
	if err == nil || !strings.Contains(err.Error(), "cursor cycle") {
		t.Fatalf("error = %v, want cursor cycle", err)
	}
}

func TestAppServerStoreDeleteTimeoutTreatsConfirmedAbsenceAsSuccess(t *testing.T) {
	marker := t.TempDir() + "/started"
	t.Setenv("COS_TEST_APP_SERVER_HELPER", "1")
	t.Setenv("COS_TEST_APP_SERVER_MARKER", marker)
	t.Setenv("COS_TEST_APP_SERVER_DELETE_HELPER", "1")
	store := NewAppServerStore(os.Args[0])
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.client(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := store.Delete(ctx, "gone"); err != nil {
		t.Fatalf("confirmed absence returned error: %v", err)
	}
}

func TestAppServerStoreDeleteTimeoutReportsUnknownWhenTargetRemains(t *testing.T) {
	marker := t.TempDir() + "/started"
	t.Setenv("COS_TEST_APP_SERVER_HELPER", "1")
	t.Setenv("COS_TEST_APP_SERVER_MARKER", marker)
	t.Setenv("COS_TEST_APP_SERVER_DELETE_HELPER", "1")
	t.Setenv("COS_TEST_APP_SERVER_VERIFY_PRESENT", "1")
	store := NewAppServerStore(os.Args[0])
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.client(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := store.Delete(ctx, "present")
	if !errors.Is(err, errDeleteOutcomeUnknown) {
		t.Fatalf("error = %v, want unknown delete outcome", err)
	}
}

func TestAppServerStoreDeleteInvalidJSONRechecksAfterReconnect(t *testing.T) {
	marker := t.TempDir() + "/started"
	if err := os.WriteFile(marker, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COS_TEST_APP_SERVER_HELPER", "1")
	t.Setenv("COS_TEST_APP_SERVER_MARKER", marker)
	t.Setenv("COS_TEST_APP_SERVER_DELETE_HELPER", "1")
	methodMarker := t.TempDir() + "/method"
	t.Setenv("COS_TEST_APP_SERVER_METHOD_MARKER", methodMarker)

	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	store := NewAppServerStore(os.Args[0])
	store.process = &appServerProcess{client: client}
	defer func() {
		_ = serverConn.Close()
		_ = store.Close()
	}()

	serverDone := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		var request map[string]any
		if err := decoder.Decode(&request); err != nil {
			serverDone <- err
			return
		}
		if request["method"] != "thread/delete" {
			serverDone <- &testError{"unexpected method before invalid JSON"}
			return
		}
		if _, err := serverConn.Write([]byte("{not-json}\n")); err != nil {
			serverDone <- err
			return
		}
		var extra map[string]any
		if err := decoder.Decode(&extra); err == nil {
			serverDone <- &testError{"thread/delete was retried on the old connection"}
			return
		}
		serverDone <- nil
	}()

	if err := store.Delete(context.Background(), "gone"); err != nil {
		t.Fatalf("confirmed absence after invalid JSON returned error: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if method, err := os.ReadFile(methodMarker); err == nil && string(method) == "thread/delete" {
		t.Fatal("thread/delete was retried after an unknown result")
	}
}

func TestAppServerStoreReadFallsBackToPaginatedTurns(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	store := &AppServerStore{process: &appServerProcess{client: client}}
	defer client.close()

	serverDone := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		for requestNumber := 0; requestNumber < 4; requestNumber++ {
			var request struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := decoder.Decode(&request); err != nil {
				serverDone <- err
				return
			}

			var response map[string]any
			switch requestNumber {
			case 0:
				if request.Method != "thread/read" || request.Params["includeTurns"] != true {
					serverDone <- &testError{"initial thread/read did not request turns"}
					return
				}
				response = map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"error": map[string]any{"code": -32600, "message": "includeTurns is not supported for paginated threads"},
				}
			case 1:
				if request.Method != "thread/read" || request.Params["threadId"] != "session" {
					serverDone <- &testError{"metadata-only thread/read was not requested"}
					return
				}
				if _, ok := request.Params["includeTurns"]; ok {
					serverDone <- &testError{"metadata-only thread/read included turns"}
					return
				}
				response = map[string]any{
					"jsonrpc": "2.0", "id": request.ID,
					"result": map[string]any{"thread": map[string]any{
						"id": "session", "preview": "metadata", "cwd": "/work",
						"updatedAt": 10, "status": map[string]any{"type": "idle"},
					}},
				}
			case 2, 3:
				if request.Method != "thread/turns/list" || request.Params["threadId"] != "session" ||
					request.Params["sortDirection"] != "desc" || request.Params["itemsView"] != "full" {
					serverDone <- &testError{"paginated turns request has incorrect parameters"}
					return
				}
				if requestNumber == 2 {
					if _, ok := request.Params["cursor"]; ok {
						serverDone <- &testError{"first paginated turns request unexpectedly had a cursor"}
						return
					}
					response = map[string]any{
						"jsonrpc": "2.0", "id": request.ID,
						"result": map[string]any{
							"data": []map[string]any{{"items": []map[string]any{{
								"type": "agentMessage", "text": "world",
							}}}}, "nextCursor": "next",
						},
					}
				} else {
					if request.Params["cursor"] != "next" {
						serverDone <- &testError{"next paginated turns cursor missing"}
						return
					}
					response = map[string]any{
						"jsonrpc": "2.0", "id": request.ID,
						"result": map[string]any{
							"data": []map[string]any{{"items": []map[string]any{{
								"type": "userMessage", "content": []map[string]any{{"type": "text", "text": "hello"}},
							}}}}, "nextCursor": nil,
						},
					}
				}
			}

			data, _ := json.Marshal(response)
			if _, err := serverConn.Write(append(data, '\n')); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	conversation, err := store.Read(context.Background(), "session")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Thread.ID != "session" || len(conversation.Items) != 2 ||
		conversation.Items[0].Text != "hello" || conversation.Items[1].Text != "world" {
		t.Fatalf("conversation = %#v", conversation)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestAppServerStoreReadOnlyFallsBackForUnsupportedIncludeTurns(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	store := &AppServerStore{process: &appServerProcess{client: client}}

	serverDone := make(chan int, 1)
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		var request map[string]any
		if decoder.Decode(&request) != nil {
			serverDone <- 0
			return
		}
		_ = json.NewEncoder(serverConn).Encode(map[string]any{
			"jsonrpc": "2.0", "id": request["id"],
			"error": map[string]any{"code": -32602, "message": "thread not found"},
		})
		var extra map[string]any
		if decoder.Decode(&extra) == nil {
			_ = json.NewEncoder(serverConn).Encode(map[string]any{
				"jsonrpc": "2.0", "id": extra["id"],
				"error": map[string]any{"code": -32602, "message": "fallback should not run"},
			})
			serverDone <- 2
			return
		}
		serverDone <- 1
	}()

	_, err := store.Read(context.Background(), "missing")
	if err == nil || !strings.Contains(err.Error(), "thread not found") {
		t.Fatalf("error = %v, want original read error", err)
	}
	_ = client.close()
	if got := <-serverDone; got != 1 {
		t.Fatalf("fallback request count marker = %d, want no fallback", got)
	}
}

func TestAppServerStoreReadFallbackPreservesInitialAndFallbackErrors(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	store := &AppServerStore{process: &appServerProcess{client: client}}
	defer client.close()

	serverDone := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		for number, message := range []string{
			`{"code":-32602,"message":"includeTurns is not supported"}`,
			`{"code":-32603,"message":"metadata read failed"}`,
		} {
			var request map[string]any
			if err := decoder.Decode(&request); err != nil {
				serverDone <- err
				return
			}
			if err := json.NewEncoder(serverConn).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request["id"], "error": json.RawMessage(message),
			}); err != nil {
				serverDone <- err
				return
			}
			if number == 0 && request["method"] != "thread/read" {
				serverDone <- &testError{"unexpected initial method"}
				return
			}
		}
		serverDone <- nil
	}()

	_, err := store.Read(context.Background(), "session")
	if err == nil || !strings.Contains(err.Error(), "includeTurns is not supported") || !strings.Contains(err.Error(), "metadata read failed") {
		t.Fatalf("error = %v, want both fallback errors", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestAppServerStorePaginatedReadStopsAtTurnLimit(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	store := &AppServerStore{process: &appServerProcess{client: client}}
	defer client.close()

	serverDone := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		for requestNumber := 0; requestNumber < 3; requestNumber++ {
			var request struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := decoder.Decode(&request); err != nil {
				serverDone <- err
				return
			}
			var response map[string]any
			switch requestNumber {
			case 0:
				response = map[string]any{"jsonrpc": "2.0", "id": request.ID,
					"error": map[string]any{"code": -32600, "message": "includeTurns is not supported"}}
			case 1:
				response = map[string]any{"jsonrpc": "2.0", "id": request.ID,
					"result": map[string]any{"thread": map[string]any{"id": "session", "updatedAt": 10}}}
			case 2:
				if request.Method != "thread/turns/list" || request.Params["sortDirection"] != "desc" {
					serverDone <- &testError{"turns were not requested newest first"}
					return
				}
				data := make([]map[string]any, 100)
				for index := range data {
					data[index] = map[string]any{"items": []map[string]any{{
						"type": "agentMessage", "text": fmt.Sprintf("turn-%03d", 99-index),
					}}}
				}
				response = map[string]any{"jsonrpc": "2.0", "id": request.ID,
					"result": map[string]any{"data": data, "nextCursor": "more"}}
			}
			encoded, _ := json.Marshal(response)
			if _, err := serverConn.Write(append(encoded, '\n')); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	conversation, err := store.Read(context.Background(), "session")
	if err != nil {
		t.Fatal(err)
	}
	if len(conversation.Items) != maxConversationTurns || conversation.Items[0].Text != "turn-000" ||
		conversation.Items[len(conversation.Items)-1].Text != "turn-099" || !conversation.Truncated {
		t.Fatalf("conversation = items:%d first:%q last:%q truncated:%v", len(conversation.Items), conversation.Items[0].Text, conversation.Items[len(conversation.Items)-1].Text, conversation.Truncated)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestConversationHistoryLimitKeepsNewestContentWithinByteBudget(t *testing.T) {
	old := strings.Repeat("o", 700*1024)
	large := strings.Repeat("n", 700*1024)
	turns := []apiTurn{
		{Items: []json.RawMessage{mustJSON(map[string]string{"type": "agentMessage", "text": old})}},
		{Items: []json.RawMessage{mustJSON(map[string]string{"type": "agentMessage", "text": large})}},
	}
	limited, truncated := limitTurnsChronological(turns)
	items, itemsTruncated := conversationItemsLimited(limited)
	if !truncated || itemsTruncated || len(items) != 1 || len(items[0].Text) != len(large) {
		t.Fatalf("limited history = turns:%d items:%d bytes:%d truncated:%v/%v", len(limited), len(items), len(items[0].Text), truncated, itemsTruncated)
	}
	if items[0].Text != large {
		t.Fatal("newest content was not retained")
	}
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func TestAppServerStoreListDescendantsChecksBothArchiveScopes(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	store := &AppServerStore{process: &appServerProcess{client: client}}
	defer client.close()

	serverDone := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		for page, archived := range []bool{false, true} {
			var request struct {
				ID     int64          `json:"id"`
				Params map[string]any `json:"params"`
			}
			if err := decoder.Decode(&request); err != nil {
				serverDone <- err
				return
			}
			if request.Params["ancestorThreadId"] != "parent" || request.Params["archived"] != archived {
				serverDone <- &testError{"descendant filter missing or incorrect"}
				return
			}
			if _, ok := request.Params["sourceKinds"]; ok {
				serverDone <- &testError{"descendant query must include all source kinds"}
				return
			}
			result := map[string]any{"data": []map[string]any{}}
			if page == 1 {
				result["data"] = []map[string]any{{
					"id": "child-1", "cwd": "/work", "updatedAt": 10,
					"status": map[string]any{"type": "idle"},
				}}
			}
			response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}
			data, _ := json.Marshal(response)
			if _, err := serverConn.Write(append(data, '\n')); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	threads, err := store.ListDescendants(context.Background(), "parent")
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].ID != "child-1" {
		t.Fatalf("descendants = %#v", threads)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestAppServerStoreListDescendantsWalksEveryPageInEachArchiveScope(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	store := &AppServerStore{process: &appServerProcess{client: client}}
	defer client.close()

	serverDone := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		responses := []struct {
			archived bool
			cursor   string
			next     string
			id       string
		}{
			{archived: false, next: "active-next"},
			{archived: false, cursor: "active-next", id: "active-child"},
			{archived: true, id: "archived-child"},
		}
		for _, expected := range responses {
			var request struct {
				ID     int64          `json:"id"`
				Params map[string]any `json:"params"`
			}
			if err := decoder.Decode(&request); err != nil {
				serverDone <- err
				return
			}
			if request.Params["archived"] != expected.archived {
				serverDone <- &testError{"unexpected archive scope"}
				return
			}
			if expected.cursor == "" {
				if _, ok := request.Params["cursor"]; ok {
					serverDone <- &testError{"unexpected cursor"}
					return
				}
			} else if request.Params["cursor"] != expected.cursor {
				serverDone <- &testError{"incorrect descendant cursor"}
				return
			}
			items := []map[string]any{}
			if expected.id != "" {
				items = append(items, map[string]any{"id": expected.id})
			}
			data := map[string]any{"items": items}
			if expected.next != "" {
				data["nextCursor"] = expected.next
			}
			if err := json.NewEncoder(serverConn).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"data": data},
			}); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	threads, err := store.ListDescendants(context.Background(), "parent")
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 || threads[0].ID != "active-child" || threads[1].ID != "archived-child" {
		t.Fatalf("descendants = %#v", threads)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestAppServerStoreListDescendantsRejectsCursorCycle(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	store := &AppServerStore{process: &appServerProcess{client: client}}
	defer client.close()

	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		for {
			var request map[string]any
			if decoder.Decode(&request) != nil {
				return
			}
			_ = json.NewEncoder(serverConn).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request["id"],
				"result": map[string]any{"data": map[string]any{"items": []any{}, "nextCursor": "cycle"}},
			})
		}
	}()

	_, err := store.ListDescendants(context.Background(), "parent")
	if err == nil || !strings.Contains(err.Error(), "cursor cycle") {
		t.Fatalf("error = %v, want cursor cycle", err)
	}
}

type testError struct{ message string }

func (e *testError) Error() string { return e.message }
