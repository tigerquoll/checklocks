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

Anonymous functions and closures cannot be annotated.

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

## Development

```sh
go build ./...
go test ./...
```

`go test` builds the vettool and runs each analyzer over its own corpus:
`test/` for `checklocks`, `test/lockstringer/` for `lockstringer`. The corpora
are self-checking: a case that must be reported carries `+checklocksfail` or
`+lockstringerfail`, and the analyzer reports a missing expected failure when
an annotated line produces none. Any output at all fails the test, so both
unexpected diagnostics and expectations that stopped holding are caught.

Each analyzer is run over its corpus with the others disabled. Most cases are
violations in more than one analyzer's terms, and an expectation can only be
stated once per line.

## License

Apache License 2.0; see `LICENSE` and `NOTICE`.
