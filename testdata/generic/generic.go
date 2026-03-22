// Package generic provides a test fixture for generic interface dispatch
// through CHA. Both RealCache and MockCache are generic instantiations
// whose fn.Package() returns nil in SSA.
package generic

import (
	"context"
	"database/sql"
)

// Cache is a generic interface with Get and Set methods.
type Cache[T any] interface {
	Get(ctx context.Context, key string) (T, error)
	Set(ctx context.Context, key string, val T) error
}

// RealCache uses database/sql, making it detectable as POSTGRES.
type RealCache[T any] struct{ db *sql.DB }

func (r *RealCache[T]) Get(ctx context.Context, key string) (T, error) {
	row := r.db.QueryRowContext(ctx, "SELECT val FROM cache WHERE k = $1", key)
	_ = row
	var zero T
	return zero, nil
}

func (r *RealCache[T]) Set(ctx context.Context, key string, val T) error {
	_, err := r.db.ExecContext(ctx, "INSERT INTO cache (k,v) VALUES ($1,$2)", key, val)
	return err
}

// MockCache is a no-op mock that satisfies Cache[T].
type MockCache[T any] struct{}

func (m *MockCache[T]) Get(ctx context.Context, key string) (T, error) {
	var zero T
	return zero, nil
}

func (m *MockCache[T]) Set(ctx context.Context, key string, val T) error {
	return nil
}

// HandleGeneric dispatches through the Cache[string] interface.
// Under CHA, both RealCache and MockCache are resolved as callees.
func HandleGeneric(ctx context.Context, c Cache[string]) {
	_, _ = c.Get(ctx, "key")
	_ = c.Set(ctx, "key", "value")
}

// Wire instantiates the concrete generic types so that CHA can see
// RealCache[string] and MockCache[string] in the type universe.
func Wire(ctx context.Context, db *sql.DB) {
	HandleGeneric(ctx, &RealCache[string]{db: db})
	HandleGeneric(ctx, &MockCache[string]{})
}
