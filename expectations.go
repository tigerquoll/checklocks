// Copyright 2026 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package checklocks

import (
	"fmt"
	"go/token"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// annotationSet names the self-check annotations belonging to one analyzer.
//
// Each analyzer in this module has its own set, so that a corpus can state
// expectations for one analyzer without the others acting on them. An empty
// string disables the corresponding annotation.
type annotationSet struct {
	// fail marks a line that must produce a diagnostic.
	fail string

	// ignore marks a line, or a function, whose diagnostics are dropped.
	ignore string

	// force marks a line whose assertions are made true rather than
	// checked. Not every analyzer has a meaningful notion of forcing.
	force string
}

// checkLocksAnnotations is the set belonging to the checklocks analyzer.
var checkLocksAnnotations = annotationSet{
	fail:   checkLocksFail,
	ignore: checkLocksIgnore,
	force:  checkLocksForce,
}

// failData indicates an expected failure.
type failData struct {
	pos   token.Pos
	wants []string
}

// positionKey is a simple position string.
type positionKey string

// expectations tracks the self-check annotations of a single analyzer.
//
// The corpus in test/ states what each analyzer must report, and this reports
// an expectation that was not met, so that a corpus regression fails as loudly
// as a real finding. It is shared by every analyzer in this module, keyed by a
// different annotationSet.
type expectations struct {
	pass *analysis.Pass
	set  annotationSet

	// reportInvalidPos reports diagnostics that have no source position.
	// These come from synthetic functions and cannot be annotated, so they
	// are suppressible.
	reportInvalidPos bool

	failures   map[positionKey]*failData
	exemptions map[positionKey]struct{}
	forced     map[positionKey]struct{}

	// groups holds the members of each grouped report until they are
	// emitted, and groupOrder keeps the emission deterministic.
	groups     map[string][]groupMember
	groupOrder []string
}

// newExpectations returns expectations for the given annotation set.
func newExpectations(pass *analysis.Pass, set annotationSet, reportInvalidPos bool) *expectations {
	return &expectations{
		pass:             pass,
		set:              set,
		reportInvalidPos: reportInvalidPos,
		failures:         make(map[positionKey]*failData),
		exemptions:       make(map[positionKey]struct{}),
		forced:           make(map[positionKey]struct{}),
	}
}

// positionKey converts from a token.Pos to a key we can use to track failures
// as the position of the failure annotation is not the same as the position of
// the actual failure (different column/offsets). Hence we ignore these fields
// and only use the file/line numbers to track failures.
func (e *expectations) positionKey(pos token.Pos) positionKey {
	position := e.pass.Fset.Position(pos)
	return positionKey(fmt.Sprintf("%s:%d", position.Filename, position.Line))
}

// addFailures adds an expected failure.
func (e *expectations) addFailures(pos token.Pos, s string) {
	s, want, ok := strings.Cut(s, "=")
	if !ok && s != "" {
		e.pass.Reportf(pos, "unable to parse failure annotation %q", s)
		return
	}
	e.failures[e.positionKey(pos)] = &failData{
		pos:   pos,
		wants: strings.Split(want, "|"),
	}
}

// addExemption adds an exemption.
func (e *expectations) addExemption(pos token.Pos) {
	e.exemptions[e.positionKey(pos)] = struct{}{}
}

// addForce adds a force annotation.
func (e *expectations) addForce(pos token.Pos) {
	e.forced[e.positionKey(pos)] = struct{}{}
}

// maybeFail checks a potential failure against a specific failure map.
func (e *expectations) maybeFail(pos token.Pos, fmtStr string, args ...any) {
	if msg, ok := e.consider(pos, fmtStr, args...); ok {
		e.pass.Report(analysis.Diagnostic{Pos: pos, Message: msg})
	}
}

// consider decides whether a potential failure is reported, and returns the
// message it would carry.
//
// This is where an expectation is met and where an ignore takes effect, so it
// runs for every potential failure whether or not one is emitted for it. A
// caller that groups its reports must still call it for each site, or a
// suppressed site would stay suppressed while its expectation went unmet.
func (e *expectations) consider(pos token.Pos, fmtStr string, args ...any) (string, bool) {
	msg := fmt.Sprintf(fmtStr, args...)
	if fd, ok := e.failures[e.positionKey(pos)]; ok {
		index := slices.IndexFunc(fd.wants, func(want string) bool {
			return strings.Contains(msg, want)
		})
		if index != -1 {
			fd.wants = slices.Delete(fd.wants, index, index+1)
			return "", false
		}
	}
	if _, ok := e.exemptions[e.positionKey(pos)]; ok {
		return "", false // Ignored, not counted.
	}
	if !e.reportInvalidPos && !pos.IsValid() {
		return "", false // Ignored, implicit.
	}
	return msg, true
}

// groupMember is one site of a grouped report.
type groupMember struct {
	pos token.Pos
	msg string
}

// maybeFailGrouped reports a potential failure as part of a group.
//
// One defect that reaches several call sites is one defect, and reporting it
// once at each site inflates the count of things to look at without adding
// anything: the sites share a cause and a fix. The members of a group are
// gathered here and emitted together by flushGroups.
//
// Suppression is unchanged, and is decided per site: an ignore on one call
// site removes that site from its group and nothing else. A group all of whose
// sites are suppressed is silent, because it has no members left.
func (e *expectations) maybeFailGrouped(key string, pos token.Pos, fmtStr string, args ...any) {
	msg, ok := e.consider(pos, fmtStr, args...)
	if !ok {
		return
	}
	if e.groups == nil {
		e.groups = make(map[string][]groupMember)
	}
	if _, seen := e.groups[key]; !seen {
		e.groupOrder = append(e.groupOrder, key)
	}
	e.groups[key] = append(e.groups[key], groupMember{pos: pos, msg: msg})
}

// flushGroups emits one diagnostic per group.
//
// The first site carries the message and the rest are attached to it as
// related positions, which go vet prints beneath it, each with its own
// position. The output is no shorter, but it says which sites are one defect.
func (e *expectations) flushGroups() {
	for _, key := range e.groupOrder {
		members := e.groups[key]
		if len(members) == 0 {
			continue
		}
		var related []analysis.RelatedInformation
		for _, m := range members[1:] {
			related = append(related, analysis.RelatedInformation{
				Pos:     m.pos,
				End:     m.pos,
				Message: "and reached from here",
			})
		}
		e.pass.Report(analysis.Diagnostic{
			Pos:     members[0].pos,
			Message: members[0].msg,
			Related: related,
		})
	}
	e.groups = nil
	e.groupOrder = nil
}

// checkFailures checks for the expected failure counts.
func (e *expectations) checkFailures() {
	for _, fd := range e.failures {
		wildcards := 0
		for _, want := range fd.wants {
			if want == "" {
				wildcards++
				continue
			}
			e.pass.Reportf(fd.pos, "missing expected failure %q", want)
		}
		if wildcards != 0 {
			e.pass.Reportf(fd.pos, "missing %d expected failures", wildcards)
		}
	}
}

// extractLineFailures extracts all line-based exceptions.
//
// Note that this applies only to individual line exemptions, and does not
// consider function-wide exemptions, or specific field exemptions, which are
// extracted separately as part of the saved facts for those objects.
func (e *expectations) extractLineFailures() {
	for _, f := range e.pass.Files {
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				// N.B. the handlers capture c, so they are built
				// per comment rather than once.
				handlers := make(map[string]func(string), 3)
				if e.set.fail != "" {
					handlers[e.set.fail] = func(p string) { e.addFailures(c.Pos(), p) }
				}
				if e.set.ignore != "" {
					handlers[e.set.ignore] = func(string) { e.addExemption(c.Pos()) }
				}
				if e.set.force != "" {
					handlers[e.set.force] = func(string) { e.addForce(c.Pos()) }
				}
				extractAnnotations(c.Text, handlers)
			}
		}
	}
}

// extractAnnotations extracts annotations from text.
func extractAnnotations(s string, fns map[string]func(p string)) {
	for _, annotation := range annotationFields(s) {
		for prefix, fn := range fns {
			if strings.HasPrefix(annotation, prefix) {
				fn(annotation[len(prefix):])
			}
		}
	}
}

// annotationFields splits a comment into the annotations it carries, each in the form the
// prefixes are written in, so that a comment may carry more than one.
//
// A line that has to be silenced for two analyses can then say so on that line:
//
//	other.value = <-other.ch // +checklocksignore +lockblockingignore
//
// rather than pushing one of the two up to the whole function, which is what a code base
// ends up doing otherwise and which silences far more than the line.
//
// The separator is a space before the plus, because an annotation always begins with one and
// a payload may contain spaces of its own: "+lockorder:A < B" and a failure message are both
// one annotation. A payload that contains " +" is therefore split, which is the price; no
// annotation defined here can produce one.
//
// The comment must BEGIN with an annotation, as it always had to. Anything else is prose,
// and prose that mentions an annotation is common in a code base that uses them.
func annotationFields(text string) []string {
	rest, ok := strings.CutPrefix(text, "//")
	if !ok {
		return nil
	}
	rest = strings.TrimPrefix(rest, " ")
	if !strings.HasPrefix(rest, "+") {
		return nil
	}
	fields := strings.Split(rest, " +")
	out := make([]string, 0, len(fields))
	for i, field := range fields {
		if i > 0 {
			field = "+" + field
		}
		out = append(out, "// "+strings.TrimRight(field, " \t"))
	}
	return out
}
