// Package mock stands in for github.com/stretchr/testify/mock in fixtures.
// detector.IsMock matches the package name and type name, not the import path.
package mock

// Mock marks a struct that embeds it as a generated mock.
type Mock struct{}
