// Package store is a dependency-module facade over several backends. The
// interface, its concrete implementation, and a generated-style mock live in
// the same package, matching common infrastructure-library layouts.
package store

import (
	"context"
	"database/sql"
	"net/http"

	"example.com/lib/mock"
	"example.com/lib/search"
)

// Store abstracts a key/value backend with a remote fetch, a search path, and
// a health check.
type Store interface {
	Get(ctx context.Context, key string) (string, error)
	Fetch(ctx context.Context, url string) (int, error)
	Find(ctx context.Context, query string) ([]string, error)
	Ping(ctx context.Context) error
}

// sqlStore is the production implementation. Get uses database/sql, Fetch
// uses net/http, Find delegates to a search client through an interface, and
// Ping runs through a mutually recursive pair of helpers.
type sqlStore struct {
	db       *sql.DB
	searcher search.Searcher
	retry    bool
}

// NewStore returns the production Store. Returning the interface materializes
// *sqlStore as a runtime type, which is how call-graph builders learn that it
// implements Store even when no analyzed code calls this constructor.
func NewStore(db *sql.DB, s search.Searcher) Store {
	return &sqlStore{db: db, searcher: s}
}

func (s *sqlStore) Get(ctx context.Context, key string) (string, error) {
	var val string
	err := s.db.QueryRowContext(ctx, "SELECT v FROM kv WHERE k = $1", key).Scan(&val)
	return val, err
}

func (s *sqlStore) Fetch(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func (s *sqlStore) Find(ctx context.Context, query string) ([]string, error) {
	return s.searcher.Search(ctx, query)
}

func (s *sqlStore) Ping(ctx context.Context) error { return s.pingA(ctx) }

func (s *sqlStore) pingA(ctx context.Context) error { return s.pingB(ctx) }

func (s *sqlStore) pingB(ctx context.Context) error {
	if s.retry {
		return s.pingA(ctx)
	}
	return s.db.PingContext(ctx)
}

// MockStore is a generated-style mock that satisfies Store without touching
// any backend.
type MockStore struct{ mock.Mock }

// Get returns an empty value.
func (m *MockStore) Get(context.Context, string) (string, error) { return "", nil }

// Fetch returns a zero status.
func (m *MockStore) Fetch(context.Context, string) (int, error) { return 0, nil }

// Find returns no results.
func (m *MockStore) Find(context.Context, string) ([]string, error) { return nil, nil }

// Ping always succeeds.
func (m *MockStore) Ping(context.Context) error { return nil }
