// Package util provides a generic helper used to exercise generic top-level
// instantiations in the call graph.
package util

// Map applies f to every element of xs.
func Map[T, U any](xs []T, f func(T) U) []U {
	out := make([]U, 0, len(xs))
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}
