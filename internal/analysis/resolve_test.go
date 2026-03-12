package analysis_test

import (
	"strings"
	"testing"

	"github.com/shairoth12/trawl/internal/analysis"
)

func TestResolve(t *testing.T) {
	t.Parallel()

	root := moduleRoot(t)
	result, err := analysis.Load(t.Context(), root, "./testdata/resolve", analysis.AlgoVTA)
	if err != nil {
		t.Fatalf("Load(./testdata/resolve): %v", err)
	}

	tests := []struct {
		name            string
		entry           string
		wantFnName      string // expected fn.Name() on success
		wantErr         bool
		wantErrContains string // non-empty: err.Error() must contain this substring
	}{
		{
			name:       "top_level_function",
			entry:      "HandleLogin",
			wantFnName: "HandleLogin",
		},
		{
			name:       "pointer_receiver_method",
			entry:      "Handler.ServeHTTP",
			wantFnName: "ServeHTTP",
		},
		{
			name:       "value_receiver_method",
			entry:      "Config.Validate",
			wantFnName: "Validate",
		},
		{
			name:       "bare_method_unique",
			entry:      "ServeHTTP",
			wantFnName: "ServeHTTP",
		},
		{
			name:            "bare_method_ambiguous",
			entry:           "Handle",
			wantErr:         true,
			wantErrContains: "(*Handler).Handle",
		},
		{
			name:    "not_found",
			entry:   "DoesNotExist",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fn, err := analysis.Resolve(result, tc.entry)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%q) = non-nil fn, want error", tc.entry)
				}
				if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Errorf("Resolve(%q) error = %q, want it to contain %q", tc.entry, err.Error(), tc.wantErrContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("Resolve(%q) unexpected error: %v", tc.entry, err)
			}
			if fn == nil {
				t.Fatalf("Resolve(%q) = nil, want non-nil *ssa.Function", tc.entry)
			}
			if got := fn.Name(); got != tc.wantFnName {
				t.Errorf("Resolve(%q).Name() = %q, want %q", tc.entry, got, tc.wantFnName)
			}
		})
	}
}
