package main

import (
	"context"
	"fmt"

	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/shairoth12/trawl/internal/analysis"
	"github.com/shairoth12/trawl/internal/detector"
)

func main() {
	root := "/workspaces/trawl"

	// First, use the normal Load to confirm what happens with DeleteSyntheticNodes
	result, err := analysis.Load(context.Background(), root, "./testdata/basic", analysis.AlgoVTA)
	if err != nil {
		fmt.Printf("Load error: %v\n", err)
		return
	}
	fmt.Printf("Module: %q, VTA graph nodes: %d\n", result.Module, len(result.Graph.Nodes))

	fn, err := analysis.Resolve(result, "HandleRequest")
	if err != nil {
		fmt.Printf("Resolve error: %v\n", err)
		return
	}

	node := result.Graph.Nodes[fn]
	fmt.Printf("HandleRequest out-edges (VTA after DeleteSyntheticNodes): %d\n", len(node.Out))

	// Now build CHA graph manually (without DeleteSyntheticNodes) to see what CHA gives us
	fmt.Println("\n--- Building CHA graph WITHOUT DeleteSyntheticNodes ---")

	cfg := &packages.Config{
		Context: context.Background(),
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedSyntax |
			packages.NeedTypesInfo | packages.NeedTypesSizes | packages.NeedModule,
		Dir: root,
	}
	pkgs, err := packages.Load(cfg, "./testdata/basic")
	if err != nil {
		fmt.Printf("packages.Load error: %v\n", err)
		return
	}
	prog, _ := ssautil.Packages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	chaGraph := cha.CallGraph(prog)
	fmt.Printf("CHA graph nodes: %d\n", len(chaGraph.Nodes))

	// Find HandleRequest in CHA graph
	var handleRequestFn *ssa.Function
	for gfn := range chaGraph.Nodes {
		if gfn != nil && gfn.String() == fn.String() {
			handleRequestFn = gfn
			break
		}
	}
	if handleRequestFn == nil {
		fmt.Println("HandleRequest NOT found in CHA graph by string match")
		// Try by package member
	} else {
		chaNode := chaGraph.Nodes[handleRequestFn]
		fmt.Printf("CHA HandleRequest out-edges: %d\n", len(chaNode.Out))
		for _, edge := range chaNode.Out {
			if edge.Callee != nil && edge.Callee.Func != nil {
				if pkg := edge.Callee.Func.Package(); pkg != nil {
					fmt.Printf("  -> %s (pkg=%s)\n", edge.Callee.Func.String(), pkg.Pkg.Path())
				}
			}
		}
	}

	// Build VTA WITHOUT DeleteSyntheticNodes
	fmt.Println("\n--- VTA graph WITHOUT DeleteSyntheticNodes ---")
	vtaGraph := vta.CallGraph(ssautil.AllFunctions(prog), chaGraph)
	fmt.Printf("VTA graph nodes: %d\n", len(vtaGraph.Nodes))
	vtaNode := vtaGraph.Nodes[fn]
	if vtaNode == nil {
		fmt.Println("fn from analysis.Load not in vtaGraph (different *ssa.Function instances)")
		// Find by string match
		for gfn, gnode := range vtaGraph.Nodes {
			if gfn != nil && gfn.String() == fn.String() {
				fmt.Printf("VTA HandleRequest (by string): out-edges=%d\n", len(gnode.Out))
				det := detector.New(nil)
				for _, edge := range gnode.Out {
					if edge.Callee != nil && edge.Callee.Func != nil {
						if pkg := edge.Callee.Func.Package(); pkg != nil {
							svcType, ok := det.Detect(pkg.Pkg.Path())
							fmt.Printf("  -> %s (pkg=%s, detected=%v, svcType=%s)\n",
								edge.Callee.Func.String(), pkg.Pkg.Path(), ok, svcType)
						}
					}
				}
				break
			}
		}
	}
}
