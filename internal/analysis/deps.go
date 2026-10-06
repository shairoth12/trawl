package analysis

import (
	"go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/types/typeutil"

	"github.com/shairoth12/trawl"
)

// maxSelectionRounds is how many interface hops get bodies. Round 1 picks
// packages implementing the interfaces your code calls; round 2 picks packages
// implementing the interfaces *those* packages call; and so on. Three covers
// the common two-hop case (your code → facade → client wrapper) with one round
// to spare; longer chains end in interface_dispatch records instead of a
// traced backend. Each extra round scans only the packages picked in the
// previous one, so raising this is cheap if a real target shows many
// unresolved_invokes with few dependency_packages.
// See docs/adr/0009-selective-dependency-bodies.md.
const maxSelectionRounds = 3

// invokedInterfaces maps a method name to the interfaces that have it, so a
// candidate type is only checked against interfaces it could possibly satisfy.
type invokedInterfaces map[string][]*types.Named

// selectDependencyPkgs decides which packages, beyond initial, get function
// bodies. modulePkgs is every other package of the analyzed module (sorted by
// path). external is packages from other modules that declare a concrete,
// non-mock type implementing an interface called by the module (round 1) or
// by the packages picked in the previous round (rounds 2..maxSelectionRounds),
// ordered by round then path. Standard-library packages (the paths in
// stdlib, see stdlibPkgs) and indicator packages are never picked.
func selectDependencyPkgs(initial []*packages.Package, modulePath string, isIndicator func(string) bool, stdlib map[string]bool) (modulePkgs, external []*packages.Package) {
	isInitial := make(map[*packages.Package]bool, len(initial))
	for _, p := range initial {
		isInitial[p] = true
	}
	inModule := func(p *packages.Package) bool {
		return isInitial[p] || (modulePath != "" && p.Module != nil && p.Module.Path == modulePath)
	}

	var toScan, unpickedDeps []*packages.Package
	packages.Visit(initial, nil, func(p *packages.Package) {
		if p.Types == nil || p.IllTyped || p.TypesInfo == nil || len(p.Syntax) == 0 {
			return
		}
		if inModule(p) {
			toScan = append(toScan, p)
			if !isInitial[p] {
				modulePkgs = append(modulePkgs, p)
			}
			return
		}
		if stdlib[p.PkgPath] || (isIndicator != nil && isIndicator(p.PkgPath)) {
			return
		}
		unpickedDeps = append(unpickedDeps, p)
	})
	sortByPath(modulePkgs)

	var cache typeutil.MethodSetCache
	for round := 0; round < maxSelectionRounds && len(toScan) > 0 && len(unpickedDeps) > 0; round++ {
		ifaces := collectInvokedInterfaces(toScan, stdlib)
		if len(ifaces) == 0 {
			break
		}
		var picked, rest []*packages.Package
		for _, p := range unpickedDeps {
			if declaresImplementor(p.Types.Scope(), ifaces, &cache) {
				picked = append(picked, p)
				continue
			}
			rest = append(rest, p)
		}
		sortByPath(picked)
		external = append(external, picked...)
		toScan, unpickedDeps = picked, rest
	}
	return modulePkgs, external
}

func sortByPath(pkgs []*packages.Package) {
	slices.SortFunc(pkgs, func(a, b *packages.Package) int { return strings.Compare(a.PkgPath, b.PkgPath) })
}

// collectInvokedInterfaces gathers every named, non-stdlib interface that the
// scanned packages call a method on. The interface is taken from the method's
// receiver type, so a call through a struct that embeds the interface counts
// as a call on the interface.
func collectInvokedInterfaces(toScan []*packages.Package, stdlib map[string]bool) invokedInterfaces {
	out := invokedInterfaces{}
	seen := map[*types.Named]bool{}
	for _, p := range toScan {
		for _, sel := range p.TypesInfo.Selections {
			// MethodExpr covers I.M(x); MethodVal covers x.M() and x.M.
			if k := sel.Kind(); k != types.MethodVal && k != types.MethodExpr {
				continue
			}
			fn, ok := sel.Obj().(*types.Func)
			if !ok || fn.Signature().Recv() == nil {
				continue
			}
			named, ok := types.Unalias(fn.Signature().Recv().Type()).(*types.Named)
			if !ok || !types.IsInterface(named) {
				continue
			}
			named = named.Origin()
			obj := named.Obj()
			if obj.Pkg() == nil || stdlib[obj.Pkg().Path()] || seen[named] {
				continue
			}
			iface, ok := named.Underlying().(*types.Interface)
			if !ok || iface.NumMethods() == 0 {
				continue
			}
			seen[named] = true
			for method := range iface.Methods() {
				name := method.Name()
				out[name] = append(out[name], named)
			}
		}
	}
	return out
}

// declaresImplementor reports whether scope declares a concrete named type,
// other than a generated mock (see trawl.IsMock), that implements any of the
// collected interfaces.
func declaresImplementor(scope *types.Scope, ifaces invokedInterfaces, cache *typeutil.MethodSetCache) bool {
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || tn.IsAlias() || trawl.IsMock(tn.Type()) {
			continue
		}
		named, ok := types.Unalias(tn.Type()).(*types.Named)
		if !ok || types.IsInterface(named) {
			continue
		}
		if implementsAny(named, ifaces, cache) {
			return true
		}
	}
	return false
}

// implementsAny reports whether *named implements any collected interface
// that shares a method name with it. For generic types and generic interfaces
// types.Implements gives no defined answer, so those are matched by method
// names only: if the type has every method the interface asks for, it counts.
// Being a little generous here is fine, because this only decides which
// bodies to build; the call-graph algorithm decides the real edges.
func implementsAny(named *types.Named, ifaces invokedInterfaces, cache *typeutil.MethodSetCache) bool {
	generic := named.TypeParams().Len() > 0
	methods := methodNames(named, generic, cache)
	if len(methods) == 0 {
		return false
	}
	ptr := types.NewPointer(named)
	checked := map[*types.Named]bool{}
	for m := range methods {
		for _, iface := range ifaces[m] {
			if checked[iface] {
				continue
			}
			checked[iface] = true
			it, ok := iface.Underlying().(*types.Interface)
			if !ok {
				continue
			}
			if generic || iface.TypeParams().Len() > 0 {
				if hasAllMethods(methods, it) {
					return true
				}
				continue
			}
			if types.Implements(ptr, it) {
				return true
			}
		}
	}
	return false
}

// methodNames returns the names in *named's method set. Generic types use
// their declared methods because method sets of uninstantiated types are not
// well defined.
func methodNames(named *types.Named, generic bool, cache *typeutil.MethodSetCache) map[string]bool {
	out := map[string]bool{}
	if generic {
		for method := range named.Methods() {
			out[method.Name()] = true
		}
		return out
	}
	mset := cache.MethodSet(types.NewPointer(named))
	for method := range mset.Methods() {
		out[method.Obj().Name()] = true
	}
	return out
}

func hasAllMethods(methods map[string]bool, it *types.Interface) bool {
	for method := range it.Methods() {
		if !methods[method.Name()] {
			return false
		}
	}
	return true
}
