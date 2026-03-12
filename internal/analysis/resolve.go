package analysis

import (
	"fmt"
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// Resolve returns the *ssa.Function for the named entry point within result.
//
// Three entry-point formats are supported:
//
//   - "FunctionName" — top-level function; if none is found the name is tried
//     as a bare method name across all types in the package.
//   - "Type.Method" — method on a named type; searches both pointer and value
//     receivers via the type's declared method list.
//   - "MethodName" (no dot, not a top-level function) — scans all package
//     members; returns an error if the name matches more than one method.
//
// For RTA, call Resolve before building the call graph and pass the returned
// function to rta.Analyze as its root.
func Resolve(result *LoadResult, entry string) (*ssa.Function, error) {
	if strings.Contains(entry, ".") {
		return resolveMethod(result.SSAPkg, entry)
	}
	return resolveFunc(result.SSAPkg, entry)
}

// resolveFunc looks up a top-level function by name. If none is found it
// falls back to a bare-method scan across all package members.
func resolveFunc(ssaPkg *ssa.Package, name string) (*ssa.Function, error) {
	if fn := ssaPkg.Func(name); fn != nil {
		return fn, nil
	}
	return resolveBareMethod(ssaPkg, name)
}

// resolveMethod resolves a "Type.Method" entry-point string. It looks up the
// named type in the package members and searches its declared method list,
// which includes both pointer-receiver and value-receiver methods.
func resolveMethod(ssaPkg *ssa.Package, entry string) (*ssa.Function, error) {
	parts := strings.SplitN(entry, ".", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid entry point format %q: expected Type.Method", entry)
	}
	typeName, methodName := parts[0], parts[1]

	member, ok := ssaPkg.Members[typeName]
	if !ok {
		return nil, fmt.Errorf("type %q not found in package %s", typeName, ssaPkg.Pkg.Path())
	}
	typeMem, ok := member.(*ssa.Type)
	if !ok {
		return nil, fmt.Errorf("%q is not a type in package %s", typeName, ssaPkg.Pkg.Path())
	}
	named, ok := typeMem.Type().(*types.Named)
	if !ok {
		return nil, fmt.Errorf("%q is not a named type in package %s", typeName, ssaPkg.Pkg.Path())
	}

	for method := range named.Methods() {
		if method.Name() == methodName {
			return ssaPkg.Prog.FuncValue(method), nil
		}
	}

	return nil, fmt.Errorf("method %s not found on type %s (tried both pointer and value receiver)", methodName, typeName)
}

// resolveBareMethod scans all named types in the package for a method named
// name. Returns an error if zero or more than one match is found.
func resolveBareMethod(ssaPkg *ssa.Package, name string) (*ssa.Function, error) {
	prog := ssaPkg.Prog
	var matches []*ssa.Function

	for _, member := range ssaPkg.Members {
		typeMem, ok := member.(*ssa.Type)
		if !ok {
			continue
		}
		named, ok := typeMem.Type().(*types.Named)
		if !ok {
			continue
		}
		for method := range named.Methods() {
			if method.Name() == name {
				if fn := prog.FuncValue(method); fn != nil {
					matches = append(matches, fn)
				}
				break // at most one method with this name per type
			}
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("entry point %q not found in package %s", name, ssaPkg.Pkg.Path())
	case 1:
		return matches[0], nil
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.RelString(ssaPkg.Pkg)
		}
		return nil, fmt.Errorf("ambiguous entry point %q — matches: %s", name, strings.Join(names, ", "))
	}
}
