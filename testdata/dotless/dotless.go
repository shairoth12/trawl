// Package dotless is a module whose path has no dot, like a local service
// declared as `module myservice`. Its interface must not be mistaken for a
// standard-library interface.
package dotless

import (
	"context"

	"dotless/impl"
)

// Store is implemented by impl.SQL.
type Store interface {
	Get(ctx context.Context, key string) (string, error)
}

// Handler holds an injected Store; no code assigns it, as with reflection DI.
type Handler struct {
	S Store
}

// Real is referenced so impl is part of the program, without flowing into
// Handler.S.
var Real Store = impl.SQL{}

// Handle reaches the SQL backend through the injected Store.
func (h *Handler) Handle(ctx context.Context, key string) (string, error) {
	return h.S.Get(ctx, key)
}
