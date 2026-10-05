// Package storemock holds only a mock implementation of store.Store.
package storemock

import (
	"context"

	"example.com/lib/mock"
)

// MockStore satisfies store.Store and must be ignored by dependency selection.
type MockStore struct{ mock.Mock }

// Get returns an empty value.
func (MockStore) Get(context.Context, string) (string, error) { return "", nil }

// Fetch returns a zero status.
func (MockStore) Fetch(context.Context, string) (int, error) { return 0, nil }

// Find returns no results.
func (MockStore) Find(context.Context, string) ([]string, error) { return nil, nil }

// Ping always succeeds.
func (MockStore) Ping(context.Context) error { return nil }
