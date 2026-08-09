package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
)

func TestRPCClientHandlesNotificationsAndResponse(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	defer client.close()

	serverDone := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		var request map[string]any
		if err := decoder.Decode(&request); err != nil {
			serverDone <- err
			return
		}
		_, _ = serverConn.Write([]byte(`{"jsonrpc":"2.0","method":"thread/started","params":{}}` + "\n"))
		id := int(request["id"].(float64))
		response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]string{"ok": "yes"}})
		_, err := serverConn.Write(append(response, '\n'))
		serverDone <- err
	}()

	var result struct {
		OK string `json:"ok"`
	}
	if err := client.request(context.Background(), "ping", map[string]string{"x": "y"}, &result); err != nil {
		t.Fatal(err)
	}
	if result.OK != "yes" {
		t.Fatalf("result = %#v", result)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestRPCClientReturnsServerError(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	defer client.close()
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		var request map[string]any
		_ = decoder.Decode(&request)
		id := int(request["id"].(float64))
		_, _ = serverConn.Write([]byte(`{"jsonrpc":"2.0","id":` + string(rune('0'+id)) + `,"error":{"code":-1,"message":"nope"}}` + "\n"))
	}()
	if err := client.request(context.Background(), "bad", nil, nil); err == nil || err.Error() != "app-server error (-1): nope" {
		t.Fatalf("error = %v", err)
	}
}

func TestRPCClientDetectsClosedServer(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	_ = serverConn.Close()
	defer client.close()
	if err := client.request(context.Background(), "ping", nil, nil); !errors.Is(err, errRPCClosed) {
		t.Fatalf("error = %v, want closed", err)
	}
}
