// Package search is a thin client wrapper. Callers depend on the Searcher
// interface; the concrete client is constructed elsewhere.
package search

import (
	"context"
	"net/http"
)

// Searcher runs a remote search.
type Searcher interface {
	Search(ctx context.Context, query string) ([]string, error)
}

type client struct{ http *http.Client }

// NewSearcher returns the production Searcher.
func NewSearcher(h *http.Client) Searcher { return &client{http: h} }

func (c *client) Search(ctx context.Context, query string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://search.example.com/?q="+query, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return []string{}, nil
}
