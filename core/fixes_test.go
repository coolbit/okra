package core

import (
	"errors"
	"testing"
)

func TestArithmeticErrorsOnNonNumeric(t *testing.T) {
	e := NewEngine()
	// Previously these silently returned 0 / a wrong number.
	cases := []string{
		"'abc' * 2",
		"'abc' - 1",
		"nil_val * 5",
	}
	data := map[string]any{"nil_val": nil}
	for _, expr := range cases {
		if _, err := e.Eval(expr, data); err == nil {
			t.Fatalf("%s: expected error, got nil", expr)
		}
	}
}

func TestFloatModuloErrors(t *testing.T) {
	e := NewEngine()
	if _, err := e.Eval("10.5 % 2.0", nil); err == nil {
		t.Fatal("expected float modulo error")
	}
}

func TestLenBuiltinConsistency(t *testing.T) {
	e := NewEngine()
	arr := [3]int{1, 2, 3}
	data := map[string]any{"arr": arr, "ptr": &arr, "np": (*int)(nil), "s": "abcd"}

	// Arrays are now supported by the len() builtin (was returning 0).
	if v, err := e.Eval("len(arr)", data); err != nil || v != int64(3) {
		t.Fatalf("len(arr): got %v, %v", v, err)
	}
	// Pointers are dereferenced.
	if v, err := e.Eval("len(ptr)", data); err != nil || v != int64(3) {
		t.Fatalf("len(ptr): got %v, %v", v, err)
	}
	// Nil pointer is safe.
	if v, err := e.Eval("len(np)", data); err != nil || v != int64(0) {
		t.Fatalf("len(np): got %v, %v", v, err)
	}
	// String length as int64.
	if v, err := e.Eval("len(s)", data); err != nil || v != int64(4) {
		t.Fatalf("len(s): got %v, %v", v, err)
	}
}

func TestCompileReuse(t *testing.T) {
	e := NewEngine()
	prog, err := e.Compile("a + b")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := prog.Eval(map[string]any{"a": int64(1), "b": int64(2)}); err != nil || v != int64(3) {
		t.Fatalf("first eval: got %v, %v", v, err)
	}
	if v, err := prog.Eval(map[string]any{"a": int64(10), "b": int64(20)}); err != nil || v != int64(30) {
		t.Fatalf("second eval: got %v, %v", v, err)
	}

	// A Program is immutable: it snapshots funcs at Compile time, so a func
	// registered AFTER Compile does not affect it.
	prog2, err := e.Compile("triple(x)")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.RegisterFunc("triple", func(args []any) (any, error) {
		i, _ := toInt64(args[0])
		return i * 3, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := prog2.Eval(map[string]any{"x": int64(4)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("triple after compile: expected ErrNotFound (snapshot), got %v", err)
	}
	// Re-compiling after registration picks it up.
	prog3, err := e.Compile("triple(x)")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := prog3.Eval(map[string]any{"x": int64(4)}); err != nil || v != int64(12) {
		t.Fatalf("triple after recompile: got %v, %v", v, err)
	}

	// Compile-time errors are reported (and recovered) too.
	if _, err := e.Compile("1 +"); err == nil {
		t.Fatal("expected compile error")
	}
}
