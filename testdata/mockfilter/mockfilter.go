// Package mockfilter tests that CHA dispatch through mock types is suppressed
// by the mock-type filter. MockStore (which makes HTTP calls) should be
// filtered, while RealStore (which uses database/sql) should be detected.
package mockfilter

import (
	"context"
	"database/sql"
	"net/http"
)

// Store is an interface with both a mock and a real implementation.
type Store interface {
	Get(ctx context.Context, key string) (string, error)
}

// RealStore implements Store using database/sql.
type RealStore struct{ DB *sql.DB }

func (s *RealStore) Get(ctx context.Context, key string) (string, error) {
	var val string
	err := s.DB.QueryRowContext(ctx, "SELECT v FROM kv WHERE k = $1", key).Scan(&val)
	return val, err
}

// MockStore implements Store but makes HTTP calls internally. Under CHA, the
// mock-type filter must prevent the walker from entering MockStore.Get and
// detecting a spurious HTTP call.
type MockStore struct{}

func (m *MockStore) Get(ctx context.Context, _ string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://mock.example.com", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return "mock", nil
}

// HandleMock calls Store.Get through the interface. CHA resolves to both
// MockStore.Get and RealStore.Get. Only RealStore should produce a detection.
func HandleMock(ctx context.Context, s Store) (string, error) {
	return s.Get(ctx, "user:1")
}
