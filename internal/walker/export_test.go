package walker

import (
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
	IsMockReceiver        = isMockReceiver
	ReceiverTypesPkg      = receiverTypesPkg
)

// InferFromTypesPkg wraps the unexported (*Walker).inferFromTypesPkg for
// testing. It constructs a Walker with no graph, the given detector, and
// empty module path.
func InferFromTypesPkg(det detector.Detector, pkg *types.Package) trawl.ServiceType {
	w := &Walker{det: det}
	return w.inferFromTypesPkg(pkg)
}

// InterfaceMethodLabel wraps the unexported interfaceMethodLabel for testing.
func InterfaceMethodLabel(cc *ssa.CallCommon) string {
	return interfaceMethodLabel(cc)
}
