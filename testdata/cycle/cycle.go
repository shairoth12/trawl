package cycle

import (
	"context"
	"net/http"
)

func fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/api", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func processA(ctx context.Context, n int) error {
	if n <= 0 {
		return fetch(ctx)
	}
	return processB(ctx, n-1)
}

func processB(ctx context.Context, n int) error {
	return processA(ctx, n-1)
}

// HandleCycle is the entry point that exercises the visited-set guard against
// infinite DFS. processA and processB are mutually recursive in the static
// call graph; the HTTP call is reachable when n reaches zero.
func HandleCycle(ctx context.Context) error {
	return processA(ctx, 2)
}
