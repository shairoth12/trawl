// Package leaf provides a fixture for scope-based interface resolution tests.
package leaf

import "context"

// Store abstracts a data backend. The leaf package defines the interface but
// does not import any concrete storage package.
type Store interface {
	Get(ctx context.Context, key string) (string, error)
}

// HandleLeaf is the entry point. It calls through the Store interface; without
// scope, VTA has zero concrete candidates and reports no external calls.
func HandleLeaf(ctx context.Context, s Store) {
	_, _ = s.Get(ctx, "user:1")
}
