package walker

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/shairoth12/trawl"
	"github.com/shairoth12/trawl/internal/detector"
)

// Exported aliases for testing unexported functions from the walker_test
// external package. Follows the net/http/export_test.go pattern.
var (
	IsUbiquitousInterface = isUbiquitousInterface
	IsMockMethod          = isMockMethod
	CalleePkg             = calleePkg
	MergeByPosition       = mergeByPosition
)

// Hit exposes the internal hit type for merge tests.
type Hit = hit

// NewHit builds a hit at pos.
func NewHit(call trawl.ExternalCall, pos token.Pos) Hit {
	return hit{call: call, pos: pos}
}

// InferFromTypesPkg wraps the unexported (*Walker).inferFromTypesPkg for
// testing. It constructs a Walker with no graph and the given detector.
func InferFromTypesPkg(det detector.Detector, pkg *types.Package) trawl.ServiceType {
	return New(nil, det, Options{}).inferFromTypesPkg(pkg)
}

// InterfaceMethodLabel wraps the unexported interfaceMethodLabel for testing.
func InterfaceMethodLabel(cc *ssa.CallCommon) string {
	return interfaceMethodLabel(cc)
}
