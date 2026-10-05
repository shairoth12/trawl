// Package svc is the analyzed service module. Its handler depends on
// store.Store from a separate dependency module and never constructs the
// concrete type itself, mimicking reflection-based dependency injection.
package svc

import (
	"context"
	"strings"

	"example.com/lib/cache"
	"example.com/lib/store"
	"example.com/lib/storemock"
	"example.com/lib/util"
)

// Handler holds injected dependencies.
type Handler struct {
	Store store.Store
}

// TestDouble is the mock a test would inject; referenced so that the mock-only
// package is part of the dependency graph.
var TestDouble store.Store = storemock.MockStore{}

// Fallback is a backend-free store; referenced so that the cache package is
// part of the dependency graph and is selected as an implementor.
var Fallback store.Store = cache.New()

// HandleGet reaches the SQL backend through the injected Store.
func (h *Handler) HandleGet(ctx context.Context, key string) (string, error) {
	return h.Store.Get(ctx, key)
}

// HandleFetch reaches the HTTP backend through the injected Store.
func (h *Handler) HandleFetch(ctx context.Context, url string) (int, error) {
	return h.Store.Fetch(ctx, url)
}

// HandleFind reaches the search wrapper through two interface hops.
func (h *Handler) HandleFind(ctx context.Context, q string) ([]string, error) {
	return h.Store.Find(ctx, q)
}

// HandlePing reaches the SQL backend through a cycle inside the dependency.
func (h *Handler) HandlePing(ctx context.Context) error {
	return h.Store.Ping(ctx)
}

// HandleGeneric calls a generic top-level function from the dependency module
// before the interface call. The generic edge must not hide the Store edge.
func (h *Handler) HandleGeneric(ctx context.Context, keys []string) ([]string, error) {
	upper := util.Map(keys, strings.ToUpper)
	if len(upper) == 0 {
		return nil, nil
	}
	v, err := h.Store.Get(ctx, upper[0])
	return []string{v}, err
}

// HandleFindTwice calls Find from two module-side call sites so that one
// dependency-side interface call is reached through two crossings.
func (h *Handler) HandleFindTwice(ctx context.Context, q string) ([]string, error) {
	first, err := h.Store.Find(ctx, q)
	if err != nil {
		return nil, err
	}
	second, err := h.Store.Find(ctx, q+"!")
	return append(first, second...), err
}
