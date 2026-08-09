package appserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

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

		var message rpcMessage
		if err := json.Unmarshal(line, &message); err != nil {
			c.finish(fmt.Errorf("%w: invalid JSONL message: %w", errRPCClosed, err))
			return
		}
		if messageIsNotification(message) {
			continue
		}

		requestID, err := responseID(message)
		if err != nil {
			continue
		}
		c.deliverResponse(requestID, rpcResponse{
			ID: message.ID, Result: message.Result, Error: message.Error,
		})
	}
}

func messageIsNotification(message rpcMessage) bool {
	// Notifications and server requests can be interleaved with ordinary
	// responses. The client intentionally does not expose them to callers.
	return len(message.ID) == 0 || message.Method != ""
}

func responseID(message rpcMessage) (int64, error) {
	var id int64
	if err := json.Unmarshal(message.ID, &id); err != nil {
		return 0, err
	}
	return id, nil
}

func (c *rpcClient) deliverResponse(requestID int64, response rpcResponse) {
	// Keep lookup and send under the same mutex as finish's close. This avoids
	// sending to a response channel after the connection has been finalized.
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	pending := c.pending[requestID]
	if pending == nil {
		return
	}
	select {
	case pending.responseCh <- response:
	default:
		// A duplicate response must not block the reader.
	}
}

// readJSONLMessage bounds a line before unmarshalling it. ReadBytes and
// Scanner can allocate an unbounded token before the limit is checked.
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
