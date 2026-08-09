package appserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	maxConversationTurns = 100
	maxConversationBytes = 1 << 20
)

func (s *AppServerStore) Read(ctx context.Context, id string) (Conversation, error) {
	client, err := s.client(ctx)
	if err != nil {
		return Conversation{}, err
	}
	var response struct {
		Thread apiThread `json:"thread"`
	}
	truncated := false
	if err := client.request(ctx, "thread/read", map[string]any{
		"threadId": id, "includeTurns": true,
	}, &response); err != nil {
		initialErr := err
		if !isIncludeTurnsUnsupportedError(err) {
			return Conversation{}, err
		}
		// Some app-server versions reject includeTurns for paginated threads.
		// Retry with a metadata-only read and hydrate the turns through the
		// pagination API, while retaining compatibility with older servers that
		// only support the original thread/read request.
		var metadata struct {
			Thread apiThread `json:"thread"`
		}
		if metadataErr := client.request(ctx, "thread/read", map[string]any{
			"threadId": id,
		}, &metadata); metadataErr != nil {
			return Conversation{}, errors.Join(initialErr, fmt.Errorf("metadata read fallback: %w", metadataErr))
		}
		turns, turnsTruncated, turnsErr := listThreadTurns(ctx, client, id)
		if turnsErr != nil {
			return Conversation{}, errors.Join(initialErr, fmt.Errorf("paginated turns fallback: %w", turnsErr))
		}
		metadata.Thread.Turns = turns
		truncated = turnsTruncated
		response.Thread = metadata.Thread
	}
	limitedTurns, turnsTruncated := limitTurnsChronological(response.Thread.Turns)
	response.Thread.Turns = limitedTurns
	truncated = truncated || turnsTruncated
	thread := response.Thread.toThread()
	items, itemsTruncated := conversationItemsLimited(response.Thread.Turns)
	return Conversation{Thread: thread, Items: items, Truncated: truncated || itemsTruncated}, nil
}

func isIncludeTurnsUnsupportedError(err error) bool {
	var rpcErr *rpcError
	if !errors.As(err, &rpcErr) {
		return false
	}
	message := strings.ToLower(rpcErr.Message)
	return strings.Contains(message, "includeturns") &&
		(strings.Contains(message, "unsupported") || strings.Contains(message, "not supported"))
}

func listThreadTurns(ctx context.Context, client *rpcClient, id string) ([]apiTurn, bool, error) {
	var newestFirst []apiTurn
	var cursor string
	seenCursors := make(map[string]struct{})
	usedBytes := 0
	for {
		params := map[string]any{
			"threadId":      id,
			"limit":         100,
			"sortDirection": "desc",
			"itemsView":     "full",
		}
		if cursor != "" {
			params["cursor"] = cursor
		}

		var page apiTurnsListResponse
		if err := client.request(ctx, "thread/turns/list", params, &page); err != nil {
			return nil, false, err
		}
		for _, turn := range page.Data {
			if len(newestFirst) >= maxConversationTurns {
				return reverseTurns(newestFirst), true, nil
			}
			turnBytes := turnDisplayBytes(turn)
			if len(newestFirst) > 0 && usedBytes+turnBytes > maxConversationBytes {
				return reverseTurns(newestFirst), true, nil
			}
			newestFirst = append(newestFirst, turn)
			usedBytes += turnBytes
			if turnBytes > maxConversationBytes {
				return reverseTurns(newestFirst), true, nil
			}
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return reverseTurns(newestFirst), false, nil
		}
		if len(newestFirst) >= maxConversationTurns || usedBytes >= maxConversationBytes {
			return reverseTurns(newestFirst), true, nil
		}
		nextCursor := *page.NextCursor
		if nextCursor == cursor {
			return nil, false, fmt.Errorf("thread/turns/list cursor cycle detected at %q", nextCursor)
		}
		if _, seen := seenCursors[nextCursor]; seen {
			return nil, false, fmt.Errorf("thread/turns/list cursor cycle detected at %q", nextCursor)
		}
		seenCursors[nextCursor] = struct{}{}
		cursor = nextCursor
	}
}
