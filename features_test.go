package okra

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// --- `in` operator + list literals --------------------------------------------

func TestInOperatorAndListLiterals(t *testing.T) {
	e := NewEngine()
	data := map[string]any{
		"nums": []int{1, 2, 3},
		"m":    map[string]int{"a": 1, "b": 2},
		"s":    "hello world",
	}
	cases := []struct {
		expr string
		want any
	}{
		{"2 in nums", true},
		{"9 in nums", false},
		{"2 in [1, 2, 3]", true},
		{"'a' in m", true}, // map key membership
		{"'z' in m", false},
		{"'world' in s", true}, // substring
		{"'nope' in s", false},
		{"[] == []", true}, // empty list literal parses and evaluates
		{"len([10, 20, 30])", int64(3)},
	}
	for _, c := range cases {
		got, err := e.Eval(c.expr, data)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", c.expr, err)
		}
		if got != c.want {
			t.Fatalf("%s: got %v (%T), want %v", c.expr, got, got, c.want)
		}
	}

	// `in` on an unsupported type is an error, not a panic.
	if _, err := e.Eval("1 in 2", nil); err == nil {
		t.Fatal("expected error for `in` on non-container")
	}
}

func TestNotInOperator(t *testing.T) {
	e := NewEngine()
	cases := []struct {
		expr string
		want any
	}{
		{"4 not in [1, 2, 3]", true},
		{"2 not in [1, 2, 3]", false},
		{"'z' not in 'abc'", true},
		{"'b' not in 'abc'", false},
	}
	for _, c := range cases {
		got, err := e.Eval(c.expr, nil)
		if err != nil || got != c.want {
			t.Fatalf("%s: got %v, err %v, want %v", c.expr, got, err, c.want)
		}
	}
	// `not` without `in` is a clean parse error, not a panic.
	if _, err := e.Eval("1 not 2", nil); err == nil {
		t.Fatal("expected error for bare 'not'")
	}
}

func TestStringOrdering(t *testing.T) {
	e := NewEngine()
	cases := []struct {
		expr string
		want any
	}{
		{"'a' < 'b'", true},
		{"'b' > 'a'", true},
		{"'abc' <= 'abd'", true},
		{"'abc' >= 'abd'", false},
	}
	for _, c := range cases {
		got, err := e.Eval(c.expr, nil)
		if err != nil || got != c.want {
			t.Fatalf("%s: got %v, err %v, want %v", c.expr, got, err, c.want)
		}
	}
	// Strong typing: comparing a string with a number is a type error, never a
	// silent numeric coercion. '5' > 3 no longer secretly means 5 > 3.
	for _, src := range []string{"'5' > 3", "3 < '5'", "'10' >= 10"} {
		if _, err := e.Eval(src, nil); err == nil {
			t.Fatalf("%s: expected a type error for mixed string/number comparison", src)
		}
	}
}

func TestEqualityIsTypeAware(t *testing.T) {
	e := NewEngine()
	cases := []struct {
		expr string
		want any
	}{
		{"1 == '1'", false}, // string is not numerically equal to a number
		{"'1' != 1", true},
		{"10 == 10.0", true}, // cross-numeric equality still holds
		{"'a' == 'a'", true},
	}
	for _, c := range cases {
		got, err := e.Eval(c.expr, nil)
		if err != nil || got != c.want {
			t.Fatalf("%s: got %v, err %v, want %v", c.expr, got, err, c.want)
		}
	}
}

func TestInPrecedence(t *testing.T) {
	e := NewEngine()
	// `+` (40) binds tighter than `in` (35): (1+1) in [2,3]
	if got, err := e.Eval("1 + 1 in [2, 3]", nil); err != nil || got != true {
		t.Fatalf("arith before in: got %v, %v", got, err)
	}
	// `in` (35) binds tighter than `==` (30): (2 in [1,2]) == true
	if got, err := e.Eval("2 in [1, 2] == true", nil); err != nil || got != true {
		t.Fatalf("in before ==: got %v, %v", got, err)
	}
}

// --- strict mode ---------------------------------------------------------------

type strictUser struct {
	Name string
	Tags []string
}

func TestStrictMode(t *testing.T) {
	e := NewEngine()
	data := map[string]any{
		"user":   strictUser{Name: "Alice", Tags: []string{"a"}},
		"scores": map[string]int{"x": 1},
	}

	// Opt out: unknown field/key/index resolve to nil under non-strict.
	e.SetStrict(false)
	for _, expr := range []string{"user.Naem", "scores.missing", "user.Tags[9]"} {
		if v, err := e.Eval(expr, data); err != nil || v != nil {
			t.Fatalf("non-strict %s: got %v, %v", expr, v, err)
		}
	}

	// Strict is the default; assert a fresh engine already errors on the typo.
	if _, err := NewEngine().Eval("user.Naem", data); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("strict should be the default: got %v", err)
	}

	e.SetStrict(true)
	strictErrs := []string{
		"user.Naem",        // misspelled field
		"scores.missing",   // absent map key
		"user.Tags[9]",     // out of range
		"user.Tags[9] > 0", // propagates through operators
	}
	for _, expr := range strictErrs {
		if _, err := e.Eval(expr, data); !errors.Is(err, ErrUnknownField) {
			t.Fatalf("strict %s: expected ErrUnknownField, got %v", expr, err)
		}
	}
	// Valid access still works in strict mode.
	if v, err := e.Eval("user.Name", data); err != nil || v != "Alice" {
		t.Fatalf("strict valid: got %v, %v", v, err)
	}
}

// --- method filter ---------------------------------------------------------------

type guarded struct{ n int }

func (g guarded) Double() int { return g.n * 2 }
func (g guarded) Danger() int { return 999 }

func TestMethodFilter(t *testing.T) {
	e := NewEngine()
	data := map[string]any{"g": guarded{n: 21}}

	// No filter: both methods callable.
	if v, err := e.Eval("g.Double()", data); err != nil || v != 42 {
		t.Fatalf("unfiltered Double: %v, %v", v, err)
	}

	// Allow only Double.
	e.SetMethodFilter(func(name string) bool { return name == "Double" })
	if v, err := e.Eval("g.Double()", data); err != nil || v != 42 {
		t.Fatalf("filtered Double: %v, %v", v, err)
	}
	if _, err := e.Eval("g.Danger()", data); !errors.Is(err, ErrMethodDenied) {
		t.Fatalf("Danger should be denied, got %v", err)
	}

	// Filter also gates getter-style access (0-in, 1-out method as a field).
	e.SetMethodFilter(func(name string) bool { return false })
	if _, err := e.Eval("g.Double", data); !errors.Is(err, ErrMethodDenied) {
		t.Fatalf("getter should be denied, got %v", err)
	}

	// nil restores allow-all.
	e.SetMethodFilter(nil)
	if v, err := e.Eval("g.Danger()", data); err != nil || v != 999 {
		t.Fatalf("after reset: %v, %v", v, err)
	}
}

// --- constant folding ------------------------------------------------------------

func TestConstantFolding(t *testing.T) {
	e := NewEngine()
	// Constant subtree folds to a literal.
	prog, err := e.Compile("1 + 2 * 3")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := prog.ast.(*LiteralExpr); !ok {
		t.Fatalf("expected folded literal, got %T", prog.ast)
	}
	if v, _ := prog.Eval(nil); v != int64(7) {
		t.Fatalf("folded value: got %v", v)
	}

	// A constant error subtree is NOT folded (error still surfaces at Eval).
	prog2, err := e.Compile("1 / 0")
	if err != nil {
		t.Fatalf("compile should not error: %v", err)
	}
	if _, ok := prog2.ast.(*LiteralExpr); ok {
		t.Fatal("1/0 must not be folded")
	}
	if _, err := prog2.Eval(nil); !errors.Is(err, ErrDivByZero) {
		t.Fatalf("expected ErrDivByZero at eval, got %v", err)
	}

	// Mixed constant/variable: only the constant part folds, result stays correct.
	if v, err := e.Eval("x + 2 * 3", map[string]any{"x": int64(1)}); err != nil || v != int64(7) {
		t.Fatalf("mixed fold: got %v, %v", v, err)
	}
}

// --- introspection ---------------------------------------------------------------

func TestProgramIntrospection(t *testing.T) {
	e := NewEngine()
	prog, err := e.Compile("user.Age > 18 && contains(user.Name, 'a') || score in [1, 2]")
	if err != nil {
		t.Fatal(err)
	}
	vars := prog.Vars()
	wantVars := map[string]bool{"user": true, "score": true}
	for _, v := range vars {
		if !wantVars[v] {
			t.Fatalf("unexpected var %q in %v", v, vars)
		}
	}
	if len(vars) != 2 {
		t.Fatalf("expected 2 vars, got %v", vars)
	}
	funcs := prog.Funcs()
	if len(funcs) != 1 || funcs[0] != "contains" {
		t.Fatalf("expected [contains], got %v", funcs)
	}
}

// --- embedded / promoted struct fields ------------------------------------------

type EmbBase struct {
	ID   int
	Name string
}
type EmbMeta struct {
	Kind string `okra:"kind"`
}
type EmbUser struct {
	EmbBase
	*EmbMeta
	Name string // shadows EmbBase.Name
	Age  int
}

func TestEmbeddedFields(t *testing.T) {
	e := NewEngine()
	u := EmbUser{
		EmbBase: EmbBase{ID: 7, Name: "base"},
		EmbMeta: &EmbMeta{Kind: "admin"},
		Name:    "outer",
		Age:     30,
	}
	data := map[string]any{"u": u}
	cases := []struct {
		expr string
		want any
	}{
		{"u.ID", 7},                // promoted from EmbBase
		{"u.Age", 30},              // direct
		{"u.Name", "outer"},        // outer shadows EmbBase.Name
		{"u.kind", "admin"},        // promoted from *EmbMeta, via tag
		{"u.EmbBase.Name", "base"}, // reach the shadowed one explicitly
	}
	for _, c := range cases {
		got, err := e.Eval(c.expr, data)
		if err != nil || got != c.want {
			t.Fatalf("%s: got %v (%T), err %v, want %v", c.expr, got, got, err, c.want)
		}
	}

	// A promoted field through a nil embedded pointer must not panic.
	u2 := EmbUser{EmbMeta: nil, Name: "x"}
	e.SetStrict(false)
	if got, err := e.Eval("u.kind", map[string]any{"u": u2}); err != nil || got != nil {
		t.Fatalf("nil embedded ptr (non-strict): got %v, err %v", got, err)
	}
	e.SetStrict(true)
	if _, err := e.Eval("u.kind", map[string]any{"u": u2}); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("nil embedded ptr (strict): expected ErrUnknownField, got %v", err)
	}
}

// --- folded list String() round-trips -------------------------------------------

func TestFoldedListRoundTrip(t *testing.T) {
	ast, err := ParseExpr("x in [1, 'a', 2]")
	if err != nil {
		t.Fatal(err)
	}
	folded := foldConstants(ast) // the list literal folds to a []any literal
	s := folded.String()
	// The rendered form must parse again and evaluate identically.
	if _, err := ParseExpr(s); err != nil {
		t.Fatalf("folded String() %q does not round-trip: %v", s, err)
	}
	e := NewEngine()
	data := map[string]any{"x": "a"}
	v1, _ := e.Eval("x in [1, 'a', 2]", data)
	v2, _ := e.Eval(s, data)
	if v1 != true || v2 != v1 {
		t.Fatalf("round-trip mismatch: %v vs %v (rendered %q)", v1, v2, s)
	}
}

// --- Funcs() includes method calls ----------------------------------------------

func TestFuncsIncludesMethods(t *testing.T) {
	e := NewEngine()
	prog, err := e.Compile("contains(user.Name, 'a') && user.IsActive()")
	if err != nil {
		t.Fatal(err)
	}
	got := prog.Funcs()
	want := map[string]bool{"contains": true, "IsActive": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("Funcs() = %v, want contains+IsActive", got)
	}
}

// --- concurrency: a compiled Program is safe for concurrent Eval ----------------

func TestConcurrentEval(t *testing.T) {
	e := NewEngine()
	prog, err := e.Compile("a * b + a - b")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 50
	done := make(chan error, workers)
	for w := 0; w < workers; w++ {
		go func(n int64) {
			var lastErr error
			for i := 0; i < 1000; i++ {
				v, err := prog.Eval(map[string]any{"a": n, "b": int64(2)})
				if err != nil {
					lastErr = err
					break
				}
				if v != n*2+n-2 {
					lastErr = fmt.Errorf("got %v for a=%d", v, n)
					break
				}
			}
			done <- lastErr
		}(int64(w + 1))
	}
	for w := 0; w < workers; w++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func BenchmarkEvalReparsed(b *testing.B) {
	e := NewEngine()
	data := map[string]any{"a": int64(3), "b": int64(4)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = e.Eval("a * b + a - b", data) // re-parses every call
	}
}

func BenchmarkEvalCompiled(b *testing.B) {
	e := NewEngine()
	data := map[string]any{"a": int64(3), "b": int64(4)}
	prog, err := e.Compile("a * b + a - b")
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = prog.Eval(data) // parsed once
	}
}

// --- string builtins ----------------------------------------------------------

func TestStringBuiltins(t *testing.T) {
	e := NewEngine()
	cases := []struct {
		expr string
		want any
	}{
		{"contains('hello', 'ell')", true},
		{"startsWith('hello', 'he')", true},
		{"endsWith('hello', 'lo')", true},
		{"lower('HeLLo')", "hello"},
		{"upper('HeLLo')", "HELLO"},
		{"trim('  hi  ')", "hi"},
	}
	for _, c := range cases {
		got, err := e.Eval(c.expr, nil)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", c.expr, err)
		}
		if got != c.want {
			t.Fatalf("%s: got %v, want %v", c.expr, got, c.want)
		}
	}
	// Wrong arity returns an error rather than panicking.
	if _, err := e.Eval("contains('x')", nil); err == nil {
		t.Fatal("expected arity error")
	}
}

// --- number lexing ------------------------------------------------------------

func TestNumberLexing(t *testing.T) {
	e := NewEngine()
	cases := []struct {
		expr string
		want any
	}{
		{"0xFF", int64(255)},
		{"0x10", int64(16)},
		{"1e3", float64(1000)},
		{"1.5e2", float64(150)},
		{"1_000", int64(1000)},
		{"1_000.5", float64(1000.5)},
	}
	for _, c := range cases {
		got, err := e.Eval(c.expr, nil)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", c.expr, err)
		}
		if got != c.want {
			t.Fatalf("%s: got %v (%T), want %v", c.expr, got, got, c.want)
		}
	}
	// Malformed numbers are errors, not silent zeros.
	for _, bad := range []string{"1.2.3", "0xZZ", "1e"} {
		if _, err := e.Eval(bad, nil); err == nil {
			t.Fatalf("%s: expected parse error", bad)
		}
	}
}

// --- UTF-8 identifiers ---------------------------------------------------------

func TestUnicodeIdentifiers(t *testing.T) {
	e := NewEngine()
	data := map[string]any{"名前": "Alice", "число": int64(42)}
	if got, err := e.Eval("名前", data); err != nil || got != "Alice" {
		t.Fatalf("unicode var: got %v, %v", got, err)
	}
	if got, err := e.Eval("число + 8", data); err != nil || got != int64(50) {
		t.Fatalf("unicode arithmetic: got %v, %v", got, err)
	}
}

// --- unexported field safety (no panic) ---------------------------------------

type withHidden struct {
	Name   string
	secret int //nolint:unused // intentionally unexported for the test
}

func TestUnexportedFieldIsSafe(t *testing.T) {
	e := NewEngine()
	data := map[string]any{"o": &withHidden{Name: "x", secret: 99}}
	if got, err := e.Eval("o.Name", data); err != nil || got != "x" {
		t.Fatalf("exported field: got %v, %v", got, err)
	}
	// Strict (default): the unexported field is not exposed; it errors cleanly
	// (never panics, never leaks the value).
	if _, err := e.Eval("o.secret", data); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("unexported field (strict): expected ErrUnknownField, got %v", err)
	}
	// Non-strict: it simply resolves to nil, still never panicking or leaking.
	e.SetStrict(false)
	if got, err := e.Eval("o.secret", data); err != nil || got != nil {
		t.Fatalf("unexported field: got %v, %v", got, err)
	}
}

// --- method fallback error transparency ---------------------------------------

type rootWithMethods struct{}

func (rootWithMethods) Boom() (int, error) { return 0, errors.New("kaboom") }

func TestMethodFallbackErrorNotMasked(t *testing.T) {
	e := NewEngine()
	// A method that exists but fails should surface its real error...
	_, err := e.Eval("Boom()", rootWithMethods{})
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("expected kaboom error, got %v", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("real method error must not be reported as not-found")
	}
	// ...while a genuinely missing function reports not-found.
	_, err = e.Eval("Missing()", rootWithMethods{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// --- sentinel errors ----------------------------------------------------------

func TestSentinelErrors(t *testing.T) {
	e := NewEngine()
	checks := []struct {
		expr string
		is   error
	}{
		{"10 / 0", ErrDivByZero},
		{"10 % 0", ErrModByZero},
		{"1.5 % 2.0", ErrFloatModulo},
		{"1 << -1", ErrNegativeShift},
	}
	for _, c := range checks {
		_, err := e.Eval(c.expr, nil)
		if !errors.Is(err, c.is) {
			t.Fatalf("%s: expected %v, got %v", c.expr, c.is, err)
		}
	}
}

// --- configurable nesting depth -----------------------------------------------

func TestSetMaxNestingDepth(t *testing.T) {
	e := NewEngine()
	e.SetMaxNestingDepth(3)
	if _, err := e.Eval("(((( 1 ))))", nil); err == nil {
		t.Fatal("expected nesting-too-deep error")
	}
	e.SetMaxNestingDepth(0) // restore default
	if _, err := e.Eval("(((( 1 ))))", nil); err != nil {
		t.Fatalf("default depth should allow it: %v", err)
	}
}

// --- strong-typed conditions: strings are never coerced to bool ---------------

func TestStringConditionErrors(t *testing.T) {
	e := NewEngine()
	// Under strong typing, a string (or number, or nil) as a condition is a type
	// error, not a silent truthy/falsy guess. No more "'0' is truthy" footgun.
	for _, src := range []string{
		"'false' ? 1 : 2",
		"'0' ? 1 : 2",
		"'x' ? 1 : 2",
		"5 ? 1 : 2",
		"'a' && true",
		"!'x'",
	} {
		if _, err := e.Eval(src, nil); err == nil {
			t.Fatalf("%s: expected a bool-condition type error", src)
		}
	}
}

// --- String() round-trips through ParseExpr -----------------------------------

func TestStringRoundTrip(t *testing.T) {
	for _, src := range []string{"'it\\'s ok'", "'a\\nb'", "'plain'"} {
		ast, err := ParseExpr(src)
		if err != nil {
			t.Fatalf("parse %s: %v", src, err)
		}
		reparsed, err := ParseExpr(ast.String())
		if err != nil {
			t.Fatalf("reparse %q: %v", ast.String(), err)
		}
		v1, _ := (&Engine{}).evalAST(ast)
		v2, _ := (&Engine{}).evalAST(reparsed)
		if v1 != v2 {
			t.Fatalf("round-trip mismatch: %v vs %v", v1, v2)
		}
	}
}

// evalAST is a tiny test helper to evaluate a pre-built AST with default funcs.
func (e *Engine) evalAST(ast Expr) (any, error) {
	src := NewEngine()
	return (&Program{
		ast:          ast,
		fns:          src.loadFuncs(),
		macros:       src.loadMacros(),
		strict:       src.strict.Load(),
		methodFilter: src.methodFilterFn(),
	}).Eval(nil)
}

// --- panic safety: nothing ever escapes as a panic ----------------------------

func FuzzEvalNeverPanics(f *testing.F) {
	seeds := []string{
		"1 + 1", "user.Name", "a ? b : c", "[1, 2, 3]", "'x' in y",
		"1.2.3", "((((", "))))", "0xFF", "1_000", "len()", "a.b.c.d",
		"1 << -1", "10 / 0", "'unterminated", "a[b][c]", "-~!x",
		"contains('a','b')", "名前", "@#$%", "a in [1,2] in b",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	e := NewEngine()
	data := map[string]any{"a": int64(1), "b": int64(2), "user": map[string]any{"Name": "x"}}
	f.Fuzz(func(t *testing.T, expr string) {
		// None of these may panic; the engine must always return (value, error).
		_, _ = e.Eval(expr, data)
		if prog, err := e.Compile(expr); err == nil {
			_, _ = prog.Eval(data)
		}
		_, _ = ParseExpr(expr)
	})
}

// --- has()/get(): the sanctioned way to touch possibly-absent members ---------

func TestHasGet(t *testing.T) {
	e := NewEngine() // strict is on by default
	type coupon struct{ Code string }
	type user struct {
		Name   string
		Coupon *coupon
	}
	data := map[string]any{
		"user":   user{Name: "Alice", Coupon: nil},
		"scores": map[string]int{"math": 90},
	}

	cases := []struct {
		expr string
		want any
	}{
		{"has(user, 'Name')", true},
		{"has(user, 'Nope')", false},
		{"has(scores, 'math')", true},
		{"has(scores, 'science')", false},
		{"get(scores, 'math', 0)", 90},            // found value keeps its Go type (map[string]int)
		{"get(scores, 'science', -1)", int64(-1)}, // default is a DSL int64 literal
		{"get(user, 'Name', 'x')", "Alice"},
		// Optional field expressed explicitly instead of relying on silent nil.
		{"has(user, 'Coupon') ? 'has' : 'none'", "has"}, // key present (value is nil ptr)
		{"has(scores, 'bonus') ? get(scores, 'bonus', 0) : 0", int64(0)},
	}
	for _, c := range cases {
		got, err := e.Eval(c.expr, data)
		if err != nil || got != c.want {
			t.Fatalf("%s: got %v (%T), err %v, want %v", c.expr, got, got, err, c.want)
		}
	}

	// has/get take the member NAME as a string, and never invoke methods.
	if _, err := e.Eval("has(user, 123)", data); err == nil {
		t.Fatal("has with non-string name should error")
	}
}

// --- RegisterMacro: collection ops live in userland, not the core -------------

// registerAnyAll installs userland any/all macros. A macro receives its args
// un-evaluated, so it can re-evaluate the predicate once per element with the
// element swapped in as the Context's Data (the element-scoping convention is
// the macro's choice, not the language's).
func registerAnyAll(t *testing.T, e *Engine) {
	t.Helper()
	quantify := func(all bool) MacroFunc {
		return func(ctx Context, args []Expr) (any, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("any/all: expected 2 args")
			}
			coll, err := args[0].Eval(ctx)
			if err != nil {
				return nil, err
			}
			rv := reflect.ValueOf(coll)
			if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
				return nil, fmt.Errorf("any/all: not a collection: %T", coll)
			}
			for i := 0; i < rv.Len(); i++ {
				child := ctx
				child.Data = rv.Index(i).Interface()
				v, err := args[1].Eval(child)
				if err != nil {
					return nil, err
				}
				b, err := asBool(v)
				if err != nil {
					return nil, err
				}
				if all && !b {
					return false, nil
				}
				if !all && b {
					return true, nil
				}
			}
			return all, nil
		}
	}
	if err := e.RegisterMacro("any", quantify(false)); err != nil {
		t.Fatal(err)
	}
	if err := e.RegisterMacro("all", quantify(true)); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterMacroAnyAll(t *testing.T) {
	e := NewEngine()
	registerAnyAll(t, e)

	data := map[string]any{
		"orders": []map[string]any{
			{"price": int64(50), "fresh": true},
			{"price": int64(150), "fresh": false},
		},
	}
	// This macro's element-scoping convention: the predicate is evaluated with the
	// element as the root, so a bare field name (price, fresh) refers to the
	// element. That is the macro author's choice; the core language stays neutral.
	cases := []struct {
		expr string
		want any
	}{
		{"any(orders, price > 100)", true},
		{"all(orders, price > 100)", false},
		{"any(orders, fresh)", true}, // element field used directly as a bool condition
		{"any(orders, price == 50)", true},
		{"all(orders, price >= 50)", true},
	}
	for _, c := range cases {
		got, err := e.Eval(c.expr, data)
		if err != nil || got != c.want {
			t.Fatalf("%s: got %v (%T), err %v, want %v", c.expr, got, got, err, c.want)
		}
	}

	// A macro is resolved before a plain function of the same name would be, and
	// before the data-method fallback; unknown names still error.
	if _, err := e.Eval("nope(orders, price > 100)", data); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown call: expected ErrNotFound, got %v", err)
	}
}

// --- Program immutability: config is snapshotted at Compile -------------------

func TestProgramConfigSnapshot(t *testing.T) {
	e := NewEngine()
	data := map[string]any{"user": map[string]any{"Name": "x"}}

	// Compiled while strict (default): flipping strict afterwards must not change it.
	strictProg, err := e.Compile("user.Nope")
	if err != nil {
		t.Fatal(err)
	}
	e.SetStrict(false)
	if _, err := strictProg.Eval(data); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("snapshot: strict program should still error, got %v", err)
	}
	// A program compiled now (non-strict) resolves the typo to nil.
	lenientProg, err := e.Compile("user.Nope")
	if err != nil {
		t.Fatal(err)
	}
	e.SetStrict(true) // change again: must not affect the already-compiled program
	if v, err := lenientProg.Eval(data); err != nil || v != nil {
		t.Fatalf("snapshot: lenient program should stay lenient, got %v, %v", v, err)
	}
}

// --- nil is allowed to exist and be consumed, but errors when used in an op ----

func TestNilOnUse(t *testing.T) {
	e := NewEngine()
	data := map[string]any{
		"m": map[string]any{"x": nil}, // present key whose value is nil
	}

	// has is structural: the key is there (even though its value is nil).
	if v, err := e.Eval("has(m, 'x')", data); err != nil || v != true {
		t.Fatalf("has present-nil: got %v, %v", v, err)
	}
	// get is value-level: a present-but-nil member yields the default, so
	// get(...) is always safe to feed into an operation.
	if v, err := e.Eval("get(m, 'x', 'dflt')", data); err != nil || v != "dflt" {
		t.Fatalf("get present-nil: got %v, %v (want dflt)", v, err)
	}
	if v, err := e.Eval("get(m, 'x', 1) + 1", data); err != nil || v != int64(2) {
		t.Fatalf("get feeds an op safely: got %v, %v", v, err)
	}

	// nil can still be a final result (returned to the Go caller as-is).
	if v, err := e.Eval("m.x", data); err != nil || v != nil {
		t.Fatalf("nil final result: got %v, %v", v, err)
	}

	// But taking that nil into an operation is an error, never a silent 0/false.
	for _, src := range []string{
		"m.x + 1",     // arithmetic
		"m.x > 0",     // comparison
		"m.x ? 1 : 2", // condition
		"m.x.y",       // further member access
		"1 in m.x",    // nil as a container
		"len(m.x)",    // len of nil
	} {
		if _, err := e.Eval(src, data); err == nil {
			t.Fatalf("%s: expected an error using nil in an operation", src)
		}
	}

	// Equality is the one exemption: it always has an answer, for nil and for
	// mixed types alike (they simply compare unequal).
	if v, err := e.Eval("m.x == 1", data); err != nil || v != false {
		t.Fatalf("nil == 1: got %v, %v", v, err)
	}
	if v, err := e.Eval("m.x != 1", data); err != nil || v != true {
		t.Fatalf("nil != 1: got %v, %v", v, err)
	}
}

// --- fail-loud builtins: no silent stringification, no len()=0 -----------------

func TestBuiltinsAreStrict(t *testing.T) {
	e := NewEngine()
	// String builtins require real strings — no fmt.Sprint coercion.
	for _, src := range []string{
		"contains(123, '2')",
		"contains('abc', 1)",
		"startsWith(1, '1')",
		"endsWith('a', 5)",
		"lower(456)",
		"upper(true)",
		"trim(1.5)",
	} {
		if _, err := e.Eval(src, nil); err == nil {
			t.Fatalf("%s: expected a type error", src)
		}
	}
	// len: wrong arity and non-sized types are errors.
	for _, src := range []string{"len()", "len(5)", "len(true)", "len('a', 'b')"} {
		if _, err := e.Eval(src, nil); err == nil {
			t.Fatalf("%s: expected an error", src)
		}
	}
	// The valid forms still work.
	if v, err := e.Eval("len('abc')", nil); err != nil || v != int64(3) {
		t.Fatalf("len('abc'): got %v, %v", v, err)
	}
	if v, err := e.Eval("contains('hello', 'ell')", nil); err != nil || v != true {
		t.Fatalf("contains: got %v, %v", v, err)
	}
}

// --- error annotation: failures name the sub-expression that produced them -----

func TestErrorsNameFailingSubexpression(t *testing.T) {
	e := NewEngine()
	data := map[string]any{
		"user": map[string]any{"Age": int64(30), "Name": "Alice"},
	}
	// A long rule with several comparisons: the error must point at the one that
	// failed, and only once (no re-wrapping at every level on the way up).
	_, err := e.Eval("user.Age > 18 && user.Age > user.Name && user.Age < 65", data)
	if err == nil {
		t.Fatal("expected a comparison type error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "user.Age > user.Name") {
		t.Fatalf("error should name the failing sub-expression, got: %v", msg)
	}
	if strings.Count(msg, "user.Age < 65") != 0 {
		t.Fatalf("error should not drag in unrelated sub-expressions, got: %v", msg)
	}

	// Sentinel matching still works through the annotation (%w).
	if _, err := e.Eval("true && (1/0 == 1)", nil); !errors.Is(err, ErrDivByZero) {
		t.Fatalf("expected ErrDivByZero through annotation, got %v", err)
	}
}
