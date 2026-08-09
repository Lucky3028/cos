package appserver

import (
	"context"
	"fmt"
	"strings"
)

func (s *AppServerStore) List(ctx context.Context, request ThreadListRequest) (ThreadPage, error) {
	if request.Scope != CurrentDirectory && request.Scope != AllThreads {
		return ThreadPage{}, fmt.Errorf("unknown list scope %d", request.Scope)
	}
	client, err := s.client(ctx)
	if err != nil {
		return ThreadPage{}, err
	}
	params := map[string]any{
		"archived":      false,
		"sourceKinds":   []string{"cli", "vscode", "appServer"},
		"sortKey":       "updated_at",
		"sortDirection": "desc",
		"limit":         threadPageLimit(request.Limit),
	}
	if request.Cursor != "" {
		params["cursor"] = request.Cursor
	}
	switch request.Scope {
	case CurrentDirectory:
		params["cwd"] = request.CWD
	case AllThreads:
	}

	if request.Query == "" {
		return listThreadPage(ctx, client, params)
	}
	return listSearchPage(ctx, client, params, request)
}

func (s *AppServerStore) ListDescendants(ctx context.Context, id string) ([]Thread, error) {
	client, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	var descendants []Thread
	for _, archived := range []bool{false, true} {
		var cursor string
		seenCursors := make(map[string]struct{})
		for {
			params := map[string]any{
				"ancestorThreadId": id,
				"archived":         archived,
				"sortKey":          "updated_at",
				"sortDirection":    "desc",
				"limit":            1,
			}
			if cursor != "" {
				params["cursor"] = cursor
			}
			page, err := listThreadPage(ctx, client, params)
			if err != nil {
				return nil, fmt.Errorf("list descendants (archived=%t): %w", archived, err)
			}
			descendants = append(descendants, page.Threads...)
			if page.NextCursor == "" {
				break
			}
			if _, ok := seenCursors[page.NextCursor]; ok {
				return nil, fmt.Errorf("list descendants (archived=%t): cursor cycle detected at %q", archived, page.NextCursor)
			}
			seenCursors[page.NextCursor] = struct{}{}
			cursor = page.NextCursor
		}
	}
	return descendants, nil
}

func threadPageLimit(limit int) int {
	if limit <= 0 || limit > defaultThreadPageSize {
		return defaultThreadPageSize
	}
	return limit
}

func listThreadPage(ctx context.Context, client *rpcClient, params map[string]any) (ThreadPage, error) {
	var response apiThreadListResponse
	if err := client.request(ctx, "thread/list", params, &response); err != nil {
		return ThreadPage{}, err
	}
	page := ThreadPage{Threads: make([]Thread, 0, len(response.Data.Items))}
	for _, item := range response.Data.Items {
		page.Threads = append(page.Threads, item.toThread())
	}
	if nextCursor := response.Data.NextCursor; nextCursor != nil {
		page.NextCursor = *nextCursor
	} else if response.NextCursor != nil {
		page.NextCursor = *response.NextCursor
	}
	return page, nil
}

func listSearchPage(ctx context.Context, client *rpcClient, params map[string]any, request ThreadListRequest) (ThreadPage, error) {
	result := ThreadPage{Threads: make([]Thread, 0, threadPageLimit(request.Limit))}
	seen := make(map[string]struct{})
	cursor := request.Cursor
	if cursor != "" {
		seen[cursor] = struct{}{}
	}
	searchedPages := request.SearchPages
	if searchedPages < 0 || searchedPages > maxSearchPages {
		return ThreadPage{}, fmt.Errorf("invalid search page count %d", searchedPages)
	}
	for searchedPages < maxSearchPages {
		if cursor == "" {
			delete(params, "cursor")
		} else {
			params["cursor"] = cursor
		}
		page, err := listThreadPage(ctx, client, params)
		if err != nil {
			return ThreadPage{}, err
		}
		searchedPages++
		result.ScannedPages++
		matches := make([]Thread, 0, len(page.Threads))
		for _, thread := range page.Threads {
			if matchesThreadQuery(thread, request.Query) {
				matches = append(matches, thread)
			}
		}
		// Keep an entire matching app-server page. Returning only the first
		// matches would make the rest of this page unreachable on the next UI
		// request because the opaque cursor advances past it.
		if len(matches) > 0 {
			result.Threads = matches
			result.NextCursor = page.NextCursor
			result.Incomplete = searchedPages >= maxSearchPages && page.NextCursor != ""
			return result, nil
		}
		if page.NextCursor == "" {
			return result, nil
		}
		if _, ok := seen[page.NextCursor]; ok {
			return ThreadPage{}, fmt.Errorf("thread/list cursor cycle detected at %q", page.NextCursor)
		}
		seen[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	result.Incomplete = true
	return result, nil
}

func matchesThreadQuery(thread Thread, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	return strings.Contains(strings.ToLower(thread.Title), query) ||
		strings.Contains(strings.ToLower(thread.Preview), query) ||
		strings.Contains(strings.ToLower(thread.CWD), query)
}
