// Package gomock stands in for go.uber.org/mock/gomock in fixtures.
// detector.IsMock matches the package name and type name, not the import path.
package gomock

// Controller marks a struct with a *Controller field as a generated mock.
type Controller struct{}
