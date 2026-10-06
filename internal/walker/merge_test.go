package walker_test

import (
	"go/token"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shairoth12/trawl"
	"github.com/shairoth12/trawl/internal/walker"
)

func call(svc trawl.ServiceType, fn, via, conf string) trawl.ExternalCall {
	return trawl.ExternalCall{ServiceType: svc, Function: fn, ResolvedVia: via, Confidence: conf, CallChain: []string{"entry", fn}}
}

func TestMergeByPosition(t *testing.T) {
	t.Parallel()
	const (
		concrete = "(*example.com/lib.T).M"
		label    = "example.com/lib.I.M"
	)
	p1, p2 := token.Pos(10), token.Pos(20)
	tests := []struct {
		name string
		hits []walker.Hit
		want []trawl.ExternalCall
	}{
		{
			name: "same_type_same_pos_higher_confidence_wins",
			hits: []walker.Hit{
				walker.NewHit(call("REDIS", concrete, trawl.ResolvedViaCrossModuleInference, trawl.ConfidenceLow), p1),
				walker.NewHit(call("REDIS", label, trawl.ResolvedViaMockInference, trawl.ConfidenceMedium), p1),
			},
			want: []trawl.ExternalCall{call("REDIS", label, trawl.ResolvedViaMockInference, trawl.ConfidenceMedium)},
		},
		{
			name: "tie_keeps_interface_label",
			hits: []walker.Hit{
				walker.NewHit(call("REDIS", concrete, trawl.ResolvedViaDirect, trawl.ConfidenceHigh), p1),
				walker.NewHit(call("REDIS", label, trawl.ResolvedViaDirect, trawl.ConfidenceHigh), p1),
			},
			want: []trawl.ExternalCall{call("REDIS", label, trawl.ResolvedViaDirect, trawl.ConfidenceHigh)},
		},
		{
			name: "tie_same_kind_keeps_first",
			hits: []walker.Hit{
				walker.NewHit(call("POSTGRES", "(*database/sql.DB).QueryRowContext", trawl.ResolvedViaDirect, trawl.ConfidenceHigh), p1),
				walker.NewHit(call("POSTGRES", "(*database/sql.Row).Scan", trawl.ResolvedViaDirect, trawl.ConfidenceHigh), p1),
			},
			want: []trawl.ExternalCall{call("POSTGRES", "(*database/sql.DB).QueryRowContext", trawl.ResolvedViaDirect, trawl.ConfidenceHigh)},
		},
		{
			name: "different_types_same_pos_not_merged",
			hits: []walker.Hit{
				walker.NewHit(call("HTTP", concrete, trawl.ResolvedViaDirect, trawl.ConfidenceHigh), p1),
				walker.NewHit(call("REDIS", concrete, trawl.ResolvedViaDirect, trawl.ConfidenceHigh), p1),
			},
			want: []trawl.ExternalCall{
				call("HTTP", concrete, trawl.ResolvedViaDirect, trawl.ConfidenceHigh),
				call("REDIS", concrete, trawl.ResolvedViaDirect, trawl.ConfidenceHigh),
			},
		},
		{
			name: "different_positions_not_merged",
			hits: []walker.Hit{
				walker.NewHit(call("REDIS", concrete, trawl.ResolvedViaDirect, trawl.ConfidenceHigh), p1),
				walker.NewHit(call("REDIS", concrete, trawl.ResolvedViaDirect, trawl.ConfidenceHigh), p2),
			},
			want: []trawl.ExternalCall{
				call("REDIS", concrete, trawl.ResolvedViaDirect, trawl.ConfidenceHigh),
				call("REDIS", concrete, trawl.ResolvedViaDirect, trawl.ConfidenceHigh),
			},
		},
		{
			name: "nopos_never_merged",
			hits: []walker.Hit{
				walker.NewHit(call("REDIS", concrete, trawl.ResolvedViaDirect, trawl.ConfidenceHigh), token.NoPos),
				walker.NewHit(call("REDIS", concrete, trawl.ResolvedViaDirect, trawl.ConfidenceHigh), token.NoPos),
			},
			want: []trawl.ExternalCall{
				call("REDIS", concrete, trawl.ResolvedViaDirect, trawl.ConfidenceHigh),
				call("REDIS", concrete, trawl.ResolvedViaDirect, trawl.ConfidenceHigh),
			},
		},
		{name: "empty", hits: nil, want: []trawl.ExternalCall{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := walker.MergeByPosition(tt.hits)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("MergeByPosition mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
