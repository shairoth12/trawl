package walker

import (
	"strings"

	"github.com/shairoth12/trawl"
)

// shortenCalls fills ShortFunction and ShortCallChain from Function and
// CallChain.
func shortenCalls(calls []trawl.ExternalCall) {
	for i := range calls {
		calls[i].ShortFunction = shortenName(calls[i].Function)
		calls[i].ShortCallChain = make([]string, len(calls[i].CallChain))
		for j, name := range calls[i].CallChain {
			calls[i].ShortCallChain[j] = shortenName(name)
		}
	}
}

// shortenName strips module path prefixes and generic type parameters from
// a fully-qualified Go SSA function name, producing a concise form suitable
// for LLM consumption.
//
// Examples:
//
//	"github.com/foo/bar.Get"                          → "Get"
//	"(*github.com/foo/bar.Client).Do"                 → "(*Client).Do"
//	"github.com/foo/bar.Cache[github.com/a/b.T].Set"  → "Cache.Set"
func shortenName(s string) string {
	s = stripGenericParams(s)

	lastSlash := strings.LastIndex(s, "/")
	if lastSlash == -1 {
		return s
	}

	dotAfterSlash := strings.IndexByte(s[lastSlash:], '.')
	if dotAfterSlash == -1 {
		return s
	}

	// Preserve prefix characters before the package path, e.g. "(*".
	pathStart := 0
	for pathStart < lastSlash && (s[pathStart] == '(' || s[pathStart] == '*') {
		pathStart++
	}

	prefix := s[:pathStart]
	suffix := s[lastSlash+dotAfterSlash+1:]
	return prefix + suffix
}

// stripGenericParams removes Go generic type parameter blocks ([...]) from s,
// handling nested brackets. Iterative to handle strings with multiple
// top-level bracket pairs without additional stack frames.
func stripGenericParams(s string) string {
	for {
		start := strings.IndexByte(s, '[')
		if start == -1 {
			return s
		}
		depth := 0
		for i := start; i < len(s); i++ {
			switch s[i] {
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					s = s[:start] + s[i+1:]
					goto next
				}
			}
		}
		return s // unmatched '[', bail
	next:
	}
}
