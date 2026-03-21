// Package erriface tests that CHA dispatch through error.Error() does not
// produce false positives when an error type lives in a service-indicator
// package. The ubiquitous-interface filter must suppress these edges.
package erriface

import (
	"context"
	"net/http"
)

func doWork(ctx context.Context) error {
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

// HandleErr makes a legitimate HTTP call via doWork. It also calls err.Error(),
// which under CHA dispatches to every type implementing error — including
// SvcError in the svcpkg subpackage. The ubiquitous-interface filter must
// suppress the svcpkg edge while preserving the real HTTP detection.
func HandleErr(ctx context.Context) string {
	err := doWork(ctx)
	if err != nil {
		return err.Error()
	}
	return "ok"
}
