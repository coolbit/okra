package core

import (
	"errors"
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
		{"'a' in m", true},   // map key membership
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
		{"'5' > 3", true}, // numeric string vs number still compares numerically
	}
	for _, c := range cases {
		got, err := e.Eval(c.expr, nil)
		if err != nil || got != c.want {
			t.Fatalf("%s: got %v, err %v, want %v", c.expr, got, err, c.want)
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

	// Non-strict (default): unknown field/key/index resolve to nil.
	for _, expr := range []string{"user.Naem", "scores.missing", "user.Tags[9]"} {
		if v, err := e.Eval(expr, data); err != nil || v != nil {
			t.Fatalf("non-strict %s: got %v, %v", expr, v, err)
		}
	}

	e.SetStrict(true)
	strictErrs := []string{
		"user.Naem",       // misspelled field
		"scores.missing",  // absent map key
		"user.Tags[9]",    // out of range
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
	// Accessing the unexported field must not panic; it simply resolves to nil.
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

// --- toBool case-insensitivity ------------------------------------------------

func TestToBoolCaseInsensitiveFalse(t *testing.T) {
	for _, s := range []string{"false", "False", "FALSE"} {
		if toBool(s) {
			t.Fatalf("toBool(%q) should be false", s)
		}
	}
	if !toBool("0") { // non-empty, not "false"
		t.Fatal(`toBool("0") should be true`)
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
	return (&Program{ast: ast, engine: NewEngine()}).Eval(nil)
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
