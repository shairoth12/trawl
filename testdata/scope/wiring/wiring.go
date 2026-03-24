// Package wiring provides a concrete Store backed by database/sql.
package wiring

import (
	"context"
	"database/sql"

	"github.com/shairoth12/trawl/testdata/scope/leaf"
)

// SQLStore implements leaf.Store using database/sql.
type SQLStore struct {
	DB *sql.DB
}

// Verify interface compliance at compile time.
var _ leaf.Store = (*SQLStore)(nil)

// Get retrieves a row by key from the database.
func (s *SQLStore) Get(ctx context.Context, key string) (string, error) {
	var val string
	err := s.DB.QueryRowContext(ctx, "SELECT v FROM kv WHERE k = $1", key).Scan(&val)
	return val, err
}

// Wire calls HandleLeaf with a concrete SQLStore, providing the value flow
// that VTA needs to resolve Store→SQLStore dispatch.
func Wire(ctx context.Context, db *sql.DB) {
	leaf.HandleLeaf(ctx, &SQLStore{DB: db})
}
