# Okra

Okra is a small expression/DSL engine written in Go. It parses and evaluates expressions against a root data object using reflection.

## Quick Start

```go
package main

import (
    "fmt"

    "okra/core"
)

type User struct {
    Name string
    Age  int
}

func main() {
    e := core.NewEngine()
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
| String | `'...'` (single quotes) | `string` | `'hi\n' + 1` | `"hi\n1"` |
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
| `struct` / `*struct` | `user.Method(args...)` | Method call via reflection | `user.SayHi('hi')` |
| `struct` / `*struct` | `user.Method` | Getter-style: only if method has **0 inputs** and **>=1 outputs** | `user.MultiReturn` |
| `map[K]V` | `m.key` or `m[key]` | `key` is a string; if `K` is numeric, Okra tries to parse numeric keys from strings | `scores.1`, `scores[1]` |
| `[]T` / `[N]T` | `arr.0` or `arr[0]` | Index access; invalid/out-of-range returns `nil` | `nums.1`, `nums[1]` |
| pointers | auto-dereference | Nil pointers usually yield `nil` (except some special cases like `len`) | `ptr.Field`, `ptr.Method()` |

Field promotion follows Go's rules: a directly-declared field shadows a promoted one of the same name (reach the shadowed field explicitly, e.g. `user.Base.Name`). Only **exported** embedded structs are traversed — reflecting through an unexported field would panic — and a promoted field reached through a `nil` embedded pointer resolves to `nil` (or errors in strict mode).

### About `arr[0]`

Okra supports bracket indexing like `arr[0]` and it also supports **index expressions**.

- Examples: `arr[i]`, `arr[1+2]`, `matrix[row][col]`, `scores[0+1]`
- Indexing works for slices/arrays and maps (via reflection)

## Built-in Functions

| Name | Signature / return | Supported inputs | Example | Example result |
|---|---|---|---|---|
| `len` | `len(x) -> int64`; `len() -> int64(0)` | `slice` / `array` / `string` / `map` (pointers are dereferenced; other types return `int64(0)`) | `len(tags)`, `len()` | `int64(2)`, `int64(0)` |
| `now` | `now() -> int64` | none | `now()` | Unix seconds |
| `contains` | `contains(s, sub) -> bool` | strings (non-strings coerced via `fmt.Sprint`) | `contains('hello', 'ell')` | `true` |
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
e := core.NewEngine()

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

## List Literals and the `in` Operator

List literals use square brackets and can hold any expressions: `[1, 2, 3]`, `['a', 'b']`, `[]`. They evaluate to `[]any`.

The `in` operator (and its negation `not in`) tests membership and never panics. It binds at the comparison precedence tier, so `1 + 1 in [2, 3]` parses as `(1 + 1) in [2, 3]`.

| Left / Right | Meaning | Example | Example result |
|---|---|---|---|
| `x in slice`/`array` | any element equals `x` (same equality as `==`) | `2 in [1, 2, 3]` | `true` |
| `key in map` | the map contains that key | `'a' in scores` | `true` |
| `sub in string` | substring test | `'ell' in 'hello'` | `true` |
| `x not in y` | negation of the above | `4 not in [1, 2, 3]` | `true` |
| other | error | `1 in 2` | error |

```okra
status in ['active', 'trial'] ? 1 : 0
```

## Operators and Type Coercion

Okra uses three internal coercions:

- `toInt64`: supports all signed/unsigned integer kinds
- `toFloat`: supports integers, floats, and strings that parse via `ParseFloat`
- `toBool`: supports `bool`, strings (`""` and any case of `"false"` are false), otherwise `toFloat(v) != 0`

### Arithmetic: `+ - * / %`

| Operator | Supported types | Rule (simplified) | Example | Example result |
|---|---|---|---|---|
| `+` | number + number | integer path if both are int-like; otherwise float path | `1 + 2`, `1.5 + 2` | `int64(3)`, `3.5` |
| `+` | string + any | if LHS is `string`, concatenates `fmt.Sprint(rhs)` | `'res:' + 10` | `"res:10"` |
| `- * /` | numbers | integer path if possible; otherwise float path | `10 - 3`, `2.0 * 3.5` | `int64(7)`, `7.0` |
| `/` | numbers | division by zero returns `ErrDivByZero` | `10 / 0` | error |
| `%` | integers | modulo by zero returns `ErrModByZero`; float modulo returns `ErrFloatModulo` | `10 % 3`, `1.2 % 2.0` | `int64(1)`, error |

If either operand cannot be coerced to a number (and it is not string concatenation), arithmetic returns an error rather than silently yielding `0`.

### Comparison: `> < >= <=`

| Operator | Supported types | Rule | Example | Example result |
|---|---|---|---|---|
| `> < >= <=` | two strings, or both `toFloat`-convertible | two strings compare lexically; otherwise numeric; else error | `'a' < 'b'`, `10.5 > 10` | `true`, `true` |

### Equality: `== !=`

| Operator | Supported types | Rule | Example | Example result |
|---|---|---|---|---|
| `==` | any | `DeepEqual` first; else numeric compare when **both sides are numbers**; else `false` | `10 == 10.0`, `1 == '1'` | `true`, `false` |
| `!=` | any | negation of `==` | `10 != 10.0`, `'1' != 1` | `false`, `true` |

A string is never considered numerically equal to a number (`1 == '1'` is `false`), though two numbers of different kinds still compare by value (`10 == 10.0` is `true`).

### Logical: `&& ||` (short-circuit)

| Operator | Rule | Example | Example result |
|---|---|---|---|
| `&&` | if LHS is false, RHS is not evaluated | `false && (1/0)` | `false` (no error) |
| `||` | if LHS is true, RHS is not evaluated | `true || (1/0)` | `true` (no error) |

### Unary: `! - ~`

| Operator | Supported types | Rule | Example | Example result |
|---|---|---|---|---|
| `!x` | any | `!toBool(x)` | `!true` | `false` |
| `-x` | numbers | supports int/float; otherwise error | `-1`, `-1.5` | `int64(-1)`, `-1.5` |
| `~x` | integers | bitwise not on `int64` | `~0` | `int64(-1)` |

### Bitwise: `& | ^ << >>`

| Operator | Supported types | Rule | Example | Example result |
|---|---|---|---|---|
| `& | ^` | integers | both sides must be int-like | `5 & 3`, `5 ^ 1` | `int64(1)`, `int64(4)` |
| `<< >>` | integers | shift count must be >= 0 | `1 << 3`, `1 << -1` | `int64(8)`, error |

## Ternary Operator: `cond ? then : else`

| Syntax | Rule | Example | Example result |
|---|---|---|---|
| `a ? b : c` | condition uses `toBool`; evaluates only one branch (short-circuit) | `true ? 1 : 2` | `int64(1)` |

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

A `Program` always reads the Engine's latest registered functions, so `RegisterFunc` still takes effect after `Compile`. At compile time, sub-expressions built entirely from literals are **constant-folded** (e.g. `1 + 2 * 3` becomes the literal `7`), so they are not recomputed on every `Eval`. A subtree that errors when folded (like `1 / 0`) is left intact so the error still surfaces at eval time.

### Inspecting a Program

- `prog.Vars() []string` — the distinct **root** variable identifiers the program reads (the base of each access chain, so `user.Age` reports `user`, not the full path `user.Age`). Useful for validating which top-level objects a rule needs, or building dependency indexes, before running it.
- `prog.Funcs() []string` — the distinct function and method names the program calls (bare calls like `contains(...)` and method calls like `user.Save()`).

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
- Unexported struct fields are never exposed (reading them via reflection would panic), so `obj.unexportedField` simply resolves to `nil`.

### Sentinel Errors

Several errors are exported so callers can match them with `errors.Is`, even though they are wrapped with context:

- `ErrDivByZero`, `ErrModByZero`, `ErrFloatModulo`
- `ErrNegativeShift`
- `ErrNotFound` (unknown function or method)
- `ErrUnknownField` (strict-mode missing field/key/index)
- `ErrMethodDenied` (blocked by the method filter)

### Strict Mode

By default a missing field, absent map key, out-of-range index, or member access on `nil` resolves to `nil` (so a misspelled field is silent). Enable strict mode to turn these into errors instead:

```go
e.SetStrict(true)
_, err := e.Eval("user.Naem", data) // errors.Is(err, ErrUnknownField)
```

### Restricting Methods

Because member/method access uses reflection, an expression can call any exported method on the data object. To lock this down, install a filter — names for which it returns `false` are denied (`ErrMethodDenied`). This gates **reflected** calls into the data object: explicit method calls (`user.Save()`) and getter-style access alike. It does **not** gate built-in or `RegisterFunc` functions, nor the `x.len()` shortcut — control those by not registering them. `nil` (the default) allows all.

```go
allowed := map[string]bool{"FullName": true, "Age": true}
e.SetMethodFilter(func(name string) bool { return allowed[name] })
```

### Nesting Depth

Parser recursion (and therefore AST and evaluation depth) is bounded to keep deeply nested input from exhausting the stack. The default limit is `MaxStackDepth` (256); override it per Engine with `e.SetMaxNestingDepth(n)`. Exceeding it returns an `expression nesting too deep` error.

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
