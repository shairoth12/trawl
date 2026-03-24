// Package resolve provides test fixtures for entry-point resolution scenarios.
// It intentionally contains multiple types that share a method name to exercise
// the ambiguity path in the resolve logic.
package resolve

// HandleLogin is a top-level entry-point stub.
func HandleLogin() {}

// Handler dispatches HTTP-like requests.
type Handler struct{}

// ServeHTTP serves a single request (pointer receiver).
func (h *Handler) ServeHTTP() {}

// Handle processes a generic event on the handler.
func (h *Handler) Handle() {}

// Config holds service configuration.
type Config struct{}

// Validate checks that c is well-formed (value receiver).
func (c Config) Validate() {}

// Handle processes a configuration-change event.
// Combined with (*Handler).Handle, this makes "Handle" an ambiguous bare name.
func (c Config) Handle() {}

// MockHandler is a test double for Handler.
// It deliberately shares method names with Handler to verify that bare-name
// resolution ignores types prefixed with "Mock".
type MockHandler struct{}

// ServeHTTP satisfies the same interface as (*Handler).ServeHTTP.
func (m *MockHandler) ServeHTTP() {}
