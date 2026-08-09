package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
)

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

	threads, err := store.List(context.Background(), CurrentDirectory, "/work/project")
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 || threads[0].ID != "t1" || threads[1].ID != "t2" {
		t.Fatalf("threads = %#v", threads)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

type testError struct{ message string }

func (e *testError) Error() string { return e.message }
