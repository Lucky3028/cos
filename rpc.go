package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
)

var errRPCClosed = errors.New("codex app-server connection closed")

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("app-server error (%d): %s", e.Code, e.Message)
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcClient struct {
	input  io.WriteCloser
	output io.ReadCloser

	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[int64]chan rpcResponse
	nextID    atomic.Int64
	done      chan struct{}
	doneOnce  sync.Once
	doneErr   error
}

func newRPCClient(input io.WriteCloser, output io.ReadCloser) *rpcClient {
	c := &rpcClient{
		input: input, output: output, pending: make(map[int64]chan rpcResponse), done: make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *rpcClient) readLoop() {
	decoder := json.NewDecoder(bufio.NewReader(c.output))
	for {
		var msg rpcMessage
		if err := decoder.Decode(&msg); err != nil {
			if !errors.Is(err, io.EOF) {
				c.finish(fmt.Errorf("%w: %v", errRPCClosed, err))
			} else {
				c.finish(errRPCClosed)
			}
			return
		}
		// Notifications and server requests are deliberately not exposed to the
		// read-only organizer. They can be interleaved with ordinary responses.
		if len(msg.ID) == 0 || msg.Method != "" {
			continue
		}
		var id int64
		if err := json.Unmarshal(msg.ID, &id); err != nil {
			continue
		}
		c.pendingMu.Lock()
		ch := c.pending[id]
		c.pendingMu.Unlock()
		if ch != nil {
			ch <- rpcResponse{ID: msg.ID, Result: msg.Result, Error: msg.Error}
		}
	}
}

func (c *rpcClient) finish(err error) {
	c.doneOnce.Do(func() {
		c.pendingMu.Lock()
		c.doneErr = err
		close(c.done)
		for id, ch := range c.pending {
			delete(c.pending, id)
			close(ch)
		}
		c.pendingMu.Unlock()
	})
}

func (c *rpcClient) request(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	responseCh := make(chan rpcResponse, 1)
	c.pendingMu.Lock()
	select {
	case <-c.done:
		err := c.doneErr
		c.pendingMu.Unlock()
		return err
	default:
	}
	c.pending[id] = responseCh
	c.pendingMu.Unlock()

	payload := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", id, method, params}
	if err := c.write(payload); err != nil {
		c.removePending(id)
		return err
	}

	select {
	case response, ok := <-responseCh:
		if !ok {
			c.pendingMu.Lock()
			err := c.doneErr
			c.pendingMu.Unlock()
			if err == nil {
				err = errRPCClosed
			}
			return err
		}
		c.removePending(id)
		if response.Error != nil {
			return response.Error
		}
		if result == nil || len(response.Result) == 0 || string(response.Result) == "null" {
			return nil
		}
		return json.Unmarshal(response.Result, result)
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case <-c.done:
		c.removePending(id)
		c.pendingMu.Lock()
		err := c.doneErr
		c.pendingMu.Unlock()
		if err == nil {
			err = errRPCClosed
		}
		return err
	}
}

func (c *rpcClient) notify(method string, params any) error {
	return c.write(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", method, params})
}

func (c *rpcClient) write(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.input.Write(append(data, '\n')); err != nil {
		closedErr := fmt.Errorf("%w: %v", errRPCClosed, err)
		c.finish(closedErr)
		return closedErr
	}
	return nil
}

func (c *rpcClient) removePending(id int64) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

func (c *rpcClient) close() error {
	c.finish(errRPCClosed)
	if err := c.input.Close(); err != nil {
		return err
	}
	return nil
}

type appServerProcess struct {
	client *rpcClient
	cmd    *exec.Cmd
}

func startAppServer(ctx context.Context, command string, args ...string) (*appServerProcess, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	client := newRPCClient(stdin, stdout)
	return &appServerProcess{client: client, cmd: cmd}, nil
}
