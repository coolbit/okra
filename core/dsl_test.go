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
		{"10.5 + 2.5", 13.0, false},
		{"10.5 - 0.5", 10.0, false},
		{"2.0 * 3.5", 7.0, false},
		{"10.0 / 4.0", 2.5, false},
		{"'res: ' + 10", "res: 10", false},
		{"10 / 0", nil, true},
		{"10.5 / 0", nil, true},

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

		// 4. Logic & Short-circuit
		{"active && false", false, false},
		{"active || false", true, false},
		{"false && (1/0)", false, false},
		{"true || (1/0)", true, false},

		// 5. Member Access & Methods
		{"tags.[0]", "go", false},
		{"scores.[1]", "Gold", false},
		{"user.GetName()", "Alice", false},
		{"user.GetName", "Alice", false}, // Getter mode
		{"user.PointerMethod()", "pointer", false},
		{"user.SayHi('Hi')", "Hi Alice", false},
		{"user.Variadic('a', 'b')", "a,b", false},
		{"p.Meta.Detail.color", "black", false},
		{"matrix.[0].[1]", 2, false},
		{"u + 10", int64(60), false}, // Uint test

		// 6. OO Sugar & Built-ins
		{"len(tags)", int64(2), false},
		{"tags.len()", int64(2), false},
		{"'abc'.len()", int64(3), false},
		{"user.GetName().len()", int64(5), false},
		{"now() > 0", true, false},

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
		"a.b", "a.[0]", "a.b(c, d)", "func(a)",
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
