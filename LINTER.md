# Linter Guide for LLMs

> Practical reference for writing lint-clean Go code with this golangci-lint v2 configuration.
> 70+ linters are enabled. Every rule below will fire on your code. Follow this guide to get it right the first time.

> **⚠️ This is a base configuration.** Individual projects may override or disable specific rules.
> Run `golangci-lint config path` to discover the actual config file location, then read it before relying on the rules below.

---

## Table of Contents

- [1. Overview](#1-overview)
- [2. Critical Rules (Most Common Errors)](#2-critical-rules-most-common-errors)
  - [Error Handling](#error-handling)
  - [Security](#security)
  - [Code Quality \& Complexity](#code-quality--complexity)
  - [Formatting \& Style](#formatting--style)
  - [Testing](#testing)
  - [Concurrency](#concurrency)
  - [Performance](#performance)
  - [Modernization](#modernization)
  - [Slog / Structured Logging](#slog--structured-logging)
  - [SQL \& Database](#sql--database)
  - [Other Enabled Linters](#other-enabled-linters)
- [3. Quick Fix Reference](#3-quick-fix-reference)
- [4. Running the Linter](#4-running-the-linter)
- [5. nolint Guidelines](#5-nolint-guidelines)

---

## 1. Overview

This project uses a heavily customized **golangci-lint v2** configuration with strict defaults and ~70 enabled linters covering error handling, security, code quality, formatting, testing, performance, and modern Go idioms.

**Key principles:**

- All `default` linters are disabled; every linter is explicitly opted in.
- Lines must be **≤ 100 characters** (including tabs counted as 4 spaces).
- Functions must have **≤ 40 statements** (comments ignored).
- Cognitive complexity must be **< 10** per function.
- Control nesting must be **≤ 3 levels** deep.
- Functions may have **≤ 2 return values** and **≤ 4 arguments**.
- Struct tags must use **snake_case** for json/yaml/xml/toml/bson/avro/mapstructure and **UPPER_SNAKE_CASE** for env/envconfig.
- **No global slog functions** — always use a logger instance.
- Error returns from external packages **must be wrapped**.
- **No `nil, nil` returns** — return a sentinel error instead.
- All struct fields must be initialized (with exclusions for `http.Server`, `http.Client`, `http.Transport`, and types ending in `Opts`, `Options`, `Config`).

---

## 2. Critical Rules (Most Common Errors)

### Error Handling

#### errorlint — Enforce proper error wrapping and comparison

Always use `%w` in `fmt.Errorf` to wrap errors. Use `errors.Is` and `errors.As` instead of `==` and type assertions.

```/dev/null/errorlint.go#L1-1
// WRONG
if err == io.EOF { ... }
if e, ok := err.(*SomeError); ok { ... }
fmt.Errorf("failed: %v", err)

// CORRECT
if errors.Is(err, io.EOF) { ... }
var e *SomeError
if errors.As(err, &e) { ... }
fmt.Errorf("failed: %w", err)
```

**Note:** `errorf-multi` is disabled — do not use multiple `%w` verbs in a single `fmt.Errorf`. Use `errors.Join` instead.

#### wrapcheck — Wrap errors from external packages

Errors returned by functions from other packages must be wrapped with context using `fmt.Errorf`.

```/dev/null/wrapcheck.go#L1-1
// WRONG
func loadData() ([]byte, error) {
    return os.ReadFile("data.json")
}

// CORRECT
func loadData() ([]byte, error) {
    data, err := os.ReadFile("data.json")
    if err != nil {
        return nil, fmt.Errorf("load data from file: %w", err)
    }
    return data, nil
}
```

**Excluded from wrapping:** `errors.New`, `fmt.Errorf`, `.Errorf`, `.Wrap`, `.Wrapf`, `.WithMessage`, `.WithStack`, `errors.Join`, `errors.Unwrap`. Internal errors are not reported (`report-internal-errors: false`).

#### nilnil — Do not return (nil, nil)

Returning both values as nil from a function with signature `(T, error)` is forbidden. Return a sentinel error instead.

```/dev/null/nilnil.go#L1-1
// WRONG
func findUser(id string) (*User, error) {
    if id == "" {
        return nil, nil
    }
    return &User{}, nil
}

// CORRECT
var errUserNotFound = errors.New("user not found")

func findUser(id string) (*User, error) {
    if id == "" {
        return nil, errUserNotFound
    }
    return &User{}, nil
}
```

**Applies to types:** chan, func, interface, map, pointer, uintptr, unsafe.Pointer. `detect-opposite` is enabled, so returning a nil error with a zero-value non-nil type is also flagged.

#### nilnesserr / nilerr — Detect nil error checks and assignments

These catch bugs where you return an error when err is nil, or check `err != nil` and then return nil.

```/dev/null/nilnesserr.go#L1-1
// WRONG — returning error when err is nil
if err == nil {
    return err // nilnesserr: returned error is nil
}

// WRONG — returning nil when err is not nil
if err != nil {
    return nil // nilerr: return nil ignoring error
}
```

#### errcheck — Check all error returns

`check-blank: true` means `_ = mayFail()` is also flagged. `disable-default-exclusions: true` means even commonly ignored functions must be handled.

```/dev/null/errcheck.go#L1-1
// WRONG
_ = os.WriteFile("out.txt", data, 0o644)
rows.Next()

// CORRECT
if err := os.WriteFile("out.txt", data, 0o644); err != nil {
    return fmt.Errorf("write output: %w", err)
}

// Excluded from errcheck (no need to check):
// (*os.File).Close, (*sql.Rows).Close, (*sqlx.Rows).Close
// encoding/json.Marshal, bytes.Buffer/StringBuilder Write methods
// fmt.Print/Printf/Fprintln to stdout, stderr, *bytes.Buffer, *strings.Builder
// crypto/rand.Read, (hash.Hash).Write
```

#### noinlineerr — Multi-line error checking style

Forces the pattern `err := expr(); if err != nil {}` instead of inline error checking.

```/dev/null/noinlineerr.go#L1-1
// WRONG
if err := doSomething(); err != nil {
    return err
}

// CORRECT
err := doSomething()
if err != nil {
    return err
}
```

#### noctx — HTTP requests must have context

```/dev/null/noctx.go#L1-1
// WRONG
req, err := http.NewRequest("GET", url, nil)

// CORRECT
req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
```

#### rowserrcheck / sqlclosecheck — SQL hygiene

Always check `rows.Err()` after iterating and always close `sql.Rows`, `sql.Stmt`, and `sqlx` equivalents.

```/dev/null/sqlcheck.go#L1-1
// WRONG
rows, err := db.Queryx(query)
for rows.Next() { ... }

// CORRECT
rows, err := db.Queryx(query)
if err != nil {
    return fmt.Errorf("query: %w", err)
}
defer rows.Close()

for rows.Next() { ... }
if err := rows.Err(); err != nil {
    return fmt.Errorf("iterate rows: %w", err)
}
```

`sqlclosecheck` also enforces that `sql.Rows`, `sql.Stmt`, and `sqlx` types have a `defer *.Close()` call in the same function.

---

### Security

#### depguard — Blocked packages

The following packages are forbidden:

| Package | Reason |
|---------|--------|
| `crypto/md5` | Weak crypto algorithm |
| `crypto/sha1` | Weak crypto algorithm |
| `crypto/des` | Weak crypto algorithm |
| `crypto/rc4` | Weak crypto algorithm |
| `golang.org/x/crypto/md4` | Weak crypto algorithm |
| `golang.org/x/crypto/ripemd160` | Weak crypto algorithm |
| `math/rand` | Use `crypto/rand` |
| `math/rand/v2` | Use `crypto/rand` |
| `github.com/golang/mock` | Archived; use `go.uber.org/mock/mockgen` |

Use `crypto/sha256`, `crypto/aes`, or `crypto/rand` instead of the blocked packages.

#### forbidigo — Forbidden identifiers

| Pattern | Reason |
|---------|--------|
| `fmt.Print`, `fmt.Printf`, `fmt.Println`, `print`, `println` | Direct stdout output is forbidden |
| `uuid.Nil` | Global variable that can be reassigned; use `uuid.UUID{}` |
| `sql.LevelDefault` | Use a concrete isolation level |
| `runtime.SetFinalizer` | Use `runtime.AddCleanup` (modern, safer) |
| `signal.Notify` | Prefer `signal.NotifyContext`; `context.Cause(ctx)` returns error containing signal name (go 1.26+) |

```/dev/null/forbidigo.go#L1-1
// WRONG
fmt.Println("hello")
var id = uuid.Nil
_, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelDefault})
runtime.SetFinalizer(obj, cleanup)
signal.Notify(ch, os.Interrupt)

// CORRECT
slog.InfoContext(ctx, "hello")
var id uuid.UUID // uuid.UUID{} is the zero value
_, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
runtime.AddCleanup(obj, cleanup)
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
defer stop()
// after ctx.Done(): cause := context.Cause(ctx) // error like "interrupt signal received"
```

#### gosec — Security rules (G101–G115, G201–G203, G301–G307, G402–G403)

Key rules to remember:

- **G101–G115**: Hardcoded credentials, weak crypto, poor random number generation, dangerous function calls, integer overflow, format string issues
- **G201–G203**: SQL injection — never build SQL queries with string concatenation
- **G301–G307**: File permission issues — default is `0o666` (configured via `G306: "0o666"`)
- **G402–G403**: Insecure TLS configuration, weak random source for crypto

```/dev/null/gosec.go#L1-1
// WRONG — hardcoded credentials (G101)
const apiKey = "sk-abc123"

// WRONG — SQL injection (G201-G203)
query := "SELECT * FROM users WHERE name = '" + name + "'"

// WRONG — bad file permissions (G306)
os.WriteFile("secret", data, 0o777)

// WRONG — insecure TLS (G402)
http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{
    InsecureSkipVerify: true,
}

// CORRECT
query := "SELECT * FROM users WHERE name = $1"
os.WriteFile("secret", data, 0o600) // G306 expects 0o666 but use stricter
```

---

### Code Quality & Complexity

#### gocognit — Cognitive complexity ≤ 10

Each function's cognitive complexity must stay under 10. Reduce complexity by:
- Extracting helper functions
- Using early returns (`revive:early-return` is also enabled)
- Replacing nested if-else with switch statements
- Using table-driven tests

#### funlen — ≤ 40 statements per function

Comments are ignored. `main()` and `Test*` functions are excluded from this check. Extract helpers for anything longer.

#### revive: add-constant — No magic numbers

With `max-lit-count: 1`, any literal that appears more than once must be extracted to a constant. `0` and `1` are always allowed. `""` is always allowed.

```/dev/null/addconstant.go#L1-1
// WRONG
func process(retries int) {
    for i := 0; i < retries; i++ {
        time.Sleep(3 * time.Second) // magic number 3
    }
}

// CORRECT
const retryDelay = 3 * time.Second

func process(retries int) {
    for i := 0; i < retries; i++ {
        time.Sleep(retryDelay)
    }
}
```

Many standard library functions are excluded from this rule (HTTP status codes, `time.Date`, slog functions, `fmt.Sprintf`, `strings.*`, etc.).

#### revive: argument-limit — Max 4 arguments

If you need more, use an options struct.

```/dev/null/arglimit.go#L1-1
// WRONG
func CreateUser(ctx context.Context, name, email, role, team, org string) (*User, error) {

// CORRECT
type CreateUserParams struct {
    Name  string
    Email string
    Role  string
    Team  string
    Org   string
}

func CreateUser(ctx context.Context, params CreateUserParams) (*User, error) {
```

#### revive: max-control-nesting — Max 3 levels

No more than 3 levels of nested `if`/`for`/`switch`. Flatten with early returns and extracted helpers.

#### revive: file-length-limit — Max 750 lines

Excludes comments and blank lines. Split large files into smaller ones.

#### revive: function-result-limit — Max 2 return values

Never return 3+ values. Use a result struct if you need more.

#### revive: enforce-map-style — Use `make()` for maps

```/dev/null/mapstyle.go#L1-1
// WRONG
m := map[string]int{}

// CORRECT
m := make(map[string]int)
```

#### revive: enforce-switch-style — Switches with `allowNoDefault`

Switch statements are enforced. A default case is not required, but if present, must follow the style rules.

#### revive: enforce-slice-style — Use `any` style (literals ok)

The value is `"any"`, meaning both `[]T{}` and `make([]T, 0)` are acceptable.

#### revive: enforce-repeated-arg-type-style — Short form for repeated types

```/dev/null/shorttype.go#L1-1
// WRONG
func foo(a int, b int, c string, d string)

// CORRECT
func foo(a, b int, c, d string)
```

This applies to both function arguments (`func-arg-style: short`) and return values (`func-ret-val-style: short`).

#### exhaustruct — Initialize all struct fields

All struct fields must be explicitly set in struct literals. Exceptions:

- `http.Server`, `http.Client`, `http.Transport`
- Any type ending in `Opts`, `Options`, or `Config`
- `allow-empty: true` — `{}` is allowed for the excluded types above

```/dev/null/exhaustruct.go#L1-1
// WRONG
srv := &http.Server{
    Addr: ":8080",
}

// CORRECT — http.Server is excluded, empty is ok
srv := &http.Server{
    Addr: ":8080",
}

// WRONG — non-excluded struct with missing fields
user := User{
    Name: "Alice",
    // missing Email, Age
}

// CORRECT
user := User{
    Name:  "Alice",
    Email: "alice@example.com",
    Age:   30,
}
```

#### gocritic — ~85 enabled checks

Some of the most commonly triggered:

| Check | What it catches |
|-------|----------------|
| `appendAssign` | `x = append(x, ...)` assigning to wrong variable |
| `appendCombine` | Multiple appends that can be combined |
| `badCall` | Incorrect use of built-in functions |
| `badLock` | Incorrect mutex usage (copy, double lock) |
| `boolExprSimplify` | `!a == true` → `!a` |
| `commentedOutCode` | Commented-out code (minLength: 0, catches everything) |
| `deferUnlambda` | `defer func() { f() }()` → `defer f()` |
| `dupArg` | Duplicate arguments `strings.Contains(s, s)` |
| `elseif` | Chain of if-else that should be switch |
| `exitAfterDefer` | `os.Exit` in a function with defers |
| `filepathJoin` | Incorrect `filepath.Join` usage |
| `hugeParam` | Large value-type params that should be pointers |
| `ifElseChain` | Long if/else if chain → use switch |
| `nestingReduce` | Suggests ways to reduce nesting |
| `nilValReturn` | Returns nil value inside conditional |
| `offBy1` | Off-by-one errors in loops/slices |
| `singleCaseSwitch` | Single-case switch should be if |
| `sloppyReassign` | Unnecessary reassignment |
| `stringConcatSimplify` | Simplify string concatenation |
| `typeAssertChain` | Chain of type assertions → use switch |
| `uncheckedInlineErr` | Inline error check without handling |
| `unnecessaryDefer` | Defer in function that doesn't need it |
| `yodaStyleExpr` | `nil == err` → `err == nil` |

#### ruleguard — Typed nil in composite literals (custom rule)

Plain `nil` is forbidden in keyed struct/map literal fields. Use typed nil to make the zero value explicit and type-safe.

**Rule:** `banUntypedNilInCompositeLit` (defined in `rules/keyword.rules.go`)

```/dev/null/typed_nil_bad.go#L1-4
// WRONG — plain nil in struct fields
svc := MyStruct{
    Service: nil,
    Storage: nil,
}
```

```/dev/null/typed_nil_good.go#L1-4
// CORRECT — typed nil
svc := MyStruct{
    Service: (*Client)(nil),
    Storage: any(nil),
}
```

Common typed nil forms:

| Field type | Typed nil |
|------------|-----------|
| `*T` (pointer) | `(*T)(nil)` |
| `any` / interface | `any(nil)` |
| `[]T` (slice) | `[]T(nil)` |
| `map[K]V` | `map[K]V(nil)` |
| `chan T` | `chan T(nil)` |

Each field with plain `nil` is reported separately (e.g., a struct with two nil fields produces two diagnostics). Return statements are not affected — `return nil` is allowed.

#### Other important revive rules enabled

| Rule | Description |
|------|-------------|
| `bare-return` | No bare returns in complex functions |
| `blank-imports` | No blank imports (except in test files) |
| `bool-literal-in-expr` | `x == true` → `x` |
| `call-to-gc` | No explicit `runtime.GC()` calls |
| `context-as-argument` | `context.Context` must be first param (except `*testing.T` can come before it) |
| `context-keys-type` | Context keys must be concrete types, not built-in |
| `datarace` | Potential data race detection |
| `deep-exit` | No `os.Exit` in library code |
| `defer` | Defer rules: no defer in loops, no method calls in defer, no recover without proper use |
| `dot-imports` | No dot imports (no allowed packages) |
| `early-return` | Use early returns with `allow-jump, preserve-scope` |
| `empty-block` | No empty blocks |
| `flag-parameter` | No bool parameters that control flow (extract into separate functions) |
| `get-return` | Getter methods shouldn't return error for simple field access |
| `identical-branches` | No identical if/else branches |
| `if-return` | Redundant else after return in if |
| `import-alias-naming` | Import alias naming rules |
| `import-shadowing` | No shadowing imports with local variables |
| `indent-error-flow` | Reduce nesting for error flows (`preserve-scope`) |
| `range-val-address` | Taking address of range value (copy) |
| `range-val-in-closure` | Range value captured in closure |
| `superfluous-else` | Redundant else after return/continue/break (`preserve-scope`) |
| `time-date` | Bad `time.Date` usage |
| `time-equal` | Use `time.Equal` instead of `==` for time comparison |
| `time-naming` | Time variable naming (e.g., `createdAt` not `created`) |
| `unchecked-type-assertion` | Type assertions must be checked with `ok` (excluded in test files) |
| `unconditional-recursion` | Infinite recursion detection |
| `unexported-naming` | Don't use all-caps for unexported names |
| `unexported-return` | Don't return unexported types from exported functions |
| `unnecessary-format` | `fmt.Sprintf("%s", s)` → just `s` |
| `unnecessary-stmt` | Unnecessary statements (empty else, redundant semicolons) |
| `unsecure-url-scheme` | HTTP URLs instead of HTTPS |
| `unused-parameter` | Function parameters must be used (prefix with `_` to ignore: `allow-regex: "^_"`) |
| `unused-receiver` | Method receivers must be used (prefix with `_`) |
| `useless-break` | Unnecessary break at end of case |
| `useless-fallthrough` | Unnecessary fallthrough |
| `var-declaration` | Variable declaration issues |
| `nested-structs` | Nested anonymous structs should be named types |
| `optimize-operands-order` | Optimize boolean expression evaluation order |
| `increment-decrement` | Use `i++` not `i += 1` |

#### mnd — Magic number detection

Checks for magic numbers in arguments, assignments, cases, conditions, operations, and returns. Files matching `magic1_*.go` are excluded. Functions from `math.*` and `http.StatusText` are excluded.

#### forcetypeassert — No unchecked type assertions

Every type assertion must use the `,ok` form unless in test files.

```/dev/null/forcetypeassert.go#L1-1
// WRONG
val := iface.(string)

// CORRECT
val, ok := iface.(string)
if !ok {
    return fmt.Errorf("expected string, got %T", iface)
}
```

#### gochecksumtype — Exhaustive interface implementation checks

`default-signifies-exhaustive: false` — the `default` case in type switches does not satisfy exhaustiveness. You must list all implementors.

---

### Formatting & Style

#### lll / golines — Line length ≤ 100 characters

Tab width is 4 spaces. Maximum line length is 100 characters. `golines` will auto-format long lines.

#### gofumpt — Strict formatting with extra rules

Stricter than `gofmt`. Extra rules enabled. Enforces:
- No extra blank lines
- Simplified expressions
- Consistent field alignment

#### gofmt — Rewrites

Two rewrite rules are configured:

| Pattern | Replacement |
|---------|-------------|
| `interface{}` | `any` |
| `uuid.Nil` | `(uuid.UUID{})` |

#### goimports / gci — Import ordering

Import groups (in order):

1. Standard library (`standard`)
2. Default packages
3. `github.com/*` packages
4. `golang.org/*` packages
5. Blank imports
6. Dot imports
7. Aliased imports
8. Local module imports

No inline or prefix comments on import lines.

#### tagliatelle — Struct tag casing

| Tag | Case |
|-----|------|
| `json` | `snake_case` |
| `yaml` | `snake_case` |
| `xml` | `snake_case` |
| `toml` | `snake_case` |
| `bson` | `snake_case` |
| `avro` | `snake_case` |
| `mapstructure` | `snake_case` |
| `env` | `UPPER_SNAKE_CASE` |
| `envconfig` | `UPPER_SNAKE_CASE` |

```/dev/null/tags.go#L1-1
// CORRECT
type Config struct {
    UserName    string `json:"user_name"    yaml:"user_name"    env:"USER_NAME"`
    AccessToken string `json:"access_token" yaml:"access_token" env:"ACCESS_TOKEN"`
}
```

#### tagalign — Tag alignment and ordering

Tags are aligned and sorted in this order:

1. `json`
2. `yaml`
3. `yml`
4. `toml`
5. `mapstructure`
6. `binding`
7. `validate`

```/dev/null/tagalign.go#L1-1
// CORRECT — tags ordered and aligned
type User struct {
    ID    string `json:"id"    validate:"required"`
    Name  string `json:"name"  validate:"required"`
    Email string `json:"email" validate:"required,email"`
}
```

#### wsl_v5 — Whitespace rules (When Spaces Linebreak)

Must add blank lines before:
- `assign`, `branch`, `decl`, `defer`, `for`, `go`, `if`, `label`
- `range`, `return`, `select`, `send`, `switch`, `type-switch`
- `append`, `assign-exclusive`, `assign-expr`
- `err` — blank line before error checks

```/dev/null/wsl.go#L1-1
// WRONG
func process() error {
    data, err := loadData()
    if err != nil {
        return err
    }
    result := transform(data)
    return nil
}

// CORRECT
func process() error {
    data, err := loadData()

    if err != nil {
        return err
    }

    result := transform(data)

    return nil
}
```

`branch-max-lines: 4` — if the last statement before a branch is ≤ 4 lines, a blank line is still required. `case-max-lines: -1` — no line limit for case blocks.

#### grouper — Require grouping for declarations

Single declarations must stand alone. Multiple declarations must be grouped:

```/dev/null/grouper.go#L1-1
// WRONG — single const not in a block
const x = 1

const y = 2

// CORRECT — grouped
const (
    x = 1
    y = 2
)

// WRONG — single import not in block
import "fmt"

// CORRECT
import (
    "fmt"
)
```

Rules: `const-require-single-const`, `const-require-grouping`, `import-require-single-import`, `import-require-grouping`, `type-require-single-type`, `type-require-grouping`, `var-require-single-var`, `var-require-grouping`.

#### enforce-repeated-arg-type-style — Short form

```/dev/null/shortargs.go#L1-1
// WRONG
func NewServer(host string, port int, handler http.Handler) *Server {

// CORRECT
func NewServer(host string, port int, handler http.Handler) *Server {
// Note: if host and port had same type: func NewServer(host, port string, ...)
```

---

### Testing

#### testifylint — Comprehensive testify rules

All major checkers are enabled:

| Checker | What it enforces |
|---------|-----------------|
| `bool-compare` | `assert.Equal(t, true, x)` → `assert.True(t, x)` |
| `compares` | Use `assert.Greater` etc. instead of `assert.True(t, a > b)` |
| `contains` | Use `assert.Contains` instead of manual check |
| `empty` | Use `assert.Empty`/`assert.NotEmpty` |
| `error-is-as` | Use `assert.ErrorIs`/`assert.ErrorAs` instead of `assert.Equal` on errors |
| `error-nil` | `assert.Nil(t, err)` → `assert.NoError(t, err)` |
| `expected-actual` | Expected value first: `assert.Equal(t, expected, actual)` |
| `float-compare` | Use `assert.InDelta` for floats |
| `formatter` | `require-f-funcs: true` — use `require.Equalf` not `require.Equal(t, msg, ...)` for formatted messages |
| `go-require` | No `require` in goroutines (not even in HTTP handlers) |
| `len` | `assert.Equal(t, 0, len(x))` → `assert.Empty(t, x)` |
| `negative-positive` | Use `assert.Negative`/`assert.Positive` |
| `nil-compare` | `assert.Equal(t, nil, x)` → `assert.Nil(t, x)` |
| `regexp` | Use `assert.Regexp` instead of manual regex match |
| `require-error` | In tests, use `require.NoError` for setup, `assert.Error` for assertions |
| `suite-broken-parallel` | Don't use `t.Parallel()` in test suite methods |
| `suite-dont-use-pkg` | Use `s.Equal` not `assert.Equal(s.T(), ...)` in suites |
| `suite-extra-assert-call` | No `assert` inside suite methods, use `s.Require()` |
| `suite-method-signature` | Suite methods must match expected signatures |
| `suite-subtest-run` | Use `s.Run()` for subtests in suites |
| `useless-assert` | No `assert.True(t, true)` or other tautological assertions |

**Expected-actual pattern:** `(^(exp(ected)?|want(ed)?)([A-Z]\w*)?$)|(^(\w*[a-z])?(Exp(ected)?|Want(ed)?)$)`

```/dev/null/testifylint.go#L1-1
// WRONG
assert.Equal(t, result, expected)
assert.Nil(t, err)
assert.Equal(t, true, isActive)
assert.True(t, a > b)

// CORRECT
assert.Equal(t, expected, result)
assert.NoError(t, err)
assert.True(t, isActive)
assert.Greater(t, a, b)
```

#### thelper — Test helper function naming

Test helpers must:
- Have `t *testing.T` (or `b *testing.B`, `tb testing.TB`, `f *testing.F`) as **first** parameter
- Have a name starting with `Test`/`Benchmark`/... is not required, but `first: true` and `name: true` are enabled

```/dev/null/thelper.go#L1-1
// WRONG
func setupDatabase(db *sql.DB, t *testing.T) {

// CORRECT
func setupDatabase(t *testing.T, db *sql.DB) {
    t.Helper()
    // ...
}
```

#### usetesting — Use testing package equivalents

| Instead of | Use |
|-----------|-----|
| `os.CreateTemp` | `t.TempDir()` + `os.Create` |
| `os.MkdirTemp` | `t.TempDir()` |
| `os.Setenv` | `t.Setenv` |
| `os.TempDir` | `t.TempDir()` |
| `os.Chdir` | `t.Chdir` |
| `context.Background()` in tests | Use `t.Context()` or the test's ctx |
| `context.TODO()` in tests | Use `t.Context()` or the test's ctx |

#### testableexamples — Examples must be testable

Example functions in `_test.go` files must be testable (have `// Output:` comment).

---

### Concurrency

#### fatcontext — No reassigning context in loops

```/dev/null/fatcontext.go#L1-1
// WRONG
func process(ctx context.Context, items []Item) {
    for _, item := range items {
        ctx = context.WithValue(ctx, "item", item) // fatcontext
        doWork(ctx)
    }
}

// CORRECT
func process(ctx context.Context, items []Item) {
    for _, item := range items {
        itemCtx := context.WithValue(ctx, "item", item)
        doWork(itemCtx)
    }
}
```

#### contextcheck — Context must propagate from outside

Do not use `context.Background()` or `context.TODO()` inside business logic. Accept `ctx` from the caller.

```/dev/null/contextcheck.go#L1-1
// WRONG
func fetchData(url string) ([]byte, error) {
    ctx := context.Background() // contextcheck
    req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
    // ...
}

// CORRECT
func fetchData(ctx context.Context, url string) ([]byte, error) {
    req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
    // ...
}
```

#### govet: copylocks — No copying mutexes

```/dev/null/copylocks.go#L1-1
// WRONG
var mu sync.Mutex
muCopy := mu // copylocks: copying a mutex

// WRONG
type Server struct {
    mu sync.Mutex
}
s := Server{}
s2 := s // copies the mutex

// CORRECT — always pass by pointer
type Server struct {
    mu sync.Mutex
}
s := &Server{}
```

#### govet: atomic — Correct atomic operations

```/dev/null/atomic.go#L1-1
// WRONG
atomic.AddInt64(&x, 1)
y = x // should use atomic.Load

// CORRECT
atomic.AddInt64(&x, 1)
y = atomic.LoadInt64(&x)
```

---

### Performance

#### perfsprint — Replace fmt.Sprintf with faster alternatives

```/dev/null/perfsprint.go#L1-1
// WRONG → CORRECT
fmt.Sprintf("%d", x)    → strconv.Itoa(x)
fmt.Sprintf("%s", s)    → s (just use the string)
fmt.Sprint(x)           → strconv.Itoa(x) (when x is int)
fmt.Sprintf("%t", b)    → strconv.FormatBool(b)
fmt.Sprintf("%x", x)    → fmt.Sprintf is ok for hex; use strconv.FormatInt(x, 16)
fmt.Errorf("err: %w", e) → errors.Join or fmt.Errorf is ok (errorf is disabled in perfsprint)
```

`concat-loop: true` and `loop-other-ops: true` — also flags `fmt.Sprintf` in loops.

#### prealloc — Preallocate slices

```/dev/null/prealloc.go#L1-1
// WRONG
var items []string
for _, v := range data {
    items = append(items, v.Name)
}

// CORRECT
items := make([]string, 0, len(data))
for _, v := range data {
    items = append(items, v.Name)
}
```

Enabled for simple cases, range loops, and for loops.

#### bodyclose — Close HTTP response body

```/dev/null/bodyclose.go#L1-1
// WRONG
resp, err := http.Get(url)
if err != nil { ... }
// resp.Body never closed

// CORRECT
resp, err := http.Get(url)
if err != nil { ... }
defer resp.Body.Close()
```

#### mirror — Identical code detection

Flags identical or near-identical code blocks that should be refactored into shared functions.

#### unconvert — Remove unnecessary type conversions

```/dev/null/unconvert.go#L1-1
// WRONG
x := int(5)
s := string("hello")

// CORRECT
x := 5
s := "hello"
```

---

### Modernization

#### modernize linter — Use modern Go idioms

**`stringsbuilder` is disabled** in the config. All other modernize checks are enabled:

| Check | What to use instead |
|-------|-------------------|
| `rangeint` | `for i := range N` instead of `for i := 0; i < N; i++` |
| `slicescontains` | `slices.Contains(s, x)` instead of manual loop |
| `slicessort` | `slices.Sort(s)` instead of `sort.Slice` |
| `stringscutprefix` | `strings.CutPrefix(s, prefix)` instead of `strings.HasPrefix` + trim |
| `stringscutsuffix` | `strings.CutSuffix(s, suffix)` instead of `strings.HasSuffix` + trim |
| `stringsindex` | Use modern string functions |
| `slicesindex` | Use `slices.Index` instead of manual loop |
| `slicesminmax` | Use `slices.Min`/`slices.Max` instead of manual loop |
| `sortfuncs` | Use `slices.SortFunc` with `cmp.Compare`/`cmp.Ordered` |
| `loop` | Replace loops with modern standard library functions |

```/dev/null/modernize.go#L1-1
// WRONG → CORRECT (modernize)
for i := 0; i < n; i++ { }      → for i := range n { }
sort.Slice(s, func(i, j int) bool { return s[i] < s[j] }) → slices.Sort(s)
for _, v := range s { if v == target { return true } } → slices.Contains(s, target)
if strings.HasPrefix(s, "pre") { s = s[len("pre"):] } → s, _ = strings.CutPrefix(s, "pre")
```

#### usestdlibvars — Use standard library constants

```/dev/null/stdlibvars.go#L1-1
// WRONG → CORRECT
http.MethodGet        (already correct, don't use "GET")
http.StatusOK         (don't use 200)
time.January          (don't use time.Month(1))
time.Monday           (don't use time.Weekday(1))
tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
crypto.SHA256         (don't use crypto.Hash(5))
```

---

### Slog / Structured Logging

#### sloglint — Strict slog usage

| Setting | Value | Meaning |
|---------|-------|---------|
| `no-global` | `all` | No `slog.Info()`, `slog.Error()`, etc. Always use a logger instance |
| `context` | `all` | Always pass context: `logger.InfoContext(ctx, ...)` not `logger.Info(...)` |
| `static-msg` | `true` | Message must be a string literal, not a variable |
| `msg-style` | `lowercased` | Message must start with lowercase |
| `key-naming-case` | `snake` | Keys must be `snake_case` |
| `forbidden-keys` | `time`, `level`, `msg`, `source` | Don't use these keys (reserved by slog) |
| `args-on-sep-lines` | `true` | Each key-value pair on its own line |
| `no-mixed-args` | `true` | Don't mix `slog.String()` and inline key-value pairs |
| `kv-only` | `false` | Both inline key-value and `slog.Attr` are allowed |
| `attr-only` | `false` | Both inline key-value and `slog.Attr` are allowed |
| `no-raw-keys` | `false` | Raw keys (string literals) are allowed |

```/dev/null/sloglint.go#L1-1
// WRONG
slog.Info("User logged in", "userID", id)
slog.ErrorContext(ctx, errorMessage, "user_id", id)
logger.InfoContext(ctx, "Failed",
    "UserId", id, "error", err,
)

// CORRECT
logger.InfoContext(ctx, "user logged in",
    "user_id", id,
)

logger.ErrorContext(ctx, "failed to process request",
    "user_id", id,
    "err", err,
)
```

#### loggercheck — Logger key types

For all loggers (slog, zap, klog, logr, kitlog):
- Keys must be string type (`require-string-key: true`)
- No printf-like patterns (`no-printf-like: true`)

---

### SQL & Database

#### unqueryvet — SQL builder validation

`check-sql-builders: true` — flags string concatenation in SQL queries.

Allowed patterns:
- `SELECT * FROM information_schema.*`
- `SELECT * FROM pg_catalog.*`
- `SELECT COUNT(*)`
- `SELECT MAX(*)`
- `SELECT MIN(*)`

Excluded in test files.

#### rowserrcheck — Check `sql.Rows.Err()`

Configured for `github.com/jmoiron/sqlx` package. Always check `rows.Err()` after iteration.

#### sqlclosecheck — Close SQL resources

Ensures `sql.Rows`, `sql.Stmt`, and `sqlx` equivalents are closed with `defer`.

---

### Other Enabled Linters

#### varnamelen — Variable name length

- `min-name-length: 3` — all variable names must be ≥ 3 characters (except `err`, `ctx`, `ok bool`, `db *sql.DB`, `wg sync.WaitGroup`, `mu sync.Mutex`, `r *http.Request`, `w http.ResponseWriter`, `w io.Writer`, `r io.Reader`, `i int`)
- `max-distance: 1` — if declaration and use are far apart, the name must be even more descriptive
- `check-return: true` — return value names also checked

#### ireturn — Acceptable return types

Interfaces returned from functions must be in the allow list:
- `anon` — anonymous interfaces
- `error` — the error interface
- `empty` — `interface{}` / `any`
- `stdlib` — standard library interfaces
- `Service$` — types ending in "Service"
- `Span$` — types ending in "Span"

#### interfacebloat — Interface method limit

Default max is 10 methods per interface.

#### iface — Interface checks

- `identical` — flag identical interfaces
- `unexported` — flag unexported methods on exported interfaces in certain cases

#### dupl — Duplicate code detection

Flags duplicate code blocks. Extract into shared functions.

#### goconst — Repeated strings/values

String literals or constants that appear 3+ times should be extracted to a named constant.

#### misspell — US English spelling

Locale is `US`. Also catches:
- `iff` → `if`
- `cancelation` → `cancellation`

#### nosprintfhostport — No host:port in Sprintf

Don't use `fmt.Sprintf("%s:%d", host, port)` for constructing URLs. Use `net.JoinHostPort` or `url.URL`.

#### canonicalheader — Canonical HTTP headers

HTTP header names must use canonical form (e.g., `"Content-Type"`, not `"content-type"`).

#### embeddedstructfieldcheck — Embedded struct field rules

Validates embedded struct field naming and usage.

#### errname / errchkjson — Error naming and JSON encoding

- `errname` — Error types must be named `ErrFoo` or `FooError`
- `errchkjson` — `report-no-exported: true`, `check-error-free-encoding: true` — JSON encoding must handle errors properly

#### durationcheck — Two durations multiplied

Detects `time.Duration * time.Duration` which produces `time.Duration²`, not `time.Duration`.

#### gocheckcompilerdirectives — Valid compiler directives

Ensures build tags and compiler directives are properly formatted.

#### iotamixing — Consistent iota usage

Don't mix iota and non-iota values in the same const block.

#### inamedparam — Named interface parameters

Interface method parameters must be named (not just types).

#### zerologlint — Zerolog method chaining

Validates proper zerolog event completion (`.Msg()` or `.Send()` must be called).

#### spancheck — OpenTelemetry span hygiene

Checks that spans are ended properly (`checks: [end]`).

#### promlinter — Prometheus metric naming

`strict: true` — metric names must follow Prometheus conventions.

---

## 3. Quick Fix Reference

| Error Pattern | Linter | Fix |
|---|---|---|
| `fmt.Errorf("...: %v", err)` | errorlint | Change `%v` to `%w` |
| `err == SomeErr` | errorlint | Use `errors.Is(err, SomeErr)` |
| `err.(*Type)` | errorlint | Use `errors.As(err, &target)` |
| `return nil, nil` | nilnil | Return a sentinel error: `return nil, errNotFound` |
| `if err != nil { return nil }` | nilerr | Return the error: `return fmt.Errorf("ctx: %w", err)` |
| Unchecked `rows.Err()` | rowserrcheck | Add `if err := rows.Err(); err != nil { ... }` after loop |
| Missing `defer resp.Body.Close()` | bodyclose | Add `defer resp.Body.Close()` after error check |
| Missing `defer rows.Close()` | sqlclosecheck | Add `defer rows.Close()` after query |
| `fmt.Sprintf("%d", n)` | perfsprint | Use `strconv.Itoa(n)` |
| `fmt.Sprintf("%s", s)` | perfsprint | Use `s` directly |
| `fmt.Sprintf("%t", b)` | perfsprint | Use `strconv.FormatBool(b)` |
| `var s []int` before append loop | prealloc | Use `s := make([]int, 0, len(src))` |
| `fmt.Println("msg")` | forbidigo | Use `logger.InfoContext(ctx, "msg")` |
| `uuid.Nil` | forbidigo, gofmt rewrite | Use `uuid.UUID{}` |
| `sql.LevelDefault` | forbidigo | Use `sql.LevelSerializable` or another level |
| `interface{}` | gofmt rewrite | Use `any` |
| `for i := 0; i < n; i++` | modernize | Use `for i := range n` |
| Manual loop to check if in slice | modernize | Use `slices.Contains(s, v)` |
| `sort.Slice(...)` | modernize | Use `slices.Sort(s)` |
| `strings.HasPrefix` + trim | modernize | Use `strings.CutPrefix` / `strings.CutSuffix` |
| `slog.Info(...)` | sloglint | Use `logger.InfoContext(ctx, ...)` with instance |
| Mixed slog args | sloglint | Use consistent key-value or attr style |
| Slog key `"UserId"` | sloglint | Use `snake_case`: `"user_id"` |
| Slog msg `"Failed"` | sloglint | Use lowercase: `"failed"` |
| Slog msg with variable | sloglint | Use string literal |
| Magic number `3` | mnd / revive:add-constant | Extract to named constant |
| `map[string]int{}` | revive:enforce-map-style | Use `make(map[string]int)` |
| Function with 5+ params | revive:argument-limit | Use options struct |
| 4 levels of nesting | revive:max-control-nesting | Extract helpers, use early return |
| File > 750 lines | revive:file-length-limit | Split into multiple files |
| 3 return values | revive:function-result-limit | Use result struct |
| `a int, b int` in func signature | revive:enforce-repeated-arg-type-style | Use `a, b int` |
| Line > 100 chars | lll / golines | Break into multiple lines |
| Missing blank line before `if` | wsl_v5 | Add blank line before `if`, `for`, `return`, etc. |
| Unchecked type assertion | forcetypeassert | Use `val, ok := x.(T)` |
| `for i := 0; i < n; i++` | modernize:rangeint | Use `for i := range n` |
| `_ = mayFail()` | errcheck | Handle the error |
| Inline `if err := f(); err != nil` | noinlineerr | Split: `err := f(); if err != nil { ... }` |
| `os.CreateTemp`/`os.MkdirTemp` in test | usetesting | Use `t.TempDir()` |
| `os.Setenv` in test | usetesting | Use `t.Setenv` |
| `context.Background()` in test | usetesting | Use `t.Context()` |
| Reassigning `ctx` in loop | fatcontext | Create new variable: `itemCtx := ...` |
| Copying mutex value | govet:copylocks | Pass by pointer |
| `assert.Nil(t, err)` | testifylint:error-nil | Use `assert.NoError(t, err)` |
| `assert.Equal(t, result, expected)` | testifylint:expected-actual | Swap: `assert.Equal(t, expected, result)` |
| `assert.Equal(t, true, x)` | testifylint:bool-compare | Use `assert.True(t, x)` |
| `assert.Equal(t, nil, x)` | testifylint:nil-compare | Use `assert.Nil(t, x)` |
| `assert.True(t, a > b)` | testifylint:compares | Use `assert.Greater(t, a, b)` |
| `assert.Equal(t, 0, len(x))` | testifylint:len | Use `assert.Empty(t, x)` |
| Unnamed test helper params | thelper | Make `*testing.T` first param, use `t.Helper()` |
| `crypto/md5` import | depguard | Use `crypto/sha256` |
| `math/rand` import | depguard | Use `crypto/rand` |
| Tag `json:"userName"` | tagliatelle | Use `json:"user_name"` |
| Tag `env:"username"` | tagliatelle | Use `env:"USER_NAME"` |
| Tags out of order | tagalign | Order: json, yaml, yml, toml, mapstructure, binding, validate |
| Duplicate code block | dupl | Extract shared function |
| Repeated string literal | goconst | Extract to named constant |
| `os.Exit()` in library | revive:deep-exit | Return error instead |
| `runtime.GC()` | revive:call-to-gc | Remove explicit GC call |
| HTTP (not HTTPS) URL | revive:unsecure-url-scheme | Use `https://` |
| Dot import `.` | revive:dot-imports | Use normal import |
| `http.StatusOK` as literal `200` | usestdlibvars | Use `http.StatusOK` constant |
| Function > 40 statements | funlen | Extract into smaller functions |
| Cognitive complexity ≥ 10 | gocognit | Simplify logic, extract helpers |
| Missing struct field in literal | exhaustruct | Initialize all fields or use `...Config` pattern |
| `defer func() { f() }()` | gocritic:deferUnlambda | Use `defer f()` |
| `if-else` chain | gocritic:ifElseChain | Use `switch` |
| Duplicate `append` calls | gocritic:appendCombine | Combine into single `append` |
| `x == true` | revive:bool-literal-in-expr | Use `x` |
| `!a == true` | gocritic:boolExprSimplify | Use `!a` |
| `const x = 1` alone | grouper | Wrap in `const ( ... )` block |
| `import "fmt"` alone | grouper | Wrap in `import ( ... )` block |
| `nil == err` | gocritic:yodaStyleExpr | Use `err == nil` |
| `time.Time` compared with `==` | revive:time-equal | Use `t1.Equal(t2)` |
| Short variable name | varnamelen | Use ≥ 3 char names (except `err`, `ctx`, etc.) |
| `fmt.Sprintf("%s", s)` | revive:unnecessary-format | Use `s` directly |
| `runtime.SetFinalizer` | forbidigo | Use `runtime.AddCleanup` |
| `signal.Notify(ch, ...)` | forbidigo | Use `signal.NotifyContext`; `context.Cause(ctx)` returns error with signal name |
| Plain `nil` in struct/map literal field | ruleguard (gocritic) | Use typed nil: `(*T)(nil)`, `any(nil)`, `[]T(nil)`, `map[K]V(nil)`, `chan T(nil)` |

---

## 4. Running the Linter

### Prerequisites

Ensure `golangci-lint` v2 is installed and available in your PATH.

### Commands

```bash
# Format and lint everything (recommended)
task lint

# Which is equivalent to:
golangci-lint fmt ./...
golangci-lint run ./...

# Run with auto-fix
golangci-lint run --fix ./...

# Run only formatters
golangci-lint fmt ./...

# Run on specific package
golangci-lint run ./pkg/...

# Run on specific file
golangci-lint run ./main.go

# Run with verbose output
golangci-lint run -v ./...

# Run with JSON output (for tooling)
golangci-lint run --out-format json ./...

# Run license header check
task license
# Which is equivalent to:
golangci-lint run --config .license.golangci.yml ./...

# Rebuild .golangci.yml from the commented version
task linter
# Which is equivalent to:
yq eval '... comments=""' .comments.golangci.yml > .golangci.yml
```

### Auto-fixable issues

Many formatters and some linters support `--fix`. The `golangci-lint fmt` command handles:
- `gofmt` / `gofumpt` / `goimports` / `gci` / `golines` — formatting and import ordering
- Tag alignment and sorting
- Line wrapping (> 100 chars)

Not all linter issues are auto-fixable. Error handling, complexity, and design issues require manual fixes.

### Timeouts

The configured timeout is **1 minute**. If linting times out, scope your run to specific packages.

---

## 5. nolint Guidelines

### Rules (from nolintlint)

1. **`require-explanation: true`** — Every `nolint` directive MUST have an explanation.
2. **`require-specific: true`** — You MUST specify the exact linter name. No bare `//nolint`.
3. **`allow-unused: false`** — Every `nolint` directive must be suppressing an actual issue. If the issue no longer exists, the `nolint` must be removed.

### Syntax

```/dev/null/nolint.go#L1-1
// WRONG — no explanation, no specific linter
//nolint
func badFunc() error { return nil, nil }

// WRONG — no explanation
//nolint:nilnil
func badFunc() error { return nil, nil }

// WRONG — no specific linter
//nolint // reason
func badFunc() error { return nil, nil }

// CORRECT
//nolint:nilnil // returning (nil, nil) is intentional for optional result pattern
func findItem() (*Item, error) {
    return nil, nil
}
```

### When to use nolint

Use `nolint` sparingly. Valid reasons include:

- **Generics/type assertions** where the `forcetypeassert` check is a false positive
- **Known false positives** in generated code
- **Deliberate design decisions** that conflict with a specific rule
- **Third-party API requirements** that force rule violations

### When NOT to use nolint

- Don't `nolint` to suppress complexity warnings — refactor instead
- Don't `nolint` to skip error handling — handle the error
- Don't `nolint` multiple linters on the same line — fix the code

### File-level exclusions

The config has built-in exclusions:

| Path | Excluded Linters |
|------|-----------------|
| `*.rules.go` | `unused` |
| `*_test.go` | `forcetypeassert`, `unqueryvet` |
| `func Test*` | `funlen` |
| `func main()` | `funlen` |

### Multi-line nolint

For suppressing an issue that spans multiple lines (e.g., a function):

```/dev/null/nolint_multiline.go#L1-1
//nolint:funlen // this is a test table that exceeds 40 statements by design
func TestLargeTable(t *testing.T) {
    // ...
}
```

### Common nolint patterns

```/dev/null/nolint_patterns.go#L1-1
// For type assertions in test files (forcetypeassert is excluded in _test.go)
//nolint:forcetypeassert // test-only type assertion, panic is acceptable

// For exhaustruct on types that genuinely don't need all fields
//nolint:exhaustruct // only configuring required fields, defaults are acceptable

// For complex test setup
//nolint:gocognit // test setup with table-driven cases requires nested blocks
```
