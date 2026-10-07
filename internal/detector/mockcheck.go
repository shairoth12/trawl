package detector

import "go/types"

// IsMock reports whether t (or the type t points to) is a generated mock: a
// struct with a field of type mock.Mock (testify, mockery) or
// *gomock.Controller (mockgen). The check uses package names, not import
// paths, so forks of those libraries count too. The type's name is not
// checked, so a real type named MockingbirdClient is not a mock, and a
// hand-written mock without such a field is not a mock either.
func IsMock(t types.Type) bool {
	if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		t = ptr.Elem()
	}
	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for field := range st.Fields() {
		if isMockLibraryType(field.Type()) {
			return true
		}
	}
	return false
}

// IsMockMethod reports whether sig belongs to a method on a generated mock
// (see IsMock). Mocks in production packages satisfy interfaces, so the call
// graph routes interface calls through them, but their bodies only record the
// call for the test.
func IsMockMethod(sig *types.Signature) bool {
	recv := sig.Recv()
	return recv != nil && IsMock(recv.Type())
}

// isMockLibraryType reports whether t is mock.Mock or gomock.Controller,
// directly or through a pointer.
func isMockLibraryType(t types.Type) bool {
	if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	pkg, name := named.Obj().Pkg().Name(), named.Obj().Name()
	return pkg == "mock" && name == "Mock" || pkg == "gomock" && name == "Controller"
}
