package main

import (
	"bufio"
	"bytes"
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

const maxRPCMessageBytes = 2 << 20

var errRPCMessageTooLarge = errors.New("codex app-server JSONL message exceeds 2 MiB")

// rpcRequestTimeout identifies the only timeout which may have caused a
// request to execute: the request was written, but its response did not
// arrive before the caller's deadline. Callers must not blindly retry such a
// request because JSON-RPC methods are not necessarily idempotent.
type rpcRequestTimeout struct {
	err error
}

func (e *rpcRequestTimeout) Error() string { return "codex app-server request timed out after write" }

func (e *rpcRequestTimeout) Unwrap() error { return e.err }

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("app-server error (%d): %s", e.Code, sanitizeSingleLine(e.Message))
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

type rpcPending struct {
	responseCh chan rpcResponse
	written    bool
}

// rpcRequestOutcomeUnknown means that the complete JSON-RPC request was
// written, but the transport failed before a response could be trusted. The
// caller must not blindly retry a non-idempotent method in this state.
type rpcRequestOutcomeUnknown struct {
	err error
}

func (e *rpcRequestOutcomeUnknown) Error() string {
	return "codex app-server request outcome is unknown after write"
}

func (e *rpcRequestOutcomeUnknown) Unwrap() error { return e.err }

type rpcClient struct {
	input  io.WriteCloser
	output io.ReadCloser

	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[int64]*rpcPending
	nextID    atomic.Int64
	done      chan struct{}
	doneOnce  sync.Once
	doneErr   error
}

func newRPCClient(input io.WriteCloser, output io.ReadCloser) *rpcClient {
	c := &rpcClient{
		input: input, output: output, pending: make(map[int64]*rpcPending), done: make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *rpcClient) readLoop() {
	reader := bufio.NewReader(c.output)
	for {
		line, err := readJSONLMessage(reader)
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) == 0 {
				c.finish(errRPCClosed)
			} else {
				c.finish(fmt.Errorf("%w: %w", errRPCClosed, err))
			}
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			c.finish(fmt.Errorf("%w: invalid JSONL message: %w", errRPCClosed, err))
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
		// Keep lookup and send under the same mutex as finish's close. This
		// prevents a reader response from sending to a channel that finish has
		// already closed.
		c.pendingMu.Lock()
		if pending := c.pending[id]; pending != nil {
			select {
			case pending.responseCh <- rpcResponse{ID: msg.ID, Result: msg.Result, Error: msg.Error}:
			default:
				// A duplicate response cannot satisfy another request. Do not
				// block the reader or prevent finish from closing the channel.
			}
		}
		c.pendingMu.Unlock()
	}
}

// readJSONLMessage bounds the line before unmarshalling it. ReadBytes and
// Scanner both make it easy to allocate an unbounded token before the limit
// is checked, so consume bufio chunks explicitly instead.
func readJSONLMessage(reader *bufio.Reader) ([]byte, error) {
	message := make([]byte, 0, maxRPCMessageBytes)
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(message)+len(chunk) > maxRPCMessageBytes {
			return nil, errRPCMessageTooLarge
		}
		message = append(message, chunk...)
		if err == nil {
			return bytes.TrimSuffix(message, []byte{'\n'}), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			if errors.Is(err, io.EOF) && len(message) > 0 {
				return bytes.TrimSuffix(message, []byte{'\n'}), nil
			}
			return nil, err
		}
	}
}

func (c *rpcClient) finish(err error) {
	c.doneOnce.Do(func() {
		c.pendingMu.Lock()
		c.doneErr = err
		close(c.done)
		for id, pending := range c.pending {
			delete(c.pending, id)
			close(pending.responseCh)
		}
		c.pendingMu.Unlock()
	})
}

func (c *rpcClient) request(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	responseCh := make(chan rpcResponse, 1)
	pending := &rpcPending{responseCh: responseCh}
	c.pendingMu.Lock()
	select {
	case <-c.done:
		err := c.doneErr
		c.pendingMu.Unlock()
		return err
	default:
	}
	c.pending[id] = pending
	c.pendingMu.Unlock()

	payload := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", id, method, params}
	writeDone := make(chan error, 1)
	go func() {
		err := c.write(payload)
		if err == nil {
			c.markWritten(pending)
		}
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			c.removePending(id)
			return err
		}
	case <-ctx.Done():
		// io.Write has no context-aware contract. Closing stdio is the only
		// reliable way to unblock a stuck pipe write; the store will reconnect
		// on the next operation because this client is now finished.
		c.abortWrite()
		<-writeDone
		c.removePending(id)
		if c.isWritten(pending) {
			return &rpcRequestTimeout{err: ctx.Err()}
		}
		return ctx.Err()
	case <-c.done:
		c.abortWrite()
		writeErr := <-writeDone
		c.removePending(id)
		c.pendingMu.Lock()
		err := c.doneErr
		c.pendingMu.Unlock()
		if err == nil {
			err = errRPCClosed
		}
		if c.isWritten(pending) || writeErr == nil {
			return &rpcRequestOutcomeUnknown{err: err}
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		c.removePending(id)
		if c.isWritten(pending) {
			return &rpcRequestTimeout{err: err}
		}
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
			if c.isWritten(pending) {
				return &rpcRequestOutcomeUnknown{err: err}
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
		return &rpcRequestTimeout{err: ctx.Err()}
	case <-c.done:
		c.removePending(id)
		c.pendingMu.Lock()
		err := c.doneErr
		c.pendingMu.Unlock()
		if err == nil {
			err = errRPCClosed
		}
		if c.isWritten(pending) {
			return &rpcRequestOutcomeUnknown{err: err}
		}
		return err
	}
}

func (c *rpcClient) notify(method string, params any) error {
	return c.notifyContext(context.Background(), method, params)
}

func (c *rpcClient) notifyContext(ctx context.Context, method string, params any) error {
	payload := struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", method, params}
	writeDone := make(chan error, 1)
	go func() { writeDone <- c.write(payload) }()
	select {
	case err := <-writeDone:
		return err
	case <-ctx.Done():
		c.abortWrite()
		return ctx.Err()
	case <-c.done:
		c.abortWrite()
		c.pendingMu.Lock()
		err := c.doneErr
		c.pendingMu.Unlock()
		if err == nil {
			err = errRPCClosed
		}
		return err
	}
}

func (c *rpcClient) write(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	data = append(data, '\n')
	n, err := c.input.Write(data)
	if err != nil {
		closedErr := fmt.Errorf("%w: %v", errRPCClosed, err)
		c.finish(closedErr)
		return closedErr
	}
	if n != len(data) {
		closedErr := fmt.Errorf("%w: %w", errRPCClosed, io.ErrShortWrite)
		c.finish(closedErr)
		return closedErr
	}
	return nil
}

func (c *rpcClient) abortWrite() {
	c.finish(errRPCClosed)
	_ = c.input.Close()
}

func (c *rpcClient) removePending(id int64) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

func (c *rpcClient) markWritten(pending *rpcPending) {
	c.pendingMu.Lock()
	pending.written = true
	c.pendingMu.Unlock()
}

func (c *rpcClient) isWritten(pending *rpcPending) bool {
	c.pendingMu.Lock()
	written := pending.written
	c.pendingMu.Unlock()
	return written
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

	closeOnce sync.Once
	closeErr  error
	waitOnce  sync.Once
	waitErr   error
}

func (p *appServerProcess) close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		clientErr := p.client.close()
		waitErr := p.wait()
		if clientErr != nil {
			p.closeErr = clientErr
		} else {
			p.closeErr = waitErr
		}
	})
	return p.closeErr
}

func (p *appServerProcess) wait() error {
	if p == nil || p.cmd == nil {
		return nil
	}
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
	})
	return p.waitErr
}

func startAppServer(_ context.Context, command string, args ...string) (*appServerProcess, error) {
	// The process belongs to the store, not to the request which happened to
	// create it. A canceled request closes the client transport when needed;
	// otherwise the same app-server can serve later requests.
	cmd := exec.Command(command, args...)
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
