package checklocks

import (
	"fmt"
	"go/types"
	"reflect"
	"sync"

	"golang.org/x/tools/go/analysis"
)

// Outside go vet each analyzer has its own fact store, so facts cross analyzers only as an owner's Result.

type factKey struct {
	obj types.Object
	typ reflect.Type
}

// objectSnapshot is an owner's object facts as of the end of its Run.
type objectSnapshot struct {
	facts []analysis.ObjectFact
	once  sync.Once
	index map[factKey]analysis.Fact
}

// ownerResult is the Result of an analyzer whose facts other analyzers read.
type ownerResult struct {
	objects *objectSnapshot
	engine  *engineView
}

var ownerResultType = reflect.TypeOf((*ownerResult)(nil))

// snapshotOf takes the facts pass holds now; the owner calls it at each return of its Run.
func snapshotOf(pass *analysis.Pass) *ownerResult {
	return &ownerResult{objects: &objectSnapshot{facts: pass.AllObjectFacts()}}
}

// importObject copies the stored fact of fact's type for obj into fact.
func (r *ownerResult) importObject(obj types.Object, fact analysis.Fact) bool {
	if obj == nil {
		panic("nil object")
	}
	s := r.objects
	s.once.Do(func() {
		s.index = make(map[factKey]analysis.Fact, len(s.facts))
		for _, of := range s.facts {
			s.index[factKey{of.Object, reflect.TypeOf(of.Fact)}] = of.Fact
		}
	})
	stored, ok := s.index[factKey{obj, reflect.TypeOf(fact)}]
	if !ok {
		return false
	}
	reflect.ValueOf(fact).Elem().Set(reflect.ValueOf(stored).Elem())
	return true
}

// withOwnerFacts returns pass with the fact types of each owner answered from that
// owner's snapshot and every other fact type outside the analyzer's own a panic.
func withOwnerFacts(pass *analysis.Pass, owners ...*analysis.Analyzer) *analysis.Pass {
	own := make(map[reflect.Type]bool)
	for _, f := range pass.Analyzer.FactTypes {
		own[reflect.TypeOf(f)] = true
	}
	route := make(map[reflect.Type]*ownerResult)
	for _, a := range owners {
		r := pass.ResultOf[a].(*ownerResult)
		for _, f := range a.FactTypes {
			route[reflect.TypeOf(f)] = r
		}
	}
	bad := func(verb string, f analysis.Fact) {
		panic(fmt.Sprintf("%s: %s of fact type %T, which it does not own", pass.Analyzer.Name, verb, f))
	}
	p := *pass
	p.ImportObjectFact = func(obj types.Object, f analysis.Fact) bool {
		t := reflect.TypeOf(f)
		if own[t] {
			return pass.ImportObjectFact(obj, f)
		}
		r, ok := route[t]
		if !ok {
			bad("import", f)
		}
		return r.importObject(obj, f)
	}
	p.ImportPackageFact = func(pkg *types.Package, f analysis.Fact) bool {
		if !own[reflect.TypeOf(f)] {
			bad("package import", f)
		}
		return pass.ImportPackageFact(pkg, f)
	}
	p.ExportObjectFact = func(obj types.Object, f analysis.Fact) {
		if !own[reflect.TypeOf(f)] {
			bad("export", f)
		}
		pass.ExportObjectFact(obj, f)
	}
	p.ExportPackageFact = func(f analysis.Fact) {
		if !own[reflect.TypeOf(f)] {
			bad("package export", f)
		}
		pass.ExportPackageFact(f)
	}
	return &p
}
