// Package mockfilter tests that CHA dispatch through mock types is suppressed
// by the mock-type filter. MockStore (which makes HTTP calls) should be
// filtered, while RealStore (which uses database/sql) should be detected.
package mockfilter

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/shairoth12/trawl/testdata/gomock"
	"github.com/shairoth12/trawl/testdata/mock"
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
type MockStore struct{ mock.Mock }

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

// Mockstore is the shape mockgen generates for an unexported interface: no
// uppercase letter after "Mock", but a *gomock.Controller field.
type Mockstore struct{ ctrl *gomock.Controller }

func (m *Mockstore) Get(context.Context, string) (string, error) { return "", nil }

// MockingbirdClient is a real type whose name happens to start with "Mock".
type MockingbirdClient struct{}

func (c *MockingbirdClient) Sing(ctx context.Context) error {
	_, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://birds.example.com", nil)
	return err
}

// HandleMock calls Store.Get through the interface. CHA resolves to both
// MockStore.Get and RealStore.Get. Only RealStore should produce a detection.
func HandleMock(ctx context.Context, s Store) (string, error) {
	return s.Get(ctx, "user:1")
}
