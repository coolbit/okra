# Okra

Okra is a small expression/DSL engine written in Go. It parses and evaluates expressions against a root data object using reflection.

## Quick Start

```go
package main

import (
    "fmt"

    "github.com/coolbit/okra"
)

type User struct {
    Name string
    Age  int
}

func main() {
    e := okra.NewEngine()
    data := map[string]any{
        "user": &User{Name: "Alice", Age: 25},
        "arr":  []int{10, 20, 30},
    }

    v, err := e.Eval("user.Age >= 18 ? user.Name : 'minor'", data)
    if err != nil {
        fmt.Println("error:", err)
        return
    }
    fmt.Println(v)
}
```

## Supported Literals

| Kind | DSL syntax | Runtime type (Go) | Example | Example result |
|---|---|---|---|---|
| Boolean | `true` / `false` | `bool` | `true && false` | `false` |
| String | `'...'` (single quotes) | `string` | `'hi' + ' there'` | `"hi there"` |
| Integer | `123`, `0xFF`, `1_000` | `int64` | `123 + 1` | `int64(124)` |
| Float | `1.25`, `1e3`, `1.5e2` | `float64` | `1.25 * 2` | `float64(2.5)` |

### Numeric Formats

Integers accept decimal, hexadecimal (`0xFF`), and underscore digit separators (`1_000`). Floats accept a decimal point and/or an exponent (`1e3`, `1.5e2`). A malformed number (e.g. `1.2.3`) is a **parse error** — it is never silently truncated to `0`.

### String Escapes

String literals use single quotes, and they support **Go-style escape sequences** (similar to Go string literals):

- `\n`, `\r`, `\t`
- `\\`, `\'`
- `\xNN` (hex byte)
- `\uNNNN` / `\UNNNNNNNN` (Unicode)
- octal escapes like `\101`

Examples:

```okra
'a\nb'
```

```okra
'it\'s ok'
```

```okra
'\u0041'  // "A"
```

## Accessing Data (`data any`)

The engine evaluates expressions against a root data object (`data any`). Member access and indexing are implemented with reflection.

Identifiers may contain Unicode letters, so non-ASCII field and map-key names work (e.g. `user.名前`, `число + 8`).

| Input type | Access | Notes | Example |
|---|---|---|---|
| `struct` / `*struct` | `user.Field` | Field access, including `okra` / `json` tag names; fields promoted from **exported embedded** structs are reachable too | `user.Name`, `user.name_tag`, `user.EmbeddedID` |
| `struct` / `*struct` | `user.Method(args...)` | Method call via reflection; arguments convert **losslessly only** — an int that doesn't fit the parameter's range, a fractional float passed to an integer parameter, or a number passed to a string parameter is an error, never a silent wrap/truncate/rune-string | `user.SayHi('hi')` |
| `struct` / `*struct` | `user.Method` | Getter-style: only if method has **0 inputs** and **>=1 outputs**; a trailing `error` return surfaces exactly as it would for `user.Method()` | `user.MultiReturn` |
| `map[K]V` | `m.key` or `m[key]` | `key` is a string; if `K` is numeric, Okra tries to parse numeric keys from strings | `scores.1`, `scores[1]` |
| `[]T` / `[N]T` | `arr.0` or `arr[0]` | Index access; invalid/out-of-range is an error in strict mode (the default), else `nil` | `nums.1`, `nums[1]` |
| pointers | auto-dereference | Nil pointers are an error in strict mode (the default), else `nil` (except special cases like `len`) | `ptr.Field`, `ptr.Method()` |

Field promotion follows Go's rules: a directly-declared field shadows a promoted one of the same name (reach the shadowed field explicitly, e.g. `user.Base.Name`). Only **exported** embedded structs are traversed — reflecting through an unexported field would panic — and a promoted field reached through a `nil` embedded pointer is an error in strict mode (the default), else `nil`.

### About `arr[0]`

Okra supports bracket indexing like `arr[0]` and it also supports **index expressions**.

- Examples: `arr[i]`, `arr[1+2]`, `matrix[row][col]`, `scores[0+1]`
- Indexing works for slices/arrays and maps (via reflection)

## Built-in Functions

| Name | Signature / return | Supported inputs | Example | Example result |
|---|---|---|---|---|
| `len` | `len(x) -> int64` | `slice` / `array` / `string` / `map` (pointers are dereferenced); any other type — or no argument — is an **error** | `len(tags)`, `len(5)` | `int64(2)`, error |
| `now` | `now() -> int64` | none | `now()` | Unix seconds |
| `date` | `date(s) -> time.Time` | a string in RFC3339, `'2006-01-02 15:04:05'`, or `'2006-01-02'`; anything else errors. The explicit way to write a time literal — a bare string never implicitly becomes a time | `date('2026-01-01')` | `time.Time` |
| `unix` | `unix(t) -> int64` | `time.Time` / non-nil `*time.Time`; the explicit bridge from times to numbers | `now() - unix(order.PaidAt) < 3600` | Unix seconds |
| `has` | `has(obj, name) -> bool` | `name` is a string field/map-key/index name; resolves fields, map keys, indexes (never methods) without a strict-mode error. **Structural**: true if the member is there, even when its value is nil | `has(user, 'Coupon')` | `true` / `false` |
| `get` | `get(obj, name, default) -> any` | as `has`, but **value-level**: a missing member *or* a nil value both yield `default`, so `get(...)` is always safe to feed into an operation | `get(scores, 'math', 0)` | value or `default` |
| `contains` | `contains(s, sub) -> bool` | strings only (no coercion) | `contains('hello', 'ell')` | `true` |
| `startsWith` | `startsWith(s, prefix) -> bool` | strings | `startsWith('hello', 'he')` | `true` |
| `endsWith` | `endsWith(s, suffix) -> bool` | strings | `endsWith('hello', 'lo')` | `true` |
| `lower` | `lower(s) -> string` | strings | `lower('HeLLo')` | `"hello"` |
| `upper` | `upper(s) -> string` | strings | `upper('HeLLo')` | `"HELLO"` |
| `trim` | `trim(s) -> string` | strings (trims surrounding whitespace) | `trim('  hi  ')` | `"hi"` |

Function names are case-insensitive (`startsWith`, `startswith`, and `STARTSWITH` all resolve to the same function).

## Custom Functions (`RegisterFunc`)

You can extend (or override) functions on a **single Engine instance**:

- Registration is not global.
- It only affects the current `Engine`.

```go
e := okra.NewEngine()

_ = e.RegisterFunc("add", func(args []any) (any, error) {
    if len(args) != 2 {
        return nil, fmt.Errorf("expected 2 args")
    }
    a, _ := args[0].(int64)
    b, _ := args[1].(int64)
    return a + b, nil
})

v, err := e.Eval("add(1, 2)", nil)
// v == int64(3)
```

Registration is per-`Engine` and only affects `Program`s compiled **after** the call
(see [Compiling Once](#compiling-once-evaluating-many-times) — a `Program` snapshots
its functions at `Compile` time).

## Lazy Functions / Macros (`RegisterMacro`)

`RegisterFunc` receives its arguments already evaluated. A **macro** instead receives
its arguments **un-evaluated** (as `[]Expr`) plus the current `Context`, so it can
choose whether and how to evaluate them — including re-evaluating a predicate once per
element of a collection. This is the extension point for collection operations
(`any` / `all` / `filter` / `map`); the core language deliberately ships none of them,
nor any element placeholder, so you build exactly the semantics you want.

```go
type MacroFunc func(ctx okra.Context, args []okra.Expr) (any, error)
```

A macro is resolved **before** a plain function of the same name and before the
data-method fallback. Example: an `any(coll, predicate)` that evaluates `predicate`
with each element swapped in as the root data (so a bare field name refers to the
element — an element-scoping convention chosen by *this* macro, not the language):

```go
e.RegisterMacro("any", func(ctx okra.Context, args []okra.Expr) (any, error) {
    coll, err := args[0].Eval(ctx)
    if err != nil {
        return nil, err
    }
    rv := reflect.ValueOf(coll)
    for i := 0; i < rv.Len(); i++ {
        child := ctx
        child.Data = rv.Index(i).Interface() // re-root at the element
        v, err := args[1].Eval(child)
        if err != nil {
            return nil, err
        }
        if b, ok := v.(bool); ok && b {
            return true, nil
        }
    }
    return false, nil
})

// any(orders, price > 100)  ->  true if some order's price exceeds 100
```

Like `RegisterFunc`, `RegisterMacro` is per-`Engine` and only affects `Program`s
compiled after the call. Because a macro exposes `Expr`/`Context`, treat those types as
part of your integration's stable surface.

## List Literals and the `in` Operator

List literals use square brackets and can hold any expressions: `[1, 2, 3]`, `['a', 'b']`, `[]`. They evaluate to `[]any`.

The `in` operator (and its negation `not in`) tests membership and never panics. It binds at the comparison precedence tier, so `1 + 1 in [2, 3]` parses as `(1 + 1) in [2, 3]`.

| Left / Right | Meaning | Example | Example result |
|---|---|---|---|
| `x in slice`/`array` | any element equals `x` (same equality as `==`) | `2 in [1, 2, 3]` | `true` |
| `key in map` | the map contains that key | `'a' in scores` | `true` |
| `sub in string` | substring test | `'ell' in 'hello'` | `true` |
| `x not in y` | negation of the above | `4 not in [1, 2, 3]` | `true` |
| `x in nil` | error — using nil as a container | `1 in missing` | error |
| other | error | `1 in 2` | error |

```okra
status in ['active', 'trial'] ? 1 : 0
```

## Operators and Types

Okra is **strongly typed and fail-loud**: it never silently coerces one type into
another to make an operation "work". Mixing types that don't belong together is an
error, not a guessed value. This keeps a rule's meaning stable when the data's type
shifts (e.g. a value that was an `int` arrives as a `string`).

Numbers are the one unified family: all signed/unsigned integer kinds and both float
kinds interoperate (integer path when both operands are int-like, otherwise float
path). A **string is never treated as a number**, and **`nil` used in any operation is
an error** (see [nil](#nil)).

### Arithmetic: `+ - * / %`

| Operator | Rule | Example | Example result |
|---|---|---|---|
| `+` (numbers) | integer path if both int-like, else float path | `1 + 2`, `1.5 + 2` | `int64(3)`, `3.5` |
| `+` (strings) | **string + string** concatenates; any string/number mix is an error | `'a' + 'b'`, `'res:' + 10` | `"ab"`, **error** |
| `- * /` | numbers only | `10 - 3`, `2.0 * 3.5` | `int64(7)`, `7.0` |
| `/` | division by zero → `ErrDivByZero`; **integer / integer truncates** | `10 / 0`, `10 / 4` | error, `int64(2)` |
| `%` | integers; `ErrModByZero`, float modulo → `ErrFloatModulo` | `10 % 3`, `1.2 % 2.0` | `int64(1)`, error |

An operand that is not a number (a string, `nil`, …) makes arithmetic an error rather
than silently yielding `0`.

**Checked arithmetic**: integer `+ - * /` (and unary `-`) that would overflow `int64`
return `ErrIntOverflow` — never a silent two's-complement wrap. Bitwise operators
(`& | ^ << >>`) are exempt: wrapping is their intended semantics. A host `uint64`
larger than `MaxInt64` is not reinterpreted as negative; it is simply not a usable
number, so arithmetic/comparison with it errors.

Note that `10 / 4` is `int64(2)` (truncating integer division, as in Go/SQL/CEL); use
a float operand (`10 / 4.0`) for float division.

### Comparison: `> < >= <=`

Both operands must be the **same category** — two strings (compared lexically) or two
numbers (compared numerically). A string is never coerced to a number.

| Example | Result |
|---|---|
| `'a' < 'b'` | `true` (lexical) |
| `10.5 > 10` | `true` (numeric) |
| `'10' > 5` | **error** (string vs number) |

### Equality: `== !=`

| Operator | Rule | Example | Example result |
|---|---|---|---|
| `==` | `DeepEqual` first; else numeric compare when **both sides are numbers**; else `false` | `10 == 10.0`, `1 == '1'` | `true`, `false` |
| `!=` | negation of `==` | `10 != 10.0`, `'1' != 1` | `false`, `true` |

Equality is the one place mixed types are allowed without error: they simply compare
unequal. A string is never numerically equal to a number (`1 == '1'` is `false`),
while two numbers of different kinds compare by value (`10 == 10.0` is `true`).

**Lists compare element-wise with these same rules**, so scalar equality lifts into
them: `[1] == [1.0]` is `true`, `['1'] == [1]` is `false`, and length mismatch
short-circuits to `false`. Other composites (maps, structs) fall back to
`reflect.DeepEqual`. Self-referential data is safe (past a depth limit the comparison
falls back to `DeepEqual`, which handles cycles).

### Times (`time.Time`)

`time.Time` values (and non-nil `*time.Time`) are first-class in comparison and
equality:

- `> < >= <=` compare **chronologically**: `user.CreatedAt > date('2026-01-01')`.
- `==` / `!=` compare **by instant** (`time.Time.Equal`), so the same moment in two
  timezones is equal — `DeepEqual` would wrongly disagree.
- Everything else stays explicit and fail-loud: a time never mixes with numbers or
  strings implicitly. `created > '2026-01-01'` and `created + 1` are errors; the
  bridges are `date(s)` (string → time) and `unix(t)` (time → seconds).

### Logical: `&& ||` (short-circuit)

Both operands must be **`bool`** — there is no truthiness coercion. A non-bool operand
(string, number, `nil`) is a type error.

| Operator | Rule | Example | Example result |
|---|---|---|---|
| `&&` | if LHS is `false`, RHS is not evaluated | `false && (1/0)` | `false` (no error) |
| `||` | if LHS is `true`, RHS is not evaluated | `true || (1/0)` | `true` (no error) |
| — | non-bool operand | `'x' && true`, `1 || false` | error |

### Unary: `! - ~`

| Operator | Rule | Example | Example result |
|---|---|---|---|
| `!x` | requires `bool` (no coercion) | `!true`, `!'x'` | `false`, **error** |
| `-x` | numbers only | `-1`, `-1.5` | `int64(-1)`, `-1.5` |
| `~x` | integers | `~0` | `int64(-1)` |

### Bitwise: `& | ^ << >>`

| Operator | Rule | Example | Example result |
|---|---|---|---|
| `& | ^` | integers, both int-like | `5 & 3`, `5 ^ 1` | `int64(1)`, `int64(4)` |
| `<< >>` | integers, shift count `>= 0` | `1 << 3`, `1 << -1` | `int64(8)`, error |

## Ternary Operator: `cond ? then : else`

| Syntax | Rule | Example | Example result |
|---|---|---|---|
| `a ? b : c` | condition must be `bool` (no coercion); evaluates only one branch | `true ? 1 : 2` | `int64(1)` |

## Compiling Once, Evaluating Many Times

`Engine.Eval` parses on every call. To evaluate the same expression repeatedly (the common rules-engine pattern), compile it once into a `Program` and reuse it:

```go
prog, err := e.Compile("user.VIP && amount > 100 ? amount * 0.8 : amount")
if err != nil {
    // handle parse error
}
for _, order := range orders {
    v, err := prog.Eval(order) // no re-parsing
    _ = v
    _ = err
}
```

A `Program` is an **immutable, self-contained artifact**: the functions, macros,
strict flag, and method filter in effect at `Compile` time are snapshotted into it.
Changing the Engine afterwards (`RegisterFunc`, `SetStrict`, `SetMethodFilter`, …) does
**not** affect Programs already compiled — they stay reproducible and are safe to
evaluate concurrently. To pick up new configuration, recompile.

At compile time, sub-expressions built entirely from literals are **constant-folded**
(e.g. `1 + 2 * 3` becomes the literal `7`), so they are not recomputed on every `Eval`.
A subtree that errors when folded (like `1 / 0`) is left intact so the error still
surfaces at eval time.

### Cancellation and Deadlines (`EvalContext`)

`Program.EvalContext(ctx, data)` is `Eval` with cooperative cancellation: evaluation
counts its work in steps (one per AST node visited and per element scanned by `in`)
and polls `ctx` roughly every 1024 steps, returning `ctx.Err()` once the context is
cancelled or its deadline passes. Use it when rules may scan large collections inside
a latency budget — a goroutine cannot be killed from outside, so this cooperative
check is the only way to actually stop a long evaluation rather than merely abandon
it.

```go
ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
defer cancel()
v, err := prog.EvalContext(ctx, data) // errors.Is(err, context.DeadlineExceeded) on timeout
```

Macros that copy the `Context` (`child := ctx`) inherit the cancellation state, so
userland collection loops stay cancellable too. Plain `Eval` skips all of this and
pays no overhead.

### Inspecting a Program

- `prog.Vars() []string` — the distinct **root** variable identifiers the program reads (the base of each access chain, so `user.Age` reports `user`, not the full path `user.Age`). Useful for validating which top-level objects a rule needs, or building dependency indexes, before running it.
- `prog.Funcs() []string` — the distinct function and method names the program calls (bare calls like `contains(...)` and method calls like `user.Save()`).

**Macro caveat**: macro arguments are collected like any other expression. A macro
that re-roots its arguments — e.g. a collection predicate evaluated per element, so in
`any(orders, price > 100)` the `price` is element-relative — makes those identifiers
*not* root variables, yet `Vars()` still reports them. Static analysis is inherently
unreliable inside macro arguments; treat `Vars()`/`Funcs()` as exact only for
macro-free expressions.

```go
prog, _ := e.Compile("user.Age > 18 && contains(user.Name, 'a')")
prog.Vars()  // ["user"]
prog.Funcs() // ["contains"]
```

### Typed Results (`EvalTo`)

`EvalTo[T]` evaluates and converts the result to `T`. Converting a float result to an integer `T` **truncates toward zero** (e.g. `EvalTo[int]` of `1.9` yields `1`).

## Error Handling and Panic Safety

Okra never crashes the host application — every public entry point (`Eval`, `Compile`, `Program.Eval`, `EvalTo`, `ParseExpr`) recovers panics and returns them as errors.

- If evaluation hits a `panic` (from reflection/type conversions or inside user methods), Okra **recovers** and returns it as an `error`.
- Unexported struct fields are never exposed (reading them via reflection would panic). Like any unknown field, `obj.unexportedField` is an error in strict mode (the default) and resolves to `nil` when strict is off — it is never leaked either way.

### Sentinel Errors

Several errors are exported so callers can match them with `errors.Is`, even though they are wrapped with context:

- `ErrDivByZero`, `ErrModByZero`, `ErrFloatModulo`
- `ErrIntOverflow` (checked integer arithmetic)
- `ErrNegativeShift`
- `ErrNotFound` (unknown function or method)
- `ErrUnknownField` (strict-mode missing field/key/index)
- `ErrMethodDenied` (blocked by the method filter)

### Strict Mode

Strict mode is **on by default**. A missing struct field, absent map key,
out-of-range index, or member/method access on `nil` is an **error**
(`ErrUnknownField`), so a misspelled field fails loudly on the first evaluation rather
than silently resolving to `nil` and steering the rule down the wrong branch:

```go
e := okra.NewEngine()               // strict by default
_, err := e.Eval("user.Naem", data) // errors.Is(err, ErrUnknownField)
```

Express a genuinely optional member explicitly with [`has` / `get`](#built-in-functions)
instead of relying on silent `nil`:

```okra
has(user, 'Coupon') ? user.Coupon.Code : 'none'
get(scores, 'bonus', 0)
```

To restore the old lenient behavior (missing → `nil`), opt out per Engine:

```go
e.SetStrict(false)
```

### nil

There is **no `null` literal** in the language — you cannot write one, and the type
system has no null. But `nil` values do arrive from the host: a map that holds `nil`, a
method that returns `nil`, or `get(obj, name, default)`. Okra's rule is **nil may exist
but not be *used***:

- A `nil` may be the **final result** of an expression (handed back to your Go caller,
  which can deal with it) and may be **consumed** by `has` / `get` (`get` turns a nil
  value into your default).
- A `nil` that enters any **operation** — arithmetic, comparison, concatenation, `in`,
  `len`, a `?:` / `&&` / `||` / `!` condition, or a further member access — is an
  **error**, never a silent `0` / `false`.
- The one exemption is **equality**: `==` / `!=` always have an answer, for nil and
  for mixed types alike (they simply compare unequal, and `nil == nil` is `true`).

This complements strict mode: *missing* is an error at access time; a present *nil
value* is fine to pass around, but *using* it in an operation is an error.

### Error messages name the failing sub-expression

An error born at an operator is annotated with that sub-expression's source form, once,
at its birthplace — so in a long rule you see *which* part failed:

```
(user.Age > user.Name): invalid comparison between int64 and string
```

Sentinel matching with `errors.Is` works through the annotation.

### Restricting Methods

Because member/method access uses reflection, an expression can call any exported method on the data object. To lock this down, install a filter — names for which it returns `false` are denied (`ErrMethodDenied`). This gates **reflected** calls into the data object: explicit method calls (`user.Save()`) and getter-style access alike. It does **not** gate built-in or `RegisterFunc` functions, nor the `x.len()` shortcut — control those by not registering them. `nil` (the default) allows all.

```go
allowed := map[string]bool{"FullName": true, "Age": true}
e.SetMethodFilter(func(name string) bool { return allowed[name] })
```

### Nesting Depth and Expression Size

Parser recursion (and therefore AST and evaluation depth) is bounded to keep deeply nested input from exhausting the stack. The default limit is `MaxStackDepth` (256); override it per Engine with `e.SetMaxNestingDepth(n)`. Exceeding it returns an `expression nesting too deep` error.

A single expression is also capped at `MaxExprLen` (1 MiB) so a pathologically huge input cannot tie up the lexer; longer input returns an `expression too long` error at compile time.

## Examples

```okra
user.Age >= 18 && (user.Name != '' ? true : false)
```

```okra
(scores.1 == 'Gold' || scores.2 == 'Silver') ? 1 << 3 : 0
```

```okra
len(tags) > 0 ? tags[0] : 'none'
```
