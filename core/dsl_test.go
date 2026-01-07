package core

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// -----------------------------------------------------------------------------
// Mock Data Structures
// -----------------------------------------------------------------------------

type Metadata struct {
	ID     int64
	Detail map[string]any
}

type TagTest struct {
	ComplexField string `okra:"real_name,omitempty"`
	IgnoredField string `okra:"-"`
}

type Product struct {
	Name  string
	Price float64
	Meta  *Metadata
}

func (p Product) Discount(ratio float64) float64 { return p.Price * ratio }
func (p *Product) IsExpensive() bool             { return p.Price > 1000 }

type TestUser struct {
	Name string `json:"name_tag"`
	Age  int
}

func (u TestUser) GetName() string                { return u.Name }
func (u *TestUser) PointerMethod() string         { return "pointer" }
func (u TestUser) SayHi(prefix string) string     { return prefix + " " + u.Name }
func (u TestUser) ErrorMethod() (string, error)   { return "", errors.New("expected error") }
func (u TestUser) MultiReturn() (string, error)   { return "ok", nil }
func (u TestUser) Variadic(args ...string) string { return strings.Join(args, ",") }

// -----------------------------------------------------------------------------
// Main Test Suite
// -----------------------------------------------------------------------------

func TestEngine_Eval(t *testing.T) {
	engine := NewEngine()
	user := &TestUser{Name: "Alice", Age: 25}

	data := map[string]any{
		"user":    user,
		"active":  true,
		"tags":    []string{"go", "okra"},
		"scores":  map[int]string{1: "Gold", 2: "Silver"},
		"nums":    []int{10, 20},
		"i":       1,
		"nested":  map[string]any{"key": "val"},
		"val_i":   10,
		"val_f":   10.5,
		"u":       uint(50),
		"nil_val": nil,
		"ptr_nil": (*TestUser)(nil),
		"p": Product{
			Name:  "Smartphone",
			Price: 999.99,
			Meta: &Metadata{
				ID:     1001,
				Detail: map[string]any{"color": "black", "stock": 50},
			},
		},
		"matrix":   [][]int{{1, 2}, {3, 4}},
		"tag_test": TagTest{ComplexField: "tagged_value"},
	}

	tests := []struct {
		expr    string
		want    any
		wantErr bool
	}{
		// 1. Literals & Basic Variables
		{"true", true, false},
		{"false", false, false},
		{"'hello'", "hello", false},
		{"'a\\nb'", "a\nb", false},
		{"'\\t'", "\t", false},
		{"'\\\\'", "\\", false},
		{"'\\x41'", "A", false},
		{"'\\101'", "A", false},
		{"'\\u0041'", "A", false},
		{"'\\U00000041'", "A", false},
		{"'it\\'s'", "it's", false},
		{"123", int64(123), false},
		{"123.4", 123.4, false},
		{"active", true, false},
		{"user.Name", "Alice", false},
		{"user.name_tag", "Alice", false},
		{"tag_test.real_name", "tagged_value", false},

		// 2. Comprehensive Math (evalMath)
		{"10 + 20", int64(30), false},
		{"20 - 5", int64(15), false},
		{"10 * 3", int64(30), false},
		{"10 / 2", int64(5), false},
		{"10 % 3", int64(1), false},
		{"10 % 0", nil, true},
		{"10.5 + 2.5", 13.0, false},
		{"10.5 - 0.5", 10.0, false},
		{"2.0 * 3.5", 7.0, false},
		{"10.0 / 4.0", 2.5, false},
		{"'res: ' + 10", "res: 10", false},
		{"10 / 0", nil, true},
		{"10.5 / 0", nil, true},
		{"'\\q'", nil, true},

		// 3. Comparisons (compare)
		{"10 > 5", true, false},
		{"5 < 10", true, false},
		{"10 >= 10", true, false},
		{"9 <= 10", true, false},
		{"10.5 > 10", true, false},
		{"9.5 < 10.0", true, false},
		{"user.Age == 25", true, false},
		{"user.Age != 20", true, false},
		{"10 == 10.0", true, false},
		{"'a' == 'a'", true, false},
		{"'a' == 'b'", false, false},
		{"'a' != 'a'", false, false},
		{"'a' != 'b'", true, false},

		// 3.5 Unary operators
		{"!true", false, false},
		{"!false", true, false},
		{"-1", int64(-1), false},
		{"-1.5", -1.5, false},
		{"-(1 + 2)", int64(-3), false},
		{"~0", int64(-1), false},

		// 4. Logic & Short-circuit
		{"active && false", false, false},
		{"active || false", true, false},
		{"false || true", true, false},
		{"false && (1/0)", false, false},
		{"true || (1/0)", true, false},

		// 4.5 Bitwise operators
		{"5 & 3", int64(1), false},
		{"5 | 2", int64(7), false},
		{"5 ^ 1", int64(4), false},
		{"1 << 3", int64(8), false},
		{"8 >> 2", int64(2), false},
		{"1 << -1", nil, true},

		// 5. Member Access & Methods
		{"tags[0]", "go", false},
		{"tags[i]", "okra", false},
		{"scores[1]", "Gold", false},
		{"scores[0+1]", "Gold", false},
		{"scores.1", "Gold", false},
		{"nums.1", 20, false},
		{"user.GetName()", "Alice", false},
		{"user.GetName", "Alice", false}, // Getter mode
		{"user.PointerMethod()", "pointer", false},
		{"user.SayHi('Hi')", "Hi Alice", false},
		{"user.Variadic('a', 'b')", "a,b", false},
		{"p.Meta.Detail.color", "black", false},
		{"matrix[0][1]", 2, false},
		{"u + 10", int64(60), false}, // Uint test

		// 6. OO Sugar & Built-ins
		{"len(tags)", int64(2), false},
		{"tags.len()", int64(2), false},
		{"'abc'.len()", int64(3), false},
		{"user.GetName().len()", int64(5), false},
		{"now() > 0", true, false},

		// 6.5 Ternary operator
		{"true ? 1 : 2", int64(1), false},
		{"false ? 1 : 2", int64(2), false},
		{"false && (1/0) ? 1 : 2", int64(2), false},
		{"true || false ? 1 : 2", int64(1), false},
		{"false ? 1 : true ? 2 : 3", int64(2), false},

		// 7. Error & Corner Cases
		{"(1 + 2)", int64(3), false},
		{"(1 + 2", nil, true},
		{"'unclosed", nil, true},
		{"user.SayHi()", nil, true},
		{"user.SayHi('a', 'b')", nil, true},
		{"user.ErrorMethod()", nil, true},
		{"nil_val.prop", nil, false},
		{"ptr_nil.Name", nil, false},
		{"1 > 'a'", nil, true},
		{"unknown_func()", nil, true},
		{"1 + / 2", nil, true},
		{"true ? 1", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			got, err := engine.Eval(tt.expr, data)
			if (err != nil) != tt.wantErr {
				t.Errorf("Eval(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
				return
			}
			if !tt.wantErr && fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("Eval(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})

		t.Run("MethodCall len non-nil pointer deref", func(t *testing.T) {
			engine := NewEngine()
			s := []int{1, 2, 3}
			ps := &s
			res, err := engine.Eval("ps.len()", map[string]any{"ps": ps})
			if err != nil {
				t.Fatal(err)
			}
			if res != int64(3) {
				t.Fatalf("want 3, got %v", res)
			}
		})
	}
}

func TestEvalTo_Generics(t *testing.T) {
	engine := NewEngine()
	data := map[string]any{"val": 42}

	// Success
	res, err := EvalTo[int](engine, "val", data)
	if err != nil || res != 42 {
		t.Errorf("EvalTo[int] failed: %v", err)
	}

	// Conversion Failure
	_, err = EvalTo[bool](engine, "val", data)
	if err == nil {
		t.Error("Expected conversion error for int to bool")
	}
}

func TestEngine_Concurrency(t *testing.T) {
	engine := NewEngine()
	data := map[string]any{"n": 1}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(v int) {
			defer wg.Done()
			res, _ := engine.Eval(fmt.Sprintf("n + %d", v), data)
			if fmt.Sprint(res) != fmt.Sprint(v+1) {
				t.Errorf("Concurrency fail")
			}
		}(i)
	}
	wg.Wait()
}

func TestAllStringRepresentations(t *testing.T) {
	exprs := []string{
		"123", "'hello'", "true", "varName",
		"a.b", "a[0]", "a.b(c, d)", "func(a)",
		"(a + b)", "(a && b)", "(a > b)",
	}
	for _, s := range exprs {
		e, err := ParseExpr(s)
		if err != nil {
			t.Fatalf("Parse failed for %s: %v", s, err)
		}
		if e.String() == "" {
			t.Errorf("String representation of %s is empty", s)
		}
	}
}

func TestAllNodes_String(t *testing.T) {
	tests := []struct {
		name string
		expr Expr
		want string
	}{
		{
			name: "Literal Int",
			expr: &LiteralExpr{Value: 100},
			want: "100",
		},
		{
			name: "Literal String",
			expr: &LiteralExpr{Value: "okra"},
			want: `"okra"`,
		},
		{
			name: "Variable",
			expr: &VariableExpr{Name: "user"},
			want: "user",
		},
		{
			name: "Member Access (Dot)",
			expr: &MemberAccessExpr{
				Left:    &VariableExpr{Name: "user"},
				Key:     "Age",
				IsIndex: false,
			},
			want: "user.Age",
		},
		{
			name: "Member Access (Index)",
			expr: &MemberAccessExpr{
				Left:    &VariableExpr{Name: "tags"},
				Key:     "0",
				IsIndex: true,
			},
			want: "tags[0]",
		},
		{
			name: "Method Call",
			expr: &MethodCallExpr{
				Left:   &VariableExpr{Name: "user"},
				Method: "GetName",
				Args: []Expr{
					&LiteralExpr{Value: "prefix"},
				},
			},
			want: `user.GetName("prefix")`,
		},
		{
			name: "Function Call",
			expr: &CallExpr{
				Name: "len",
				Args: []Expr{
					&VariableExpr{Name: "tags"},
				},
			},
			want: "len(tags)",
		},
		{
			name: "Infix Expression (Math)",
			expr: &InfixExpr{
				Left:  &VariableExpr{Name: "a"},
				Op:    "+",
				Right: &LiteralExpr{Value: 1},
			},
			want: "(a + 1)",
		},
		{
			name: "Infix Expression (Logical)",
			expr: &InfixExpr{
				Left:  &VariableExpr{Name: "active"},
				Op:    "&&",
				Right: &VariableExpr{Name: "valid"},
			},
			want: "(active && valid)",
		},
		{
			name: "Unary Expression (Not)",
			expr: &UnaryExpr{
				Op:    "!",
				Right: &VariableExpr{Name: "active"},
			},
			want: "(!active)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.expr.String()
			if got != tt.want {
				t.Errorf("%s: got %s, want %s", tt.name, got, tt.want)
			}
		})
	}
}

func BenchmarkLargeExpression(b *testing.B) {
	engine := NewEngine()
	// Stay just below the limit to test performance
	count := 90
	expr := strings.Repeat("1 + ", count) + "1"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = engine.Eval(expr, nil)
	}
}

type User struct {
	Name    string `okra:"name"`
	Age     int    `okra:"age"`
	Scores  []int
	Profile Profile
}

type Profile struct {
	Level string
}

func (u *User) GetStatus(prefix string) string {
	return prefix + ": " + u.Name
}

func TestOkra_Evaluation(t *testing.T) {
	engine := NewEngine()
	user := &User{Name: "Gemini", Age: 5}

	t.Run("Basic Math and Logic", func(t *testing.T) {
		res, _ := engine.Eval("1 + 2 * 3 == 7 && true", nil)
		if res != true {
			t.Errorf("Expected true, got %v", res)
		}
	})

	t.Run("Struct Member Access", func(t *testing.T) {
		res, _ := engine.Eval("name == 'Gemini'", user)
		if res != true {
			t.Errorf("Expected true, got %v", res)
		}
	})

	t.Run("ParseExpr invalid token", func(t *testing.T) {
		_, err := ParseExpr("@1")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("UnaryExpr error branches", func(t *testing.T) {
		engine := NewEngine()
		_, err := engine.Eval("-('a')", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		_, err = engine.Eval("~1.2", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		_, err = engine.Eval("-(1/0)", nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("InfixExpr error branches", func(t *testing.T) {
		engine := NewEngine()
		_, err := engine.Eval("(1/0) + 1", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		_, err = engine.Eval("1 + (1/0)", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		_, err = engine.Eval("true && (1/0)", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		_, err = engine.Eval("false || (1/0)", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		_, err = engine.Eval("1 & 'a'", nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("CallExpr arg eval error", func(t *testing.T) {
		engine := NewEngine()
		_, err := engine.Eval("len(1/0)", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		res, err := engine.Eval("len(1)", nil)
		if err != nil {
			t.Fatal(err)
		}
		if res != int64(0) {
			t.Fatalf("expected 0, got %v", res)
		}
	})

	t.Run("MethodCallExpr arg eval error", func(t *testing.T) {
		engine := NewEngine()
		user := &TestUser{Name: "Alice"}
		_, err := engine.Eval("user.SayHi(1/0)", map[string]any{"user": user})
		if err == nil {
			t.Fatal("expected error")
		}
		_, err = engine.Eval("1.len()", nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

type panicObj struct{}

func (p *panicObj) Boom() int {
	panic("boom")
}

func (p *panicObj) Void() {}

func (p *panicObj) Needs(a int) int { return a }

func (p *panicObj) VariadicNeedOne(prefix string, args ...string) string {
	return prefix + strings.Join(args, ",")
}

func (p *panicObj) IsNilPtr(v *int) bool { return v == nil }

func TestCoverage_EdgeCases(t *testing.T) {
	t.Run("NewEngine len with 0 args", func(t *testing.T) {
		engine := NewEngine()
		res, err := engine.Eval("len()", nil)
		if err != nil {
			t.Fatal(err)
		}
		if res != 0 {
			t.Fatalf("want 0, got %v", res)
		}
	})

	t.Run("Engine RegisterFunc", func(t *testing.T) {
		engine := NewEngine()
		err := engine.RegisterFunc("add", func(args []any) (any, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("expected 2 args")
			}
			a, ok := toInt64(args[0])
			if !ok {
				return nil, fmt.Errorf("bad arg")
			}
			b, ok := toInt64(args[1])
			if !ok {
				return nil, fmt.Errorf("bad arg")
			}
			return a + b, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		res, err := engine.Eval("add(1, 2)", nil)
		if err != nil {
			t.Fatal(err)
		}
		if res != int64(3) {
			t.Fatalf("expected 3, got %v", res)
		}

		// override built-in on current engine only
		err = engine.RegisterFunc("len", func(args []any) (any, error) { return int64(123), nil })
		if err != nil {
			t.Fatal(err)
		}
		res, err = engine.Eval("len(tags)", map[string]any{"tags": []int{1, 2}})
		if err != nil {
			t.Fatal(err)
		}
		if res != int64(123) {
			t.Fatalf("expected 123, got %v", res)
		}

		engine2 := NewEngine()
		res, err = engine2.Eval("len(tags)", map[string]any{"tags": []int{1, 2}})
		if err != nil {
			t.Fatal(err)
		}
		if res != int64(2) {
			t.Fatalf("expected 2, got %v", res)
		}

		// validation
		if err := engine.RegisterFunc("", func(args []any) (any, error) { return nil, nil }); err == nil {
			t.Fatal("expected error")
		}
		if err := engine.RegisterFunc("x", nil); err == nil {
			t.Fatal("expected error")
		}

		// lazy init for zero Engine
		var zero Engine
		if err := zero.RegisterFunc("one", func(args []any) (any, error) { return int64(1), nil }); err != nil {
			t.Fatal(err)
		}
		res, err = zero.Eval("one()", nil)
		if err != nil {
			t.Fatal(err)
		}
		if res != int64(1) {
			t.Fatalf("expected 1, got %v", res)
		}
	})

	t.Run("Engine Eval recovers panic", func(t *testing.T) {
		engine := NewEngine()
		_, err := engine.Eval("p.Boom()", map[string]any{"p": &panicObj{}})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("Engine Eval top-level recover", func(t *testing.T) {
		engine := NewEngine()
		// getMember(map[bool]...) will panic when trying to Convert(int64 -> bool) for numeric key access.
		_, err := engine.Eval("m.1", map[string]any{"m": map[bool]string{true: "t"}})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("IndexExpr branches", func(t *testing.T) {
		engine := NewEngine()

		// left eval error
		_, err := engine.Eval("(1/0)[0]", nil)
		if err == nil {
			t.Fatal("expected error")
		}

		// left is nil -> nil
		res, err := engine.Eval("x[0]", map[string]any{"x": nil})
		if err != nil {
			t.Fatal(err)
		}
		if res != nil {
			t.Fatalf("expected nil, got %v", res)
		}

		// index eval error
		res, err = engine.Eval("arr[1/0]", map[string]any{"arr": []int{1}})
		if err == nil {
			t.Fatal("expected error")
		}
		_ = res

		// pointer to slice nil
		var ps *[]int
		res, err = engine.Eval("ps[0]", map[string]any{"ps": ps})
		if err != nil {
			t.Fatal(err)
		}
		if res != nil {
			t.Fatalf("expected nil, got %v", res)
		}

		// pointer to slice non-nil (covers deref path)
		s := []int{1}
		ps2 := &s
		res, err = engine.Eval("ps2[0]", map[string]any{"ps2": ps2})
		if err != nil {
			t.Fatal(err)
		}
		if res != 1 {
			t.Fatalf("expected 1, got %v", res)
		}

		// slice index: non-int / negative / out of range
		res, err = engine.Eval("arr['x']", map[string]any{"arr": []int{1}})
		if err != nil {
			t.Fatal(err)
		}
		if res != nil {
			t.Fatalf("expected nil, got %v", res)
		}
		res, err = engine.Eval("arr[-1]", map[string]any{"arr": []int{1}})
		if err != nil {
			t.Fatal(err)
		}
		if res != nil {
			t.Fatalf("expected nil, got %v", res)
		}
		res, err = engine.Eval("arr[99]", map[string]any{"arr": []int{1}})
		if err != nil {
			t.Fatal(err)
		}
		if res != nil {
			t.Fatalf("expected nil, got %v", res)
		}

		// map: idx nil -> zero key
		res, err = engine.Eval("m[k]", map[string]any{"m": map[int]string{0: "zero"}, "k": nil})
		if err != nil {
			t.Fatal(err)
		}
		if res != "zero" {
			t.Fatalf("expected zero, got %v", res)
		}

		// map: assignable key (int)
		res, err = engine.Eval("m[i]", map[string]any{"m": map[int]string{1: "one"}, "i": 1})
		if err != nil {
			t.Fatal(err)
		}
		if res != "one" {
			t.Fatalf("expected one, got %v", res)
		}

		// map: convertible key (int64 -> int)
		res, err = engine.Eval("m[1]", map[string]any{"m": map[int]string{1: "one"}})
		if err != nil {
			t.Fatal(err)
		}
		if res != "one" {
			t.Fatalf("expected one, got %v", res)
		}

		// map: string -> int key
		res, err = engine.Eval("m['1']", map[string]any{"m": map[int]string{1: "one"}})
		if err != nil {
			t.Fatal(err)
		}
		if res != "one" {
			t.Fatalf("expected one, got %v", res)
		}
		res, err = engine.Eval("m['x']", map[string]any{"m": map[int]string{1: "one"}})
		if err != nil {
			t.Fatal(err)
		}
		if res != nil {
			t.Fatalf("expected nil, got %v", res)
		}

		// map: string -> uint key
		res, err = engine.Eval("m['1']", map[string]any{"m": map[uint]string{1: "one"}})
		if err != nil {
			t.Fatal(err)
		}
		if res != "one" {
			t.Fatalf("expected one, got %v", res)
		}
		res, err = engine.Eval("m['x']", map[string]any{"m": map[uint]string{1: "one"}})
		if err != nil {
			t.Fatal(err)
		}
		if res != nil {
			t.Fatalf("expected nil, got %v", res)
		}

		// map: unsupported key kind
		res, err = engine.Eval("m['x']", map[string]any{"m": map[bool]string{true: "t"}})
		if err != nil {
			t.Fatal(err)
		}
		if res != nil {
			t.Fatalf("expected nil, got %v", res)
		}

		// map: idx not convertible / not string
		type dummy struct{}
		res, err = engine.Eval("m[d]", map[string]any{"m": map[int]string{1: "one"}, "d": dummy{}})
		if err != nil {
			t.Fatal(err)
		}
		if res != nil {
			t.Fatalf("expected nil, got %v", res)
		}

		// map: missing key
		res, err = engine.Eval("m[2]", map[string]any{"m": map[int]string{1: "one"}})
		if err != nil {
			t.Fatal(err)
		}
		if res != nil {
			t.Fatalf("expected nil, got %v", res)
		}

		// default kind (non-indexable)
		res, err = engine.Eval("n[0]", map[string]any{"n": 123})
		if err != nil {
			t.Fatal(err)
		}
		if res != nil {
			t.Fatalf("expected nil, got %v", res)
		}
	})

	t.Run("Index parsing branches", func(t *testing.T) {
		engine := NewEngine()
		// missing ]
		_, err := engine.Eval("arr[0", map[string]any{"arr": []int{1}})
		if err == nil {
			t.Fatal("expected error")
		}
		// index expression parse error (covers led([) error propagation)
		_, err = engine.Eval("arr[1+]", map[string]any{"arr": []int{1}})
		if err == nil {
			t.Fatal("expected error")
		}
		// legacy dot-bracket form still works
		res, err := engine.Eval("arr.[0]", map[string]any{"arr": []int{1}})
		if err != nil {
			t.Fatal(err)
		}
		if res != 1 {
			t.Fatalf("expected 1, got %v", res)
		}
		// missing ] in legacy form
		_, err = engine.Eval("arr.[0", map[string]any{"arr": []int{1}})
		if err == nil {
			t.Fatal("expected error")
		}
		// index expression parse error in legacy form
		_, err = engine.Eval("arr.[1+]", map[string]any{"arr": []int{1}})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("Lexer error propagation (invalid escape)", func(t *testing.T) {
		engine := NewEngine()

		// lexer error happens during parse()'s first advance (reading the 3rd token)
		_, err := engine.Eval("1 + '\\q'", nil)
		if err == nil {
			t.Fatal("expected error")
		}

		// lexer error happens during parse()'s for-loop advance (reading token after the right operand)
		_, err = engine.Eval("1 + 2 '\\q'", nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("MethodCall len nil ptr", func(t *testing.T) {
		engine := NewEngine()
		var s *[]int
		res, err := engine.Eval("s.len()", map[string]any{"s": s})
		if err != nil {
			t.Fatal(err)
		}
		if res != int64(0) {
			t.Fatalf("want 0, got %v", res)
		}
	})

	t.Run("MethodCall nil object", func(t *testing.T) {
		engine := NewEngine()
		_, err := engine.Eval("x.len()", map[string]any{"x": nil})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("MethodCall len on non-sized kind", func(t *testing.T) {
		engine := NewEngine()
		_, err := engine.Eval("n.len()", map[string]any{"n": 123})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("Ternary condition error", func(t *testing.T) {
		engine := NewEngine()
		_, err := engine.Eval("(1/0) ? 1 : 2", nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("Ternary missing colon", func(t *testing.T) {
		engine := NewEngine()
		_, err := engine.Eval("true ? 1", nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("Ternary else parse error", func(t *testing.T) {
		engine := NewEngine()
		_, err := engine.Eval("true ? 1 : 1 + / 2", nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("ParseArgs missing close paren", func(t *testing.T) {
		engine := NewEngine()
		_, err := engine.Eval("len(tags", map[string]any{"tags": []int{1}})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("ParseArgs error in method call", func(t *testing.T) {
		engine := NewEngine()
		user := &TestUser{Name: "Alice", Age: 1}
		_, err := engine.Eval("user.SayHi(", map[string]any{"user": user})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("ParseArgs arg parse error", func(t *testing.T) {
		engine := NewEngine()
		_, err := engine.Eval("len(1+)", nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("ParseExpr extra token", func(t *testing.T) {
		_, err := ParseExpr("1 2")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("Engine Eval unexpected trailing token", func(t *testing.T) {
		engine := NewEngine()
		_, err := engine.Eval("1 2", nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("ParseExpr stack overflow", func(t *testing.T) {
		engine := NewEngine()
		open := strings.Repeat("(", MaxStackDepth+2)
		close := strings.Repeat(")", MaxStackDepth+2)
		_, err := engine.Eval(open+"1"+close, nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("toBool branches", func(t *testing.T) {
		if toBool(nil) {
			t.Fatal("expected false")
		}
		if !toBool(true) {
			t.Fatal("expected true")
		}
		if toBool(0) {
			t.Fatal("expected false")
		}
		if !toBool(1) {
			t.Fatal("expected true")
		}
		if toBool("") {
			t.Fatal("expected false")
		}
		if toBool("false") {
			t.Fatal("expected false")
		}
		if !toBool("x") {
			t.Fatal("expected true")
		}
	})

	t.Run("toInt64/toFloat branches", func(t *testing.T) {
		if _, ok := toInt64(nil); ok {
			t.Fatal("expected false")
		}
		if _, ok := toInt64("x"); ok {
			t.Fatal("expected false")
		}
		if _, ok := toFloat("x"); ok {
			t.Fatal("expected false")
		}
		if f, ok := toFloat("1.25"); !ok || f != 1.25 {
			t.Fatalf("unexpected: %v %v", f, ok)
		}
		if f, ok := toFloat(float32(1.25)); !ok || f != 1.25 {
			t.Fatalf("unexpected: %v %v", f, ok)
		}
		if _, ok := toFloat(struct{}{}); ok {
			t.Fatal("expected false")
		}
	})

	t.Run("EvalTo more branches", func(t *testing.T) {
		engine := NewEngine()
		// targetType == nil branch
		type nonEmptyIface interface{ Foo() }
		_, err := EvalTo[nonEmptyIface](engine, "1", nil)
		if err == nil {
			t.Fatal("expected error")
		}

		v1, err := EvalTo[int64](engine, "123", nil)
		if err != nil || v1 != 123 {
			t.Fatalf("unexpected: %v %v", v1, err)
		}
		v1b, err := EvalTo[int32](engine, "123", nil)
		if err != nil || v1b != 123 {
			t.Fatalf("unexpected: %v %v", v1b, err)
		}
		v2, err := EvalTo[float64](engine, "'1.5'", nil)
		if err != nil || v2 != 1.5 {
			t.Fatalf("unexpected: %v %v", v2, err)
		}
		v2b, err := EvalTo[int](engine, "'1.5'", nil)
		if err != nil || v2b != 1 {
			t.Fatalf("unexpected: %v %v", v2b, err)
		}
		_, err = EvalTo[int](engine, "'abc'", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		_, err = EvalTo[int](engine, "1/0", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		_, err = EvalTo[*int](engine, "nil_val", map[string]any{"nil_val": nil})
		if err == nil {
			t.Fatal("expected error")
		}
		s, err := EvalTo[string](engine, "'abc'", nil)
		if err != nil || s != "abc" {
			t.Fatalf("unexpected: %v %v", s, err)
		}
		type MyStr string
		ms, err := EvalTo[MyStr](engine, "'abc'", nil)
		if err != nil || ms != MyStr("abc") {
			t.Fatalf("unexpected: %v %v", ms, err)
		}
	})

	t.Run("evalBitwise errors", func(t *testing.T) {
		if _, err := evalBitwise(int64(1), int64(2), "?"); err == nil {
			t.Fatal("expected error")
		}
		if _, err := evalBitwise("a", int64(1), "&"); err == nil {
			t.Fatal("expected error")
		}
		if _, err := evalBitwise(int64(1), int64(-1), ">>"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("compare unknown op", func(t *testing.T) {
		if _, err := compare(1, 2, "?"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("evalMath float modulo fallthrough", func(t *testing.T) {
		res, err := evalMath(1.2, 2.0, '%')
		if err != nil {
			t.Fatal(err)
		}
		if res != nil {
			t.Fatalf("expected nil, got %v", res)
		}
		_, err = evalMath(1.2, 0.0, '/')
		if err == nil {
			t.Fatal("expected error")
		}
		res, err = evalMath(int64(1), int64(2), '?')
		if err != nil {
			t.Fatal(err)
		}
		if res != nil {
			t.Fatalf("expected nil, got %v", res)
		}
	})

	t.Run("callReflectMethod branches", func(t *testing.T) {
		obj := &panicObj{}
		if _, err := callReflectMethod(obj, "Missing", nil); err == nil {
			t.Fatal("expected error")
		}
		if _, err := callReflectMethod(obj, "Needs", []any{1, 2}); err == nil {
			t.Fatal("expected error")
		}
		if _, err := callReflectMethod(obj, "Needs", []any{"x"}); err == nil {
			t.Fatal("expected error")
		}
		if _, err := callReflectMethod(obj, "VariadicNeedOne", []any{}); err == nil {
			t.Fatal("expected error")
		}
		if res, err := callReflectMethod(obj, "IsNilPtr", []any{nil}); err != nil || res != true {
			t.Fatalf("unexpected: %v %v", res, err)
		}
		if res, err := callReflectMethod(obj, "Needs", []any{nil}); err != nil || res != 0 {
			t.Fatalf("unexpected: %v %v", res, err)
		}
		if _, err := callReflectMethod(obj, "Void", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := callReflectMethod(obj, "Boom", nil); err == nil {
			t.Fatal("expected error")
		}
		user := TestUser{Name: "Alice", Age: 1}
		if res, err := callReflectMethod(user, "MultiReturn", nil); err != nil || res != "ok" {
			t.Fatalf("unexpected: %v %v", res, err)
		}
	})

	t.Run("getMember branches", func(t *testing.T) {
		if v, _ := getMember(nil, "x"); v != nil {
			t.Fatalf("expected nil, got %v", v)
		}
		m := map[int]string{1: "one"}
		if v, _ := getMember(m, "abc"); v != nil {
			t.Fatalf("expected nil, got %v", v)
		}
		if v, _ := getMember([]int{1}, "5"); v != nil {
			t.Fatalf("expected nil, got %v", v)
		}
		u := TestUser{Name: "A", Age: 1}
		if v, _ := getMember(u, "SayHi"); v != nil {
			t.Fatalf("expected nil, got %v", v)
		}
		if v, _ := getMember(u, "PointerMethod"); v != nil {
			t.Fatalf("expected nil, got %v", v)
		}
		if v, _ := getMember(u, "MultiReturn"); v != "ok" {
			t.Fatalf("expected ok, got %v", v)
		}
		m2 := map[string]int{"a": 1}
		if v, _ := getMember(m2, "a"); v != 1 {
			t.Fatalf("expected 1, got %v", v)
		}
		if v, _ := getMember(m2, "b"); v != nil {
			t.Fatalf("expected nil, got %v", v)
		}
		if v, _ := getMember([]int{1}, "x"); v != nil {
			t.Fatalf("expected nil, got %v", v)
		}
		if v, _ := getMember(u, "DoesNotExist"); v != nil {
			t.Fatalf("expected nil, got %v", v)
		}
		if v, _ := getMember(&u, "PointerMethod"); v != "pointer" {
			t.Fatalf("expected pointer, got %v", v)
		}
	})

	t.Run("CallExpr data fallback arg eval error", func(t *testing.T) {
		engine := NewEngine()
		user := &TestUser{Name: "A"}
		_, err := engine.Eval("SayHi(1/0)", user)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("Unary right parse error", func(t *testing.T) {
		engine := NewEngine()
		_, err := engine.Eval("!/1", nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("Ternary then parse error", func(t *testing.T) {
		engine := NewEngine()
		_, err := engine.Eval("true ? 1 + / 2 : 3", nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("ParseExpr String for ternary", func(t *testing.T) {
		e, err := ParseExpr("true ? 1 : 2")
		if err != nil {
			t.Fatal(err)
		}
		if e.String() == "" {
			t.Fatal("expected non-empty")
		}
	})

	t.Run("UnaryExpr unknown op", func(t *testing.T) {
		u := &UnaryExpr{Op: "@", Right: &LiteralExpr{Value: 1}}
		if _, err := u.Eval(Context{}); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("InfixExpr unknown op", func(t *testing.T) {
		i := &InfixExpr{Left: &LiteralExpr{Value: 1}, Op: "@", Right: &LiteralExpr{Value: 2}}
		if v, err := i.Eval(Context{}); err != nil || v != nil {
			t.Fatalf("unexpected: %v %v", v, err)
		}
	})
}

func TestOkra_MethodCalls(t *testing.T) {
	engine := NewEngine()
	user := &User{Name: "Tester"}

	t.Run("Custom Receiver Method", func(t *testing.T) {
		res, err := engine.Eval(`GetStatus('Hello')`, user)
		if err != nil {
			t.Fatal(err)
		}
		if res != "Hello: Tester" {
			t.Errorf("Got %v", res)
		}
	})

	t.Run("Built-in len function", func(t *testing.T) {
		user.Scores = []int{1, 2, 3, 4, 5}
		res, _ := engine.Eval("len(Scores)", user)
		if res.(int64) != 5 {
			t.Errorf("Expected 5, got %v", res)
		}
	})
}

func TestOkra_Errors(t *testing.T) {
	engine := NewEngine()

	t.Run("Division by Zero", func(t *testing.T) {
		_, err := engine.Eval("10 / 0", nil)
		if err == nil || err.Error() != "div by zero" {
			t.Errorf("Expected div by zero error, got %v", err)
		}
	})

	t.Run("Invalid Assignment", func(t *testing.T) {
		// Cannot assign to a literal
		_, err := engine.Eval("10 = 20", nil)
		if err == nil {
			t.Error("Should have failed to assign to literal")
		}
	})
}
