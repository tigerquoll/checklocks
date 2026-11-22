package checklocks

import (
	"fmt"
	"go/token"
	"go/types"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/analysis"
)

func stubOwner(facts ...analysis.ObjectFact) *ownerResult {
	return snapshotOf(&analysis.Pass{AllObjectFacts: func() []analysis.ObjectFact { return facts }})
}

func newObj(name string) types.Object {
	return types.NewVar(token.NoPos, nil, name, types.Typ[types.Int])
}

func wantPanic(t *testing.T, analyzer string, fact analysis.Fact, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		msg := fmt.Sprint(recover())
		for _, want := range []string{analyzer, fmt.Sprintf("%T", fact)} {
			if !strings.Contains(msg, want) {
				t.Errorf("panic %q does not contain %q", msg, want)
			}
		}
	}()
	f()
}

func TestFactViewGuards(t *testing.T) {
	obj := newObj("x")
	owner := stubOwner()
	gap := withOwnerFacts(&analysis.Pass{
		Analyzer: LockGapAnalyzer,
		ResultOf: map[*analysis.Analyzer]any{Analyzer: owner},
	}, Analyzer)
	blocking := withOwnerFacts(&analysis.Pass{
		Analyzer: LockBlockingAnalyzer,
		ResultOf: map[*analysis.Analyzer]any{Analyzer: owner, LockOrderAnalyzer: owner},
	}, Analyzer, LockOrderAnalyzer)

	wantPanic(t, "lockgap", &classFact{}, func() { gap.ImportObjectFact(obj, &classFact{}) })
	wantPanic(t, "lockgap", &orderFact{}, func() { gap.ImportPackageFact(nil, &orderFact{}) })
	wantPanic(t, "lockblocking", &orderFact{}, func() { blocking.ExportObjectFact(obj, &orderFact{}) })
	wantPanic(t, "lockblocking", &orderFact{}, func() { blocking.ExportPackageFact(&orderFact{}) })
}

func TestFactViewRouting(t *testing.T) {
	hit, miss := newObj("hit"), newObj("miss")
	checklocks := stubOwner()
	lockorder := stubOwner(analysis.ObjectFact{Object: hit, Fact: &classFact{Class: "c"}})
	p := withOwnerFacts(&analysis.Pass{
		Analyzer: LockBlockingAnalyzer,
		ResultOf: map[*analysis.Analyzer]any{Analyzer: checklocks, LockOrderAnalyzer: lockorder},
	}, Analyzer, LockOrderAnalyzer)

	var got classFact
	if !p.ImportObjectFact(hit, &got) || got.Class != "c" {
		t.Errorf("routed hit: got %+v", got)
	}
	got = classFact{}
	if p.ImportObjectFact(miss, &got) || got.Class != "" {
		t.Errorf("routed miss: got %+v", got)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var f classFact
			if !lockorder.importObject(hit, &f) || f.Class != "c" {
				t.Errorf("concurrent lookup: got %+v", f)
			}
		}()
	}
	wg.Wait()
}
