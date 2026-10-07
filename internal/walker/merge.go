package walker

import (
	"go/token"
	"strings"

	"github.com/shairoth12/trawl"
)

type mergeKey struct {
	svc trawl.ServiceType
	pos token.Pos
}

// mergeByPosition collapses hits with equal (ServiceType, call-site position):
// the higher confidence wins; on a tie the abstract interface label is kept;
// otherwise the first hit. Hits without a position are kept as they are.
// Order of first occurrence is preserved. The result is never nil.
func mergeByPosition(hits []hit) []trawl.ExternalCall {
	out := make([]trawl.ExternalCall, 0, len(hits))
	slotOf := make(map[mergeKey]int, len(hits))
	for _, h := range hits {
		if !h.pos.IsValid() {
			out = append(out, h.call)
			continue
		}
		key := mergeKey{h.call.ServiceType, h.pos}
		slot, seen := slotOf[key]
		if !seen {
			slotOf[key] = len(out) // where h.call is about to land
			out = append(out, h.call)
			continue
		}
		if stronger(h.call, out[slot]) {
			out[slot] = h.call
		}
	}
	return out
}

func confidenceRank(confidence trawl.Confidence) int {
	switch confidence {
	case trawl.ConfidenceHigh:
		return 3
	case trawl.ConfidenceMedium:
		return 2
	case trawl.ConfidenceLow:
		return 1
	}
	return 0
}

// isInterfaceLabel reports whether function is an abstract "Iface.Method"
// label rather than a concrete "(recv).Method" SSA name.
func isInterfaceLabel(function string) bool { return !strings.HasPrefix(function, "(") }

func stronger(candidate, current trawl.ExternalCall) bool {
	candidateRank, currentRank := confidenceRank(candidate.Confidence), confidenceRank(current.Confidence)
	if candidateRank != currentRank {
		return candidateRank > currentRank
	}
	return isInterfaceLabel(candidate.Function) && !isInterfaceLabel(current.Function)
}
