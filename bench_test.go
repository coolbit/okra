package okra

import (
	"context"
	"reflect"
	"testing"
)

// The benchmark suite measures the paths that matter for the rules-engine
// pattern: compile once, evaluate many times. Run with:
//
//	go test -bench . -benchmem -run '^$'

type benchUser struct {
	Name   string
	Age    int
	VIP    bool
	Scores []int
}

func (u benchUser) Discount() int { return 15 }

func benchData() map[string]any {
	return map[string]any{
		"user":   benchUser{Name: "Alice", Age: 30, VIP: true, Scores: []int{90, 85, 77}},
		"amount": int64(250),
		"status": "active",
		"tags":   []string{"a", "b", "c"},
	}
}

func mustCompile(b *testing.B, e *Engine, src string) *Program {
	b.Helper()
	prog, err := e.Compile(src)
	if err != nil {
		b.Fatal(err)
	}
	return prog
}

// --- compile (parse + fold) ----------------------------------------------------

func BenchmarkCompileSimple(b *testing.B) {
	e := NewEngine()
	for b.Loop() {
		_, _ = e.Compile("a + b * c")
	}
}

func BenchmarkCompileRule(b *testing.B) {
	e := NewEngine()
	for b.Loop() {
		_, _ = e.Compile("user.VIP && amount > 100 && status in ['active', 'trial'] ? amount * 8 / 10 : amount")
	}
}

// --- eval: core operator paths ---------------------------------------------------

func BenchmarkEvalConstantFolded(b *testing.B) {
	// The whole expression folds to a literal at compile time; this measures
	// the floor: recover-wrapper + one LiteralExpr.Eval.
	prog := mustCompile(b, NewEngine(), "1 + 2 * 3")
	for b.Loop() {
		_, _ = prog.Eval(nil)
	}
}

func BenchmarkEvalArithmetic(b *testing.B) {
	prog := mustCompile(b, NewEngine(), "a * b + a - b")
	data := map[string]any{"a": int64(3), "b": int64(4)}
	for b.Loop() {
		_, _ = prog.Eval(data)
	}
}

func BenchmarkEvalComparisonLogic(b *testing.B) {
	prog := mustCompile(b, NewEngine(), "a > 1 && b < 10 && a != b")
	data := map[string]any{"a": int64(3), "b": int64(4)}
	for b.Loop() {
		_, _ = prog.Eval(data)
	}
}

// --- eval: member access ---------------------------------------------------------

func BenchmarkEvalStructField(b *testing.B) {
	prog := mustCompile(b, NewEngine(), "user.Age")
	data := benchData()
	for b.Loop() {
		_, _ = prog.Eval(data)
	}
}

func BenchmarkEvalStructFieldChain(b *testing.B) {
	prog := mustCompile(b, NewEngine(), "user.Scores[1]")
	data := benchData()
	for b.Loop() {
		_, _ = prog.Eval(data)
	}
}

func BenchmarkEvalMapKey(b *testing.B) {
	prog := mustCompile(b, NewEngine(), "m.x")
	data := map[string]any{"m": map[string]any{"x": int64(1)}}
	for b.Loop() {
		_, _ = prog.Eval(data)
	}
}

func BenchmarkEvalMethodCall(b *testing.B) {
	prog := mustCompile(b, NewEngine(), "user.Discount()")
	data := benchData()
	for b.Loop() {
		_, _ = prog.Eval(data)
	}
}

// --- eval: a realistic rule -------------------------------------------------------

func BenchmarkEvalTypicalRule(b *testing.B) {
	prog := mustCompile(b, NewEngine(),
		"user.VIP && amount > 100 && status in ['active', 'trial'] ? amount * 8 / 10 : amount")
	data := benchData()
	for b.Loop() {
		_, _ = prog.Eval(data)
	}
}

// --- eval: builtins ---------------------------------------------------------------

func BenchmarkEvalStringBuiltin(b *testing.B) {
	prog := mustCompile(b, NewEngine(), "startsWith(status, 'act')")
	data := benchData()
	for b.Loop() {
		_, _ = prog.Eval(data)
	}
}

func BenchmarkEvalHasGet(b *testing.B) {
	prog := mustCompile(b, NewEngine(), "has(user, 'Age') ? get(user, 'Age', 0) : 0")
	data := benchData()
	for b.Loop() {
		_, _ = prog.Eval(data)
	}
}

// --- eval: `in` over collections ----------------------------------------------

func benchmarkInSlice(b *testing.B, n int) {
	big := make([]int64, n) // needle never matches: full scan every eval
	prog := mustCompile(b, NewEngine(), "1 in big")
	data := map[string]any{"big": big}
	b.ResetTimer()
	for b.Loop() {
		_, _ = prog.Eval(data)
	}
}

func BenchmarkInSlice10(b *testing.B)   { benchmarkInSlice(b, 10) }
func BenchmarkInSlice1000(b *testing.B) { benchmarkInSlice(b, 1000) }
func BenchmarkInSlice100k(b *testing.B) { benchmarkInSlice(b, 100_000) }

// --- eval: macro (userland any over a collection) --------------------------------

func BenchmarkMacroAnyOver100(b *testing.B) {
	e := NewEngine()
	err := e.RegisterMacro("any", func(ctx Context, args []Expr) (any, error) {
		coll, err := args[0].Eval(ctx)
		if err != nil {
			return nil, err
		}
		rv := reflect.ValueOf(coll)
		for i := 0; i < rv.Len(); i++ {
			child := ctx
			child.Data = rv.Index(i).Interface()
			v, err := args[1].Eval(child)
			if err != nil {
				return nil, err
			}
			if vb, ok := v.(bool); ok && vb {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		b.Fatal(err)
	}
	orders := make([]map[string]any, 100)
	for i := range orders {
		orders[i] = map[string]any{"price": int64(i)} // predicate true at i=51
	}
	prog := mustCompile(b, e, "any(orders, price > 50)")
	data := map[string]any{"orders": orders}
	b.ResetTimer()
	for b.Loop() {
		_, _ = prog.Eval(data)
	}
}

// --- EvalContext overhead vs plain Eval -------------------------------------------
// Validates the claim that cooperative cancellation costs ~one counter
// increment per node when a live context is attached, and nothing when not.

func BenchmarkEvalNoContext(b *testing.B) {
	prog := mustCompile(b, NewEngine(), "user.VIP && amount > 100 && status in ['active', 'trial']")
	data := benchData()
	for b.Loop() {
		_, _ = prog.Eval(data)
	}
}

func BenchmarkEvalWithContext(b *testing.B) {
	prog := mustCompile(b, NewEngine(), "user.VIP && amount > 100 && status in ['active', 'trial']")
	data := benchData()
	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		_, _ = prog.EvalContext(ctx, data)
	}
}

func BenchmarkInSlice100kWithContext(b *testing.B) {
	big := make([]int64, 100_000)
	prog := mustCompile(b, NewEngine(), "1 in big")
	data := map[string]any{"big": big}
	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		_, _ = prog.EvalContext(ctx, data)
	}
}

// --- parallel eval (Program is immutable and shareable) ----------------------------

func BenchmarkEvalParallel(b *testing.B) {
	prog := mustCompile(b, NewEngine(),
		"user.VIP && amount > 100 && status in ['active', 'trial'] ? amount * 8 / 10 : amount")
	data := benchData()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = prog.Eval(data)
		}
	})
}

// Guard: the benchmarks above must actually produce the expected values —
// a benchmark that silently evaluates to an error measures the wrong thing.
func TestBenchExpressionsAreValid(t *testing.T) {
	e := NewEngine()
	data := benchData()
	for _, src := range []string{
		"user.Age",
		"user.Scores[1]",
		"user.Discount()",
		"startsWith(status, 'act')",
		"has(user, 'Age') ? get(user, 'Age', 0) : 0",
		"user.VIP && amount > 100 && status in ['active', 'trial'] ? amount * 8 / 10 : amount",
	} {
		if _, err := e.Eval(src, data); err != nil {
			t.Fatalf("%s: %v", src, err)
		}
	}
	if v, err := e.Eval("user.VIP && amount > 100 && status in ['active', 'trial'] ? amount * 8 / 10 : amount", data); err != nil || v != int64(200) {
		t.Fatalf("typical rule: got %v, %v", v, err)
	}
}
