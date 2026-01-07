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
| Integer | `123` | `int64` | `123 + 1` | `int64(124)` |
| Float | `1.25` | `float64` | `1.25 * 2` | `float64(2.5)` |

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

| Input type | Access | Notes | Example |
|---|---|---|---|
| `struct` / `*struct` | `user.Field` | Field access, including `okra` / `json` tag names | `user.Name`, `user.name_tag` |
| `struct` / `*struct` | `user.Method(args...)` | Method call via reflection | `user.SayHi('hi')` |
| `struct` / `*struct` | `user.Method` | Getter-style: only if method has **0 inputs** and **>=1 outputs** | `user.MultiReturn` |
| `map[K]V` | `m.key` or `m[key]` | `key` is a string; if `K` is numeric, Okra tries to parse numeric keys from strings | `scores.1`, `scores[1]` |
| `[]T` / `[N]T` | `arr.0` or `arr[0]` | Index access; invalid/out-of-range returns `nil` | `nums.1`, `nums[1]` |
| pointers | auto-dereference | Nil pointers usually yield `nil` (except some special cases like `len`) | `ptr.Field`, `ptr.Method()` |

### About `arr[0]`

Okra supports bracket indexing like `arr[0]` and it also supports **index expressions**.

- Examples: `arr[i]`, `arr[1+2]`, `matrix[row][col]`, `scores[0+1]`
- Indexing works for slices/arrays and maps (via reflection)

## Built-in Functions

| Name | Signature / return | Supported inputs | Example | Example result |
|---|---|---|---|---|
| `len` | `len(x) -> int64`; `len() -> 0` | `slice` / `string` / `map` (other types return `0`) | `len(tags)`, `len()` | `int64(2)`, `0` |
| `now` | `now() -> int64` | none | `now()` | Unix seconds |

## Operators and Type Coercion

Okra uses three internal coercions:

- `toInt64`: supports all signed/unsigned integer kinds
- `toFloat`: supports integers, floats, and strings that parse via `ParseFloat`
- `toBool`: supports `bool`, strings (`""` and `"false"` are false), otherwise `toFloat(v) != 0`

### Arithmetic: `+ - * / %`

| Operator | Supported types | Rule (simplified) | Example | Example result |
|---|---|---|---|---|
| `+` | number + number | integer path if both are int-like; otherwise float path | `1 + 2`, `1.5 + 2` | `int64(3)`, `3.5` |
| `+` | string + any | if LHS is `string`, concatenates `fmt.Sprint(rhs)` | `'res:' + 10` | `"res:10"` |
| `- * /` | numbers | integer path if possible; otherwise float path | `10 - 3`, `2.0 * 3.5` | `int64(7)`, `7.0` |
| `/` | numbers | division by zero returns error | `10 / 0` | error |
| `%` | integers | modulo by zero returns error; float modulo is not supported | `10 % 3`, `1.2 % 2.0` | `int64(1)`, `nil` |

### Comparison: `> < >= <=`

| Operator | Supported types | Rule | Example | Example result |
|---|---|---|---|---|
| `> < >= <=` | both sides must be `toFloat`-convertible | otherwise returns error | `10.5 > 10` | `true` |

### Equality: `== !=`

| Operator | Supported types | Rule | Example | Example result |
|---|---|---|---|---|
| `==` | any | `DeepEqual` first; else numeric compare if both `toFloat`; else `false` | `10 == 10.0`, `'a' == 'b'` | `true`, `false` |
| `!=` | any | `DeepEqual` first; else numeric compare if both `toFloat`; else `true` | `10 != 10.0`, `'a' != 'b'` | `false`, `true` |

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

## Error Handling and Panic Safety

Okra attempts to never crash the host application.

- `Engine.Eval` returns `(any, error)`.
- If evaluation hits a `panic` (including panics from reflection/type conversions or panics inside user methods), Okra will **recover** and return it as an `error`.

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
