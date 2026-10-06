// Package cache provides an in-memory Store implementation with no backend.
package cache

import "context"

// memStore satisfies store.Store structurally without importing it.
type memStore struct{ m map[string]string }

// New returns an in-memory store as an interface value so that call-graph
// builders see *memStore as a runtime type.
func New() interface {
	Get(ctx context.Context, key string) (string, error)
	Fetch(ctx context.Context, url string) (int, error)
	Find(ctx context.Context, query string) ([]string, error)
	Ping(ctx context.Context) error
} {
	return &memStore{m: map[string]string{}}
}

func (c *memStore) Get(_ context.Context, key string) (string, error) { return c.m[key], nil }
func (c *memStore) Fetch(context.Context, string) (int, error)        { return 0, nil }
func (c *memStore) Find(context.Context, string) ([]string, error)    { return nil, nil }
func (c *memStore) Ping(context.Context) error                        { return nil }
