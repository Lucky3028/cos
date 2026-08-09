package appserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/Lucky3028/cos/internal/domain"
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
	return fmt.Sprintf("app-server error (%d): %s", e.Code, domain.SanitizeSingleLine(e.Message))
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

func (c *rpcClient) close() error {
	c.finish(errRPCClosed)
	if err := c.input.Close(); err != nil {
		return err
	}
	return nil
}
