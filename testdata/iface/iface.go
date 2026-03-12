package iface

import (
	"context"
	"net/http"
)

// Caller abstracts an external call operation.
type Caller interface {
	Call(ctx context.Context) error
}

// HTTPCaller is the concrete implementation of Caller that makes real HTTP calls.
type HTTPCaller struct{}

func (h *HTTPCaller) Call(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/api", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func newCaller() Caller {
	return &HTTPCaller{}
}

func dispatch(ctx context.Context, c Caller) error {
	return c.Call(ctx)
}

// HandleIface is the entry point that exercises VTA inter-procedural type flow.
// newCaller produces the concrete type; dispatch consumes it via the interface.
func HandleIface(ctx context.Context) error {
	c := newCaller()
	return dispatch(ctx, c)
}
