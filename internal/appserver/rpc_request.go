package appserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

func (c *rpcClient) request(ctx context.Context, method string, params any, result any) error {
	requestID := c.nextID.Add(1)
	pending := &rpcPending{responseCh: make(chan rpcResponse, 1)}
	if err := c.addPending(requestID, pending); err != nil {
		return err
	}

	payload := rpcRequest{JSONRPC: "2.0", ID: requestID, Method: method, Params: params}
	response, hasResponse, writeErr := c.writeWithContext(ctx, pending, payload)
	if writeErr != nil {
		c.removePending(requestID)
		return writeErr
	}
	if hasResponse {
		c.removePending(requestID)
		return c.applyRPCResponse(response, result)
	}
	if err := ctx.Err(); err != nil {
		c.removePending(requestID)
		return c.timeoutAfterWrite(pending, err)
	}

	return c.waitForResponse(ctx, requestID, pending, result)
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

func (c *rpcClient) addPending(requestID int64, pending *rpcPending) error {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	select {
	case <-c.done:
		return c.doneErr
	default:
		c.pending[requestID] = pending
		return nil
	}
}

func (c *rpcClient) writeWithContext(ctx context.Context, pending *rpcPending, payload any) (rpcResponse, bool, error) {
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
		return rpcResponse{}, false, err
	case <-ctx.Done():
		// io.Write has no context-aware contract. Closing stdio is the only
		// reliable way to unblock a stuck pipe write.
		c.abortWrite()
		<-writeDone
		if c.isWritten(pending) {
			return rpcResponse{}, false, &rpcRequestTimeout{err: ctx.Err()}
		}
		return rpcResponse{}, false, ctx.Err()
	case <-c.done:
		c.abortWrite()
		writeErr := <-writeDone
		if response, ok := bufferedRPCResponse(pending.responseCh); ok {
			return response, true, nil
		}
		if c.isWritten(pending) || writeErr == nil {
			return rpcResponse{}, false, &rpcRequestOutcomeUnknown{err: c.connectionError()}
		}
		return rpcResponse{}, false, c.connectionError()
	}
}

func (c *rpcClient) waitForResponse(ctx context.Context, requestID int64, pending *rpcPending, result any) error {
	select {
	case response, ok := <-pending.responseCh:
		c.removePending(requestID)
		if !ok {
			if c.isWritten(pending) {
				return &rpcRequestOutcomeUnknown{err: c.connectionError()}
			}
			return c.connectionError()
		}
		return c.applyRPCResponse(response, result)
	case <-ctx.Done():
		c.removePending(requestID)
		return &rpcRequestTimeout{err: ctx.Err()}
	case <-c.done:
		if response, ok := bufferedRPCResponse(pending.responseCh); ok {
			c.removePending(requestID)
			return c.applyRPCResponse(response, result)
		}
		c.removePending(requestID)
		if c.isWritten(pending) {
			return &rpcRequestOutcomeUnknown{err: c.connectionError()}
		}
		return c.connectionError()
	}
}

func (c *rpcClient) timeoutAfterWrite(pending *rpcPending, err error) error {
	if pending != nil && c.isWritten(pending) {
		return &rpcRequestTimeout{err: err}
	}
	return err
}

func (c *rpcClient) connectionError() error {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if c.doneErr != nil {
		return c.doneErr
	}
	return errRPCClosed
}

func bufferedRPCResponse(responseCh <-chan rpcResponse) (rpcResponse, bool) {
	select {
	case response, ok := <-responseCh:
		return response, ok
	default:
		return rpcResponse{}, false
	}
}

func (c *rpcClient) applyRPCResponse(response rpcResponse, result any) error {
	if response.Error != nil {
		return response.Error
	}
	if result == nil || len(response.Result) == 0 || string(response.Result) == "null" {
		return nil
	}
	return json.Unmarshal(response.Result, result)
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
		return c.connectionError()
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

func (c *rpcClient) removePending(requestID int64) {
	c.pendingMu.Lock()
	delete(c.pending, requestID)
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
