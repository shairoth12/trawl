// Package svcpkg provides a type that implements the error interface. When a
// custom indicator matches this package, CHA dispatch of error.Error() would
// reach SvcError.Error() — a false positive that the ubiquitous-interface
// filter must suppress.
package svcpkg

// SvcError is an error type whose package path will be matched by a custom
// test indicator. CHA should NOT follow error.Error() dispatch here.
type SvcError struct{ Code int }

func (e *SvcError) Error() string { return "svc error" }
