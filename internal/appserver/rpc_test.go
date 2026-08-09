package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type blockingWriteCloser struct {
	started chan struct{}
	release chan struct{}
	closed  chan struct{}
}

func newBlockingWriteCloser() *blockingWriteCloser {
	return &blockingWriteCloser{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
}

func (w *blockingWriteCloser) Write([]byte) (int, error) {
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	<-w.release
	return 0, errors.New("write released")
}

func (w *blockingWriteCloser) Close() error {
	select {
	case <-w.release:
	default:
		close(w.release)
	}
	select {
	case <-w.closed:
	default:
		close(w.closed)
	}
	return nil
}

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

type responseAfterWriteInput struct {
	written chan struct{}
}

func (w *responseAfterWriteInput) Write(data []byte) (int, error) {
	select {
	case <-w.written:
	default:
		close(w.written)
	}
	return len(data), nil
}

func (w *responseAfterWriteInput) Close() error { return nil }

type responseAfterWriteOutput struct {
	written  <-chan struct{}
	response []byte
}

func (r *responseAfterWriteOutput) Read(data []byte) (int, error) {
	<-r.written
	if len(r.response) == 0 {
		return 0, io.EOF
	}
	n := copy(data, r.response)
	r.response = r.response[n:]
	return n, nil
}

func (r *responseAfterWriteOutput) Close() error { return nil }

func TestRPCClientPrefersBufferedResponseWhenTransportFinishes(t *testing.T) {
	for attempt := 0; attempt < 50; attempt++ {
		written := make(chan struct{})
		client := newRPCClient(
			&responseAfterWriteInput{written: written},
			&responseAfterWriteOutput{
				written:  written,
				response: []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":"yes"}}` + "\n"),
			},
		)
		var result struct {
			OK string `json:"ok"`
		}
		err := client.request(context.Background(), "buffered-response", nil, &result)
		_ = client.close()
		if err != nil {
			t.Fatalf("attempt %d: request error = %v", attempt, err)
		}
		if result.OK != "yes" {
			t.Fatalf("attempt %d: result = %#v", attempt, result)
		}
	}
}

func TestRPCClientMarksWrittenRequestUnknownWhenConnectionCloses(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	defer client.close()

	serverDone := make(chan error, 1)
	go func() {
		var request map[string]any
		if err := json.NewDecoder(bufio.NewReader(serverConn)).Decode(&request); err != nil {
			serverDone <- err
			return
		}
		serverDone <- serverConn.Close()
	}()

	err := client.request(context.Background(), "written-then-closed", nil, nil)
	var unknown *rpcRequestOutcomeUnknown
	if !errors.As(err, &unknown) || !errors.Is(err, errRPCClosed) {
		t.Fatalf("error = %v, want written request outcome unknown", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestRPCClientMarksWrittenRequestUnknownAfterInvalidJSON(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	defer client.close()

	serverDone := make(chan error, 1)
	go func() {
		var request map[string]any
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		if err := decoder.Decode(&request); err != nil {
			serverDone <- err
			return
		}
		_, err := serverConn.Write([]byte("{not-json}\n"))
		serverDone <- err
	}()

	err := client.request(context.Background(), "written-then-invalid", nil, nil)
	var unknown *rpcRequestOutcomeUnknown
	if !errors.As(err, &unknown) || !errors.Is(err, errRPCClosed) {
		t.Fatalf("error = %v, want written request outcome unknown", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestRPCClientCancellationRemovesPendingRequest(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	defer client.close()
	serverDone := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		var request map[string]any
		serverDone <- decoder.Decode(&request)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := client.request(ctx, "never-responds", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	client.pendingMu.Lock()
	pending := len(client.pending)
	client.pendingMu.Unlock()
	if pending != 0 {
		t.Fatalf("pending requests = %d, want 0", pending)
	}
	_ = serverConn.Close()
}

func TestRPCClientCancellationUnblocksWriteAndClosesTransport(t *testing.T) {
	writer := newBlockingWriteCloser()
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(writer, clientConn)
	defer func() {
		_ = serverConn.Close()
		_ = client.close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	requestDone := make(chan error, 1)
	go func() { requestDone <- client.request(ctx, "blocked-write", nil, nil) }()
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("write did not start")
	}
	select {
	case err := <-requestDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled write did not return")
	}
	select {
	case <-writer.closed:
	case <-time.After(time.Second):
		t.Fatal("canceled write did not close transport")
	}
	client.pendingMu.Lock()
	pending := len(client.pending)
	client.pendingMu.Unlock()
	if pending != 0 {
		t.Fatalf("pending requests = %d, want 0", pending)
	}
}

func TestRPCClientRejectsOversizedJSONLMessageBeforeDecode(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	defer client.close()

	go func() {
		decoder := json.NewDecoder(bufio.NewReader(serverConn))
		var request map[string]any
		if decoder.Decode(&request) != nil {
			return
		}
		response, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": request["id"],
			"result": map[string]string{"padding": strings.Repeat("x", maxRPCMessageBytes)},
		})
		_, _ = serverConn.Write(append(response, '\n'))
	}()

	err := client.request(context.Background(), "large", nil, nil)
	if !errors.Is(err, errRPCMessageTooLarge) {
		t.Fatalf("error = %v, want oversized-message error", err)
	}
}

func TestRPCClientMarksWrittenRequestTimeout(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := newRPCClient(clientConn, clientConn)
	defer client.close()

	serverDone := make(chan error, 1)
	go func() {
		var request map[string]any
		serverDone <- json.NewDecoder(bufio.NewReader(serverConn)).Decode(&request)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := client.request(ctx, "written-but-no-response", nil, nil)
	var timeout *rpcRequestTimeout
	if !errors.As(err, &timeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want written request timeout", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	_ = serverConn.Close()
}
