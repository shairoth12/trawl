package detector

import (
	"strings"

	"github.com/shairoth12/trawl"
)

// Detector classifies a Go import path as a known external service type.
// Detect is safe for concurrent use provided no indicator slice passed to New
// is mutated after New returns.
type Detector interface {
	Detect(importPath string) (serviceType trawl.ServiceType, ok bool)
}

type detector struct {
	indicators []trawl.Indicator // user first, then builtins; immutable after New
}

// New returns a Detector. userIndicators are checked before built-in indicators;
// first prefix match wins. New copies all slices so callers may mutate their
// input after New returns.
func New(userIndicators []trawl.Indicator) Detector {
	merged := make([]trawl.Indicator, 0, len(userIndicators)+len(builtinIndicators))
	merged = append(merged, userIndicators...)    // copy of user slice
	merged = append(merged, builtinIndicators...) // snapshot of builtins
	return &detector{indicators: merged}
}

// Detect returns the service type and true for the first indicator whose Package
// is a prefix of importPath. Returns ("", false) if no indicator matches.
func (d *detector) Detect(importPath string) (trawl.ServiceType, bool) {
	for _, ind := range d.indicators {
		if strings.HasPrefix(importPath, ind.Package) {
			return ind.ServiceType, true
		}
	}
	return "", false
}
