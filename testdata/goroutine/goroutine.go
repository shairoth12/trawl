package goroutine

import (
	"context"
	"net/http"
)

// HandleAsync spawns a goroutine whose closure makes an external HTTP call.
// The call-graph walker follows the closure edge to detect the service call.
func HandleAsync(ctx context.Context) {
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/api", nil)
		if err != nil {
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
	}()
}
