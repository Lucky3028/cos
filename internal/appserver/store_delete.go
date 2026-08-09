package appserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const deleteVerificationTimeout = 5 * time.Second

var errDeleteOutcomeUnknown = errors.New("thread/delete outcome is unknown")

func (s *AppServerStore) Delete(ctx context.Context, id string) error {
	client, err := s.client(ctx)
	if err != nil {
		return err
	}
	if err := client.request(ctx, "thread/delete", map[string]string{"threadId": id}, nil); err != nil {
		if !isWrittenRPCOutcomeUnknown(err) {
			return err
		}
		verifyCtx, cancel := context.WithTimeout(context.Background(), deleteVerificationTimeout)
		defer cancel()
		verifyClient, reconnectErr := s.reconnect(verifyCtx)
		if reconnectErr != nil {
			return fmt.Errorf("%w: unable to reconnect to verify session %s: %v", errDeleteOutcomeUnknown, id, reconnectErr)
		}
		absent, verifyErr := s.verifyThreadAbsent(verifyCtx, verifyClient, id)
		if verifyErr == nil && absent {
			return nil
		}
		if verifyErr != nil {
			return fmt.Errorf("%w: unable to verify session %s after uncertain result: %v", errDeleteOutcomeUnknown, id, verifyErr)
		}
		return fmt.Errorf("%w: session %s may still exist", errDeleteOutcomeUnknown, id)
	}
	return nil
}

func isWrittenRPCOutcomeUnknown(err error) bool {
	var timeout *rpcRequestTimeout
	var unknown *rpcRequestOutcomeUnknown
	return errors.As(err, &timeout) || errors.As(err, &unknown)
}

func (s *AppServerStore) reconnect(ctx context.Context) (*rpcClient, error) {
	s.mu.Lock()
	process := s.process
	s.process = nil
	s.mu.Unlock()
	if process != nil {
		_ = process.close()
	}
	return s.client(ctx)
}

func (s *AppServerStore) verifyThreadAbsent(ctx context.Context, client *rpcClient, id string) (bool, error) {
	var response struct {
		Thread apiThread `json:"thread"`
	}
	if err := client.request(ctx, "thread/read", map[string]string{"threadId": id}, &response); err != nil {
		if isThreadNotFoundError(err) {
			return true, nil
		}
		return false, err
	}
	if response.Thread.ID != id {
		return false, fmt.Errorf("thread/read returned unexpected session %q", response.Thread.ID)
	}
	return false, nil
}

func isThreadNotFoundError(err error) bool {
	var rpcErr *rpcError
	if errors.As(err, &rpcErr) {
		err = errors.New(rpcErr.Message)
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not found") || strings.Contains(message, "does not exist") || strings.Contains(message, "unknown thread")
}
