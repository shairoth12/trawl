package multi

import (
	"context"
	"database/sql"
	"net/http"
)

// HandleMulti is the entry point that triggers both HTTP and database service
// calls, exercising detection of two distinct service types in a single handler.
func HandleMulti(ctx context.Context, db *sql.DB) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/api", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var n int
	if err = db.QueryRowContext(ctx, "SELECT 1").Scan(&n); err != nil {
		return err
	}
	return nil
}
