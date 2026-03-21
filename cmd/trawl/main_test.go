package main

import (
	"testing"

	"github.com/shairoth12/trawl"
)

func TestDeduplicateCalls(t *testing.T) {
	t.Parallel()

	calls := []trawl.ExternalCall{
		{
			ServiceType: "HTTP",
			ImportPath:  "net/http",
			Function:    "Get",
			CallChain:   []string{"A", "B", "C"},
			ResolvedVia: trawl.ResolvedViaDirect,
			Confidence:  trawl.ConfidenceHigh,
		},
		{
			ServiceType: "HTTP",
			ImportPath:  "net/http",
			Function:    "Get",
			CallChain:   []string{"A", "D"},
			ResolvedVia: trawl.ResolvedViaDirect,
			Confidence:  trawl.ConfidenceHigh,
		},
		{
			ServiceType: "REDIS",
			ImportPath:  "github.com/redis/go-redis",
			Function:    "Set",
			CallChain:   []string{"A", "E"},
			ResolvedVia: trawl.ResolvedViaDirect,
			Confidence:  trawl.ConfidenceHigh,
		},
	}

	got := deduplicateCalls(calls)

	if len(got) != 2 {
		t.Fatalf("deduplicateCalls() len = %d, want 2", len(got))
	}

	// HTTP entry should keep the shorter call chain ["A", "D"].
	if got[0].ServiceType != "HTTP" {
		t.Errorf("got[0].ServiceType = %q, want %q", got[0].ServiceType, "HTTP")
	}
	if len(got[0].CallChain) != 2 {
		t.Errorf("got[0].CallChain len = %d, want 2 (shortest chain)", len(got[0].CallChain))
	}

	// REDIS entry unchanged.
	if got[1].ServiceType != "REDIS" {
		t.Errorf("got[1].ServiceType = %q, want %q", got[1].ServiceType, "REDIS")
	}
}

func TestDeduplicateCalls_NoDuplicates(t *testing.T) {
	t.Parallel()

	calls := []trawl.ExternalCall{
		{ServiceType: "HTTP", ImportPath: "net/http", Function: "Get", CallChain: []string{"A"}},
		{ServiceType: "REDIS", ImportPath: "go-redis", Function: "Set", CallChain: []string{"B"}},
	}

	got := deduplicateCalls(calls)

	if len(got) != 2 {
		t.Fatalf("deduplicateCalls() len = %d, want 2", len(got))
	}
}

func TestDeduplicateCalls_Empty(t *testing.T) {
	t.Parallel()

	got := deduplicateCalls(nil)

	if got != nil {
		t.Errorf("deduplicateCalls(nil) = %v, want nil", got)
	}
}
