# checklocks

Checklocks is an analyzer for lock and atomic constraints. The analyzer relies
on explicit annotations to identify fields that should be checked for access.

This module also carries analyzers built on top of it, sharing its annotations
and its view of which fields are guarded. They ship in the same binary and are
individually switchable; see [Analyzers](#analyzers).

This is a standalone extraction of gVisor's `tools/checklocks`, which is not
published as an importable Go module of its own. It is packaged here so that it
can be installed and pinned like any other tool, with fixes applied that are
still working their way upstream.

> **This is a temporary home.** The repository exists so the analyzer can be
> consumed by Go module tooling, which requires a public repository. It may move
> to a permanent home later, so avoid depending on this exact path in anything
> that would be expensive to change. Fixes made here are intended for upstream.

## Provenance

Derived from [google/gvisor](https://github.com/google/gvisor) `tools/checklocks`
at commit
[`1919d96`](https://github.com/google/gvisor/commit/1919d9633a18b204f8ab29dbae3c3b90bc93f07d),
Apache License 2.0. Original copyright headers are retained; see `NOTICE`.

The base import is a single commit, and each fix is a separate commit on top, so
`git log` shows exactly what diverges from upstream. The fixes are:

| Fix | Upstream |
| --- | --- |
| Panic on cross-package use of unexported global guards | [google/gvisor#14078](https://github.com/google/gvisor/pull/14078), filed |
| Pointer-typed global guards resolved through the pointer | [tigerquoll/gvisor#3](https://github.com/tigerquoll/gvisor/pull/3), pending |
| Guard annotations on package-level variable declarations | [tigerquoll/gvisor#4](https://github.com/tigerquoll/gvisor/pull/4), pending |

The `checklocks` analyzer is otherwise unmodified apart from what standing
alone requires: the analyzer package moved to the repository root, import paths
were rewritten, and the bazel `BUILD` files were dropped.

Also applied to `checklocks` itself, and not yet filed upstream: calls to a
function that returns a package-level variable resolve to that variable, so an
annotation naming the variable and a lock reached through the accessor unify.

Added here rather than derived from gVisor: the `lockstringer` analyzer, the
`go test` driver, and the multi-analyzer binary. The self-check machinery that
`checklocks` uses for its own corpus was generalised so that each analyzer has
its own `+<analyzer>fail` marker, which is the only change to its behaviour and
is not observable in its output.

## Installation

```sh
go install github.com/tigerquoll/checklocks/cmd/checklocks@latest
```

## Usage

The binary is a `go vet` tool running every analyzer in this module. If
installed to the default path:

```sh
go vet -vettool=$HOME/go/bin/checklocks ./...
```

### Analyzers

| Analyzer | What it checks |
| --- | --- |
| `checklocks` | lock and atomic constraints, from annotations on fields and functions |
| `lockstringer` | lock hazards in lazily evaluated methods such as `String` |
| `lockorder` | acquisition order between declared lock classes |
| `lockblocking` | waiting while a lock class is held |

Each may be turned off by name, which is how a project adopts one at a time:

```sh
go vet -vettool=$HOME/go/bin/checklocks -lockstringer=false ./...
```

A disabled analyzer still runs when another one requires it, so disabling
`checklocks` silences its diagnostics without depriving `lockstringer` of the
guard annotations it reads.

### Flags

Each analyzer's own flags are namespaced under its name:

*   `-checklocks.inferred` (default true): suggest annotations for fields that
    are observed to be accessed under a lock most of the time. The suggestions
    are based on observation ratios and so are sensitive to unrelated nearby
    changes; a project gating CI on the analyzer will generally want
    `-checklocks.inferred=false` plus deliberate annotation.
*   `-checklocks.atomic` (default true): enable the atomic access checks.
*   `-checklocks.wrappers` (default true): report diagnostics that have no
    source position. These arise from synthetic wrapper functions and cannot be
    annotated or suppressed in source, so `-checklocks.wrappers=false` is
    usually wanted when the output must be clean. gVisor excludes them by
    configuration for the same reason.
*   `-lockstringer.methods` (default empty): comma separated additional method
    names to treat as lazily evaluated, for a project specific interface.
*   `-lockorder.hierarchy` (default **false**): also check the direction of
    nesting within a hierarchical class; see below.
*   `-lockorder.hierarchyinfer` (default false): with `-lockorder.hierarchy`,
    treat a field of the type's own type as the parent edge when none is
    annotated.

```sh
go vet -vettool=$HOME/go/bin/checklocks \
	-checklocks.inferred=false -checklocks.wrappers=false ./...
```

> **Flag names changed when the second analyzer was added.** The binary runs
> several analyzers now, so `go vet` requires each flag to name its analyzer:
> `-inferred=false` is now `-checklocks.inferred=false`, and likewise for
> `-atomic` and `-wrappers`. The old spellings are rejected outright rather than
> ignored, so a stale command line fails loudly rather than silently analyzing
> with different settings.

## Annotations

This analyzer supports annotations for atomic access and lock enforcement, in
order to allow for mixed semantics. These are first described separately, then
the combination is discussed.

### Atomic Access Enforcement

Individual struct members may be noted as requiring atomic access. These
annotations are of the form `+checkatomic`, for example:

```go
type foo struct {
  // +checkatomic
  bar int32
}
```

This will ensure that all accesses to bar are atomic, with the exception of
operations on newly allocated objects (when detectable).

## Lock Enforcement

Individual struct members may be protected by annotations that indicate locking
requirements for accessing members. These annotations are of the form
`+checklocks`, for example:

```go
type foo struct {
    mu sync.Mutex

    // +checklocks:mu
    bar int

    foo int  // No annotation on foo means it's not guarded by mu.

    secondMu sync.RWMutex

    // Multiple annotations indicate that both must be held but the
    // checker does not assert any lock ordering.
    // +checklocks:secondMu
    // +checklocks:mu
    foobar int
}
```

These semantics are enforceable on `sync.Mutex`, `sync.RWMutex` and
`sync.Locker` fields. Semantics with respect to reading and writing are
automatically detected and enforced. If an access is read-only, then the lock
need only be held as a read lock, in the case of an `sync.RWMutex`.

Package-level variables may be guarded in the same way. The annotation may be
attached to the variable declaration or, within a parenthesized declaration, to
the individual variable.

A type that wraps a lock may be declared to be one, with
`+checklockslocktype` on the type. Its `Lock`, `Unlock`, `RLock`, `RUnlock`,
`NestedLock`, `NestedUnlock` and `DowngradeLock` methods then become
primitives: they are intercepted at each call site exactly as the standard
ones are, and their own bodies are not analyzed, because the body of a
primitive is the implementation of a lock rather than a critical section.

```go
// +checklockslocktype
type Guard struct {
    mu sync.Mutex
}

func (g *Guard) Lock()   { g.mu.Lock() }
func (g *Guard) Unlock() { g.mu.Unlock() }
```

Whether the type behaves as a `Mutex` or an `RWMutex` is taken from the type
itself: it has an `RLock` method or it does not.

Without the declaration a wrapper needs `+checklocksignore` on each forwarder,
to silence the balance error a method that takes a lock and does not release it
would otherwise produce. That ignore is read at every call site, not only in
the forwarder, so it also suppresses the "already locked" and "unlock while not
held" diagnostics for every user of the lock. Declaring the type is what gets
those back.

The locks must be resolvable within the scope of the declaration. This means the
lock must refer to one of:

*   A struct-local lock (e.g. mu).
*   A lock resolvable from the local struct (e.g. fieldX.mu).
*   A global lock (e.g. globalMu).
*   A lock resolvable from a global struct (e.g. globalX.mu).

An unexported global lock is enforceable only within the package that declares
it. Export data does not carry unexported package-level variables, so the lock
cannot be resolved elsewhere and the annotation is silently skipped at use sites
in other packages. Export the lock if cross-package enforcement is required.

A function that does nothing but return a package-level variable is treated as
naming that variable, so a lock reached through the accessor's result is the
same lock as one named directly:

```go
var instance = &foo{}

func getInstance() *foo { return instance }

// +checklocksexclude:instance.mu
func doThing() { ... }

func caller() {
    f := getInstance()
    f.mu.Lock()
    doThing()   // reported: instance.mu is held
    f.mu.Unlock()
}
```

The function must have exactly one return, of exactly one value, and that value
must be the variable or a load of it. A function that may return either of two
variables is not treated this way. Statements before the return are not
examined, so the accessor of a lazily initialised singleton, which runs an
initialiser and then returns the variable, is included.

Like atomic access enforcement, checks may be elided on newly allocated objects.

### Function Annotations

The `+checklocks` annotation may apply to functions. For example:

```go
// +checklocks:f.mu
func (f *foo) doThingLocked() { }
```

The field provided in the `+checklocks` annotation must be resolvable as one of:

*   A parameter, receiver or return value (e.g. mu).
*   A lock resolvable from a parameter, receiver or return value (e.g. f.mu).
*   A global lock (e.g. globalMu).
*   A lock resolvable from a global struct (e.g. globalX.mu).
*   A lock on a value recovered from a parameter by a type assertion (e.g.
    event.Args[0].(*Application).mu); see below.

This annotation will ensure that the given lock is held for all calls, and all
analysis of this function will assume that this is the case. The limitation on
unexported global locks described above applies here also: callers in other
packages cannot resolve the lock, so the annotation is not enforced for them.

Additional variants of the `+checklocks` annotation are supported for functions:

*   `+checklocksread`: This enforces that at least a read lock is held. Note
    that this assumption will apply locally, so accesses and function calls will
    assume that only a read lock is available.
*   `+checklocksexclude`: This enforces that the given lock is *not* held on
    entry. This assertion is checked at call sites, but does not modify the
    caller's lock state.
*   `+checklocksexcludewrite`: This enforces that the given lock is *not* held
    exclusively on entry. This assertion is checked at call sites, but does not
    modify the caller's lock state.
*   `+checklocksacquire`: This enforces that the given lock is *not* held on
    entry, but it will be held on exit. This assertion will be checked locally
    and applied to the caller's lock state.
*   `+checklocksrelease`: This enforces that the given lock is held on entry,
    and will be release on exit. This assertion is checked locally and applied
    to the caller's lock state.
*   `+checklocksacquireread`: A read variant of `+checklocksacquire`.
*   `+checklocksreleaseread`: A read variant of `+checklocksrelease`.

For examples of these cases see the tests.

### Type Alias Annotations

Types may declare aliases between locks that are structurally equivalent across
all instances of the type. These annotations must appear on the type
declaration, and the names are resolved relative to the type itself.

```go
// +checklocksalias:inner.mu=mu
type example struct {
  mu    sync.Mutex
  inner struct{ mu sync.Mutex }
}
```

The alias above means `example.inner.mu` is treated as the same lock as
`example.mu` anywhere a value of type `example` is used.

#### Anonymous Functions and Closures

A function literal may be annotated, with the same annotations a declared
function takes. A literal handed to something this analysis cannot follow, such
as a callback table read by a state machine library, is otherwise analyzed
holding nothing, and every guarded access in it is reported; the annotation
says what the caller holds when it runs.

```go
register(callbacks{
    // +checklocks:t.mu
    "enter": func(t *target) {
        t.value = 1
    },
})
```

A literal has no declaration to carry a doc comment, so the comment is matched
to the surrounding syntax. It binds when it sits immediately above:

*   the literal itself, and exactly one literal begins on that line;
*   an assignment or declaration whose single value is that literal;
*   a key and value in a composite literal whose value is that literal.

A comment above a line on which several literals begin binds to none of them,
rather than to all of them: it does not say which it means. The guard is
resolved against the literal's own parameters, or a package-level variable,
exactly as it is for a declared function — **a value the literal obtains from
inside its own body, such as one recovered from an argument by type assertion,
cannot be named**, since the annotation is resolved where it is written.

Note that a literal invoked in a scope this analysis can follow is unaffected:
its caller's real lock state is known, and is more precise than an annotation.
An ignore on a literal applies in both cases.

If anonymous functions and closures are bound and invoked within a single scope,
the analysis will happen with the available lock state. For example, the
following will not report any violations:

```go
func foo(ts *testStruct) {
  x := func() {
    ts.guardedField = 1
  }
  ts.mu.Lock()
  x() // We know the context x is being invoked.
  ts.mu.Unlock()
}
```

This pattern often applies to defer usage, which allows deferred functions to be
fully analyzed with the lock state at time of execution.

However, if a closure is passed to another function, the anonymous function
backing that closure will be analyzed assuming no available lock state. For
example, the following will report violations:

```go
func runFunc(f func()) {
  f()
}

func foo(ts *testStruct) {
  x := func() {
    ts.guardedField = 1
  }
  ts.mu.Lock()
  runFunc(x) // We can't know what will happen with x.
  ts.mu.Unlock()
}
```

Since x cannot be annotated, this may require use of the force annotation used
below. However, if anonymous functions and closures require annotations, there
may be an opportunity to split them into named functions for improved analysis
and debuggability, and avoid the need to use force annotations.

### Mixed Atomic Access and Lock Enforcement

Some members may allow read-only atomic access, but be protected against writes
by a mutex. Generally, this imposes the following requirements:

For a read, one of the following must be true:

1.  A lock held be held.
1.  The access is atomic.

For a write, both of the following must be true:

1.  The lock must be held.
1.  The write must be atomic.

In order to annotate a relevant field, simply apply *both* annotations from
above. For example:

```go
type foo struct {
  mu sync.Mutex
  // +checklocks:mu
  // +checkatomic
  bar int32
}
```

This enforces that the preconditions above are upheld.

This also applies to atomic wrapper types (for example, atomic.Int32). In the
mixed case, lock-free access is limited to read-only atomic operations such as
Load. Any atomic write operation (for example, Store, Swap, or Add) requires the
lock to be held.

## Ignoring and Forcing

From time to time, it may be necessary to ignore results produced by the
analyzer. These can be disabled on a per-field, per-function or per-line basis.

For fields, only lock suggestions may be ignored. See below for details.

For functions, the `+checklocksignore` annotation can be applied. This prevents
any local analysis from taking place. Note that the other annotations can still
be applied to the function, which will enforce assertions in caller analysis.
For example:

```go
// +checklocks:ts.mu
// +checklocksignore
func foo(ts *testStruct) {
  ts.guardedField = 1
}
```

For individual lines, the `+checklocksforce` annotation can be applied after the
statement. This does not simply ignore the line, rather it *forces* the
necessary assertion to become true. For example, if a lock must be held, this
annotation will mark that lock as held for all subsequent lines. For example:

```go
func foo(ts *testStruct) {
  ts.guardedField = 1 // +checklocksforce: don't care about locking.
}
```

In general, both annotations should be highly discouraged. It should be possible
to avoid their use by factoring functions in such a way that annotations can be
applied consistently and without the need for ignoring and forcing.

### More than one annotation on a comment

A comment may carry several annotations, separated by a space:

```go
other.value = <-other.ch // +checklocksignore +lockblockingignore
```

This matters where the analyses meet. One line can be a finding in two of their
terms, and before a comment could carry two annotations the second one had to
go on the FUNCTION, which silences the whole body rather than the line. On one
real code base that widened two suppressions to two entire functions, and a
wait added to either of them later would have been silenced with them.

The separator is a space before the plus, because an annotation always begins
with one while a payload may contain spaces of its own: `+lockorder:A < B` and
a failure message are each one annotation. A payload containing `" +"` would be
split, which no annotation defined here can produce.

The comment must still BEGIN with an annotation. A comment that mentions one in
passing is prose, and prose about annotations is common in a code base that
uses them.

## Objects Under Construction

An object nothing else can reach cannot be raced with, so its guards say nothing
while it is being built. Stating that with an ignore on every line is the largest
single family of suppressions in an annotated code base, and each one silences a
line rather than the reason for it.

Two annotations state the reason, and both are checked:

```go
// +checklocksreturnsfresh
func newQueue(name string) *Queue {
    q := &Queue{}
    q.lock.setClass(1)  // taking the address of a field is not publishing the object
    q.name = name       // no lock: nothing else can reach q
    return q
}

// +checklocksfresh:child
func (q *Queue) addChild(child *Queue) {
    q.lock.Lock()
    defer q.lock.Unlock()
    q.children[child.name] = child
    child.parent = q    // no lock on the child: the caller promised it is unpublished
}

func build(parent *Queue, name string) {
    child := newQueue(name)  // fresh, because the constructor says so and was checked
    parent.addChild(child)   // checked here: the argument must be provably unpublished
}
```

`+checklocksreturnsfresh` says the returned object cannot be reached by another
goroutine yet. It is verified in the function that carries it: what is returned
must itself be freshly allocated, or from another such constructor, and must not
have been published on the way to the `return`.

`+checklocksfresh:p` says a parameter must arrive unpublished, and is verified at
every call site. Both the guarded fields of such an object and the lock
preconditions of what it is passed to are then elided while it stays unpublished.

### What ends it

Freshness is positional: it ends where the object is published, and only for the
code that can run after that point.

| | |
| --- | --- |
| stored in a global, an unguarded field, a channel, an interface | published |
| captured by a closure or a `go` statement | published |
| passed to a parameter the callee publishes | published |
| passed to a function nothing is known about | published |
| stored into a **lock-guarded** field of an object this function did not build | **not** published for the rest of that critical section |
| `&obj.field` taken and passed on | publishes that field, not the object |

Every case the table does not name publishes. An unknown callee — an interface
method, a function outside the analyzed packages — publishes everything it is
given, so the failure mode of not knowing is a report rather than a silence.

Two of those need a word. **Taking the address of a field is not a handle on the
object**: a goroutine holding `&q.lock` can lock the queue and cannot read
`q.name`, and since taking the lock's address is what every lock call does, the
opposite rule would leave nothing fresh anywhere. **Publishing into a guarded
container** is safe exactly as long as the lock is held, which is why it holds
for the rest of that critical section and not beyond it: a callee that inserts
its argument into a map and returns has published it as far as its caller is
concerned, and the caller's next unguarded write is reported.

### Limits

*   A call site the analysis cannot see cannot be checked: an interface dispatch,
    or a caller in a package outside the run. That is the boundary every
    annotation in this tool has.
*   A fresh object stored into another fresh object's field is treated as
    published. Following it would need the container's own publication to end the
    contained object's freshness, which is a reachability question this does not
    answer.
*   A variable captured by a closure is address-taken, and freshness is lost for
    the whole function rather than from the capture onwards.

## Testing

Tests can be built using the `+checklocksfail` annotation. When applied after a
statement, these will generate a report if the line does *not* fail an
assertion. The optional value matches a substring of the failure message and
multiple expected failures can be separated with `|`. For example:

```go
func foo(ts *testStruct) {
  ts.guardedField = 1 // +checklocksfail=violation
}
```

These annotations are primarily useful for analyzer development and testing.

## Suggestions

Based on locks held during field access, the analyzer may suggest annotations.
These can be ignored with the `+checklocksignore` annotation on fields.

```go
type foo struct {
  mu sync.Mutex
  bar int32 // +checklocksignore: mu is inferred as requisite.
}
```

The annotation will be generated when the lock is held the vast majority of the
time the field is accessed. Note that it is possible for this frequency to be
greater than 100%, if the lock is held multiple times. For example:

```go
func foo(ts1 *testStruct, ts2 *testStruct) {
  ts1.Lock()
  ts2.Lock()
  ts1.guardedField = 1 // 200% locks held.
  ts1.Unlock()
  ts2.Unlock()
}
```

It should be expected that this annotation is also rare. If the field is not
protected by the mutex, it suggests that the critical section could be made
smaller by restructuring the code or the structure instead of applying the
ignore annotation.

## lockstringer

`String`, `Error`, `MarshalJSON` and `MarshalLogObject` are called by `fmt`,
`encoding/json` and `zap` at a point the type's author does not choose. The
caller's lock state is therefore unknowable, which makes both of the obvious
implementations wrong:

*   reading a lock-guarded field without the lock races with any writer, and
*   taking the receiver's own lock deadlocks when the value is formatted by
    code that already holds it, which is exactly what logging under a lock
    does.

`lockstringer` reports both. The remedy for both is the same, and is what the
diagnostic suggests: restrict the method to fields fixed at construction, or
have the caller take a snapshot under the lock and log that.

```go
type node struct {
    mu sync.Mutex
    // +checklocks:mu
    allocations int
    id          string
}

func (n *node) String() string {
    return fmt.Sprintf("%s: %d", n.id, n.allocations) // reported: guarded read races
}

func (n *node) GoString() string {
    n.mu.Lock()                                       // reported: self-deadlock
    defer n.mu.Unlock()
    return fmt.Sprintf("%s: %d", n.id, n.allocations)
}
```

The second rule also fires when the lock is taken by a method the lazy method
calls, such as a self-locking accessor, or by a function annotated as acquiring
a lock.

### Scope

The analysis is deliberately narrow, because a diagnostic here asks for a
redesign of the method rather than a local fix:

*   Only types that carry their own lock **and** have at least one
    `+checklocks` guarded field are considered. A type that has not annotated
    what its lock protects has not said enough for this to be meaningful.
*   Only guarded fields of the receiver's own type are reported. A guarded
    field reached through another object is an ordinary violation that
    `checklocks` already reports.
*   The two rules do not both fire on one method. A method that takes its own
    lock is reported for that, and its reads are not additionally reported as
    racy.
*   Callee inspection is one level deep and within the package. A lock taken
    two calls away is not found; that needs the summary facts a lock ordering
    analysis would introduce.

Note that a `+checklocksignore` does not suppress these diagnostics, and should
not: silencing `checklocks` on a `String` method is the usual way this hazard
comes to be, since neither locking nor not locking is correct.

### Annotations

*   `+lockstringerignore`: on a function, drops every diagnostic in its body;
    on a line, drops the diagnostics reported on that line.
*   `+lockstringerfail`: states that a line must be reported, for the test
    corpus, exactly as `+checklocksfail` does for `checklocks`.

## Derived exclusions

A method that takes its own receiver's lock cannot be called by anyone already
holding it: the second acquisition deadlocks. That is what
`+checklocksexclude` states, and it is derivable from the body, so it is
derived rather than restated.

```go
// No annotation. The exclusion is derived from what it does.
func (t *target) selfLocking() {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.value++
}

func caller(t *target) {
    t.mu.Lock()
    t.selfLocking()   // reported: must not hold t.mu
    t.mu.Unlock()
}
```

A method that takes the **write** lock excludes a caller holding it in any
mode. One that takes only the **read** lock excludes a caller holding it for
writing, which is what `+checklocksexcludewrite` says.

The derivation travels: a method that reaches the lock through another method
of the same receiver takes it too, resolved to a fixpoint over the package.

This is sounder than the annotation it replaces. A self-acquiring method has no
legitimate caller holding the lock, so a method that was never annotated was
not exempt from the rule, it was unchecked. Deriving it closes those gaps
instead of preserving them.

Nothing here counts call sites. The derivation is from the lock the body takes,
never from how often the method happens to be called under one; inferring a
requirement from usage frequency lets code that violates an invariant look like
evidence against it.

### What is not derived

*   A lock the method is declared to hold on entry, with `+checklocks`, is not
    one it acquires, and is left alone.
*   A lock on another object. Taking `other.mu` says nothing about the
    receiver's.
*   A method carrying `+checklocksignore`.
*   An exclusion already written by hand, which may be broader than the derived
    one.

`-checklocks.synthexcludes=false` turns the derivation off.

## Locks on asserted values

A callback invoked by a library receives its subject as an interface, inside a
parameter, and recovers it by asserting a type:

```go
register(fsm.Callbacks{
    // +checklocks:event.Args[0].(*Application).mu
    "enter_state": func(_ context.Context, event *fsm.Event) {
        app := event.Args[0].(*Application)
        app.state = "entered"
    },
})
```

The lock is held by whoever asked the library to run the callback, which
nothing here can see, and the subject is not nameable by any other means: it
exists only as a local of the body, while an annotation is resolved where it is
written. Without this the only options are to report every access in the body
or to silence it, and silencing is what a code base actually does.

The guard is written exactly as the Go expression that recovers the value. It
introduces no second syntax, and it contains no spaces, so it cannot collide
with the separator that lets one comment carry several annotations. The type
may be written with or without a package qualifier.

### Binding

The guard is matched against the assertions the body performs. An assertion
binds when it asserts the named type and its operand is reached from the named
parameter by the named path; the lock is then recorded against the asserted
value for the body.

*   **Several assertions bind together.** A body that recovers its subject on
    more than one path is covered throughout: the first is what the lock is
    recorded against and the others are aliased to it, so the lock is still
    counted once and the return balance check is unaffected.
*   **An assertion to another type does not bind.** It is a different subject.
*   **A different path does not bind**, including a different constant index.
*   **A guard that matches no assertion records nothing**, silently. The
    accesses it was meant to cover are then reported in their own right, which
    says the same thing where the reader needs it.

### The comma-ok form, and what it costs

`app, ok := event.Args[0].(*Application)` binds as well, and this is the one
imprecision worth stating: the lock is recorded for the asserted value
throughout the body, including the branch the assertion failed on, where the
value is nil. An access there is not reported.

The alternative — recording the lock only where the assertion succeeded —
needs the guard to be seeded per block rather than at entry, and the block
walk memoises by block, so a join reached from both branches would take
whichever state arrived first. The imprecision is confined to a path on which
any use of the value panics, and the alternative is a false positive on every
correct comma-ok callback, which is the worse of the two.

## Hierarchical ordering

A class marked `+lockhierarchical` is exempt from the rule that two locks of one
class must not nest, because instances of it nest legitimately: a tree walks
itself. What that exemption cannot express is the *direction*. Both sides are
the same class, so parent-then-child and child-then-parent look identical to a
class-level check, and only one of them is safe.

`-lockorder.hierarchy` recovers the direction from the structure. The link from
an instance to its parent is a field, so an acquisition whose receiver was
reached through that field of an instance already holding its lock is a
child-then-parent nesting:

```go
// +lockhierarchical:Queue
package scheduler

// +lockclass:Queue
type Queue struct {
    sync.RWMutex

    // +lockhierarchyedge
    parent *Queue

    children []*Queue
}

func (q *Queue) bad() {
    q.Lock()
    defer q.Unlock()
    q.parent.RLock()   // reported: a hierarchical class must be locked parent first
    defer q.parent.RUnlock()
}
```

Taking a child while the parent is held is the sanctioned direction and is
silent, as is the parent-first idiom, where the parent is consulted with no lock
held and only then is this instance locked.

### Scope

The check is **intraprocedural only**, and off by default. Both follow from what
it rests on:

*   It compares *instances*, and instance identity does not survive a summary. A
    summary records which classes a function may acquire, not which instance
    they were reached from, so a parent acquired one call deeper cannot be tied
    back to the child held here. The cross-function case belongs to a runtime
    checker, which sees instances.
*   Only the value shapes a field read produces are followed: the load of a
    pointer field, a field of a struct value, and conversions. A parent that
    arrives by another route — a function result, or a value this walk cannot
    tie back — is not recognised and is not reported. Reaching a *sibling* by
    going up and back down is likewise not reported, since the acquisition is
    not the parent itself.
*   Mere same-class nesting is never reported here. That is the class-level
    rule, and for a hierarchical class it is deliberately exempt.

Because the approximation accepts those escapes, it is opt-in rather than
something an existing user starts seeing without asking. Silence from it is
weak evidence; a report from it is strong evidence.

The parent edge must be annotated with `+lockhierarchyedge` on the field.
`-lockorder.hierarchyinfer` will instead take a single field of the type's own
type as the parent link when nothing is annotated, which is convenient on a code
base that has not annotated yet, but a self-typed field is not necessarily a
parent link.

## lockblocking

A lock held across a wait is not a deadlock and breaks no ordering rule, so
neither `lockorder` nor a runtime cycle detector has anything to say about it.
What it does is bound the time every other user of that lock waits by the time
the wait takes, which for a round trip to another process is unbounded: if the
far side never answers, the lock is never released and the system stops.

`lockblocking` reports reaching a wait while a declared lock class is held. It
uses the classes `lockorder` declares, and it does not use the order: the
question is whether a lock is held, not which one.

```go
// +lockclass:App
type app struct{ mu sync.Mutex }

func (a *app) release(c chan result) {
    a.mu.Lock()
    defer a.mu.Unlock()
    a.notify(c)
    <-c                          // reported: a channel receive while holding App
}
```

### What counts as a wait

A list, not an inference:

*   a channel send or receive, and a `select` **without** a default case,
*   `time.Sleep`, `sync.WaitGroup.Wait`, `sync.Cond.Wait`,
*   `net/http` round trips: the `http.Client` methods, the package level
    helpers and `RoundTrip`,
*   `os/user` lookups, which go through the name service switch and from there
    to whatever directory it is configured against,
*   the kubernetes client calls that talk to the API server: the verbs of the
    generated clients in `k8s.io/client-go/kubernetes` and the REST client
    underneath them.

A `select` WITH a default never waits, and the distinction is taken from the
SSA form rather than guessed at from the syntax. That is what makes the
non-blocking dispatch idiom usable under a lock, and reporting it would condemn
the very shape that makes dispatching safe:

```go
select {
case events <- e:            // not reported
default:
    go retry(e)
}
```

The kubernetes listers are deliberately not on the list. A lister reads the
informer's local store and does no I/O, so reporting one would condemn every
cache lookup under a lock in a code base built on informers.

Taking a lock is not on the list either. Every acquisition waits in principle,
so including them would report all nesting and say nothing; that nesting is
`lockorder`'s subject, with a declared order to judge it by.

### Scope

*   The bit travels through the summaries `lockorder` builds, so a wait three
    of your own calls away is found.
*   How far it travels depends on whether the wait can be NAMED. A call on the
    list above, or a function declared `+blocking`, is a statement about that
    callee: it holds wherever the callee is called from, so it travels without
    limit, and a caller two packages away from the declaration is still
    reported. A wait inferred from a bare channel operation travels only as far
    as this analysis can see the whole picture: within the package, and within
    the module when the analyser was given packages to work on.
*   That distinction is what keeps a dependency's internals out. The standard
    library is full of channel receives no caller can see or avoid — formatting
    a string reaches one a few layers down — and carrying those out of the
    package they occur in makes every function that logs a line blocking. On one
    real code base the difference is 23 diagnostics against 453.
*   Both modes agree on what they report about a NAMED wait, which matters
    because a `go vet` driven with a file list rather than with packages has no
    module to compare against: the unit is `command-line-arguments` and belongs
    to nothing. Only the inferred waits are narrowed there, to the package.
*   A wait reached only through an interface, or through a function-valued
    field, has no callee to consult. `+blocking` on the implementation is the
    answer, as it is for `checklocks`.

### Annotations

*   `+blocking`: on a function, declares that it waits. Use it for a wait this
    analysis cannot see: behind an indirect call, or inside a dependency.
*   `+lockblockingignore`: on a function, drops every diagnostic in its body
    and at its call sites; on a line, drops the diagnostics on that line.
*   `+lockblockingfail`: states that a line must be reported, for the test
    corpus, exactly as `+checklocksfail` does for `checklocks`.

## Grouped reports

One callee that acquires a class out of order is one defect, however many call
sites reach it. Reported per site, a single mistake in a widely used helper
fills a page.

`-lockorder.group` and `-lockblocking.group` collapse the sites of one defect
into a single diagnostic, keyed by what is acquired, through which callee, and
what was held. The first site carries the message; the rest are attached to it
as related positions, which `go vet` prints beneath it:

```
queue.go:2554:83: acquiring Application (via injectedTakesApp) while holding Queue: ...
queue.go:2555:83: 	and reached from here
queue.go:2556:83: 	and reached from here
```

**Be clear about what this does and does not save.** The output is not
shorter — each site is still a line, because each site still has a position
worth printing. What changes is the number of *diagnostics*: five findings
become one finding with five sites, which is what the reader needs in order to
know there is one thing to fix. A tool that counts diagnostics sees the
difference; a tool that counts lines does not.

Both flags are off by default. Grouping changes the shape of the output, and a
project may be reading it with something other than its eyes.

### Suppression is unchanged

Grouping decides how diagnostics are printed, never whether they are produced,
and every site is still considered individually:

*   an ignore applies to the site it is written on, and to nothing else;
*   a group is reported if any of its sites is unsuppressed, and lists only the
    sites that are;
*   a group all of whose sites are suppressed is silent.

A `+lockorderfail` expectation is likewise met per site, whether that site ends
up carrying the message or attached to it.

## Development

```sh
go build ./...
go test ./...
```

`go test` builds the vettool and runs each analyzer over its own corpus:
`test/` for `checklocks`, `test/lockstringer/` for `lockstringer`,
`test/lockorder/` and `test/lockhier/` for `lockorder`, and
`test/lockblocking/` for `lockblocking`.
The corpora are self-checking: a case that must be reported carries
`+checklocksfail` or the analyzer's own equivalent, and the analyzer reports a
missing expected failure when an annotated line produces none. Any output at
all fails the test, so both unexpected diagnostics and expectations that
stopped holding are caught.

Each analyzer is run over its corpus with the others disabled. Most cases are
violations in more than one analyzer's terms, and an expectation can only be
stated once per line.

## License

Apache License 2.0; see `LICENSE` and `NOTICE`.
