package core

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxStackDepth is the default parser nesting limit. It can be overridden per
// Engine via SetMaxNestingDepth. The limit bounds parser recursion (and hence
// AST depth and evaluation recursion), keeping deeply nested input from
// exhausting the Go stack.
const MaxStackDepth = 256

// Sentinel errors returned by evaluation. Callers can test for them with
// errors.Is even though they are wrapped with additional context.
var (
	ErrDivByZero     = errors.New("division by zero")
	ErrModByZero     = errors.New("modulo by zero")
	ErrFloatModulo   = errors.New("float modulo not supported")
	ErrNegativeShift = errors.New("negative shift count")
	ErrNotFound      = errors.New("function or method not found")
	// ErrUnknownField is returned in strict mode for a missing struct field,
	// map key, or out-of-range/nil access.
	ErrUnknownField = errors.New("unknown field or key")
	// ErrMethodDenied is returned when a method/getter call is blocked by the
	// Engine's method filter.
	ErrMethodDenied = errors.New("method not permitted")
)

// -----------------------------------------------------------------------------
// Core Types & Context
// -----------------------------------------------------------------------------

type CustomFunc func(args []any) (any, error)

type Context struct {
	Data any
	Fns  map[string]CustomFunc
	// Strict makes member/index access on a missing field, key, index, or nil
	// value return an error instead of nil. Off by default.
	Strict bool
	// MethodFilter, when non-nil, gates every reflected method and getter
	// invocation: names for which it returns false are denied. Nil allows all.
	MethodFilter func(name string) bool
}

// methodAllowed reports whether calling the named method/getter is permitted.
func (c Context) methodAllowed(name string) bool {
	return c.MethodFilter == nil || c.MethodFilter(name)
}

// miss handles a lookup that found nothing: in strict mode it is an error
// (wrapping ErrUnknownField); otherwise it resolves to nil like Go's zero-value
// lookups.
func (c Context) miss(format string, args ...any) (any, error) {
	if c.Strict {
		return nil, fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), ErrUnknownField)
	}
	return nil, nil
}

func evalBitwise(lv, rv any, op string) (any, error) {
	li, okL := toInt64(lv)
	ri, okR := toInt64(rv)
	if !okL || !okR {
		return nil, fmt.Errorf("invalid bitwise op %s between %T and %T", op, lv, rv)
	}
	switch op {
	case "&":
		return li & ri, nil
	case "|":
		return li | ri, nil
	case "^":
		return li ^ ri, nil
	case "<<":
		if ri < 0 {
			return nil, ErrNegativeShift
		}
		return li << uint64(ri), nil
	case ">>":
		if ri < 0 {
			return nil, ErrNegativeShift
		}
		return li >> uint64(ri), nil
	default:
		return nil, fmt.Errorf("unknown bitwise operator %q", op)
	}
}

type Expr interface {
	Eval(ctx Context) (any, error)
	String() string
}

// -----------------------------------------------------------------------------
// Metadata Cache
// -----------------------------------------------------------------------------

type structMeta struct {
	fields  map[string]int
	methods map[string]struct{}
}

var metaCache sync.Map

func getStructMeta(t reflect.Type) structMeta {
	if val, ok := metaCache.Load(t); ok {
		return val.(structMeta)
	}
	meta := structMeta{
		fields:  make(map[string]int),
		methods: make(map[string]struct{}),
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		// Unexported fields cannot be read via reflect.Value.Interface()
		// (it panics), so never expose them to expressions.
		if f.PkgPath != "" {
			continue
		}
		meta.fields[f.Name] = i
		tag := f.Tag.Get("okra")
		if tag == "" {
			tag = f.Tag.Get("json")
		}
		if tag != "" && tag != "-" {
			if idx := strings.Index(tag, ","); idx != -1 {
				tag = tag[:idx]
			}
			meta.fields[tag] = i
		}
	}
	for i := 0; i < t.NumMethod(); i++ {
		meta.methods[t.Method(i).Name] = struct{}{}
	}
	pt := reflect.PointerTo(t)
	for i := 0; i < pt.NumMethod(); i++ {
		meta.methods[pt.Method(i).Name] = struct{}{}
	}
	metaCache.Store(t, meta)
	return meta
}

// -----------------------------------------------------------------------------
// AST Nodes
// -----------------------------------------------------------------------------

type LiteralExpr struct{ Value any }

func (e *LiteralExpr) Eval(ctx Context) (any, error) { return e.Value, nil }
func (e *LiteralExpr) String() string {
	if s, ok := e.Value.(string); ok {
		// Emit okra's single-quoted string syntax so String() round-trips
		// back through ParseExpr. strconv.Quote gives a double-quoted Go
		// literal; swap the delimiters and fix the escaped quotes.
		q := strconv.Quote(s)
		q = strings.ReplaceAll(q[1:len(q)-1], `\"`, `"`)
		q = strings.ReplaceAll(q, `'`, `\'`)
		return "'" + q + "'"
	}
	return fmt.Sprint(e.Value)
}

type ListExpr struct{ Elems []Expr }

func (e *ListExpr) Eval(ctx Context) (any, error) {
	out := make([]any, len(e.Elems))
	for i, el := range e.Elems {
		v, err := el.Eval(ctx)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}
func (e *ListExpr) String() string {
	parts := make([]string, len(e.Elems))
	for i, el := range e.Elems {
		parts[i] = el.String()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

type VariableExpr struct{ Name string }

func (e *VariableExpr) Eval(ctx Context) (any, error) {
	return getMember(ctx, ctx.Data, e.Name)
}
func (e *VariableExpr) String() string { return e.Name }

type MemberAccessExpr struct {
	Left Expr
	Key  string
}

func (e *MemberAccessExpr) Eval(ctx Context) (any, error) {
	val, err := e.Left.Eval(ctx)
	if err != nil {
		return nil, err
	}
	if val == nil {
		return ctx.miss("cannot access %q on nil", e.Key)
	}
	return getMember(ctx, val, e.Key)
}
func (e *MemberAccessExpr) String() string {
	return fmt.Sprintf("%s.%s", e.Left.String(), e.Key)
}

type IndexExpr struct {
	Left  Expr
	Index Expr
}

func (e *IndexExpr) Eval(ctx Context) (any, error) {
	obj, err := e.Left.Eval(ctx)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return ctx.miss("cannot index nil")
	}
	idx, err := e.Index.Eval(ctx)
	if err != nil {
		return nil, err
	}

	rv := reflect.ValueOf(obj)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return ctx.miss("cannot index nil")
		}
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		i, ok := toInt64(idx)
		if !ok {
			return ctx.miss("non-integer index %v", idx)
		}
		if i < 0 || i >= int64(rv.Len()) {
			return ctx.miss("index %d out of range (len %d)", i, rv.Len())
		}
		return rv.Index(int(i)).Interface(), nil
	case reflect.Map:
		kt := rv.Type().Key()
		var kv reflect.Value
		if idx == nil {
			kv = reflect.Zero(kt)
		} else {
			v := reflect.ValueOf(idx)
			if v.Type().AssignableTo(kt) {
				kv = v
			} else if v.Type().ConvertibleTo(kt) {
				kv = v.Convert(kt)
			} else if s, ok := idx.(string); ok {
				switch kt.Kind() {
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					if i, err := strconv.ParseInt(s, 10, 64); err == nil {
						kv = reflect.ValueOf(i).Convert(kt)
					} else {
						return ctx.miss("invalid map key %q", s)
					}
				case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
					if u, err := strconv.ParseUint(s, 10, 64); err == nil {
						kv = reflect.ValueOf(u).Convert(kt)
					} else {
						return ctx.miss("invalid map key %q", s)
					}
				default:
					return ctx.miss("invalid map key %v", idx)
				}
			} else {
				return ctx.miss("invalid map key %v", idx)
			}
		}
		res := rv.MapIndex(kv)
		if !res.IsValid() {
			return ctx.miss("map has no key %v", idx)
		}
		return res.Interface(), nil
	}
	return ctx.miss("cannot index %T", obj)
}

func (e *IndexExpr) String() string {
	return fmt.Sprintf("%s[%s]", e.Left.String(), e.Index.String())
}

type MethodCallExpr struct {
	Left   Expr
	Method string
	Args   []Expr
}

func (e *MethodCallExpr) Eval(ctx Context) (any, error) {
	obj, err := e.Left.Eval(ctx)
	if err != nil || obj == nil {
		return nil, err
	}

	if e.Method == "len" && len(e.Args) == 0 {
		rv := reflect.ValueOf(obj)
		for rv.Kind() == reflect.Pointer {
			if rv.IsNil() {
				return int64(0), nil
			}
			rv = rv.Elem()
		}
		k := rv.Kind()
		if k == reflect.Slice || k == reflect.Array || k == reflect.Map || k == reflect.String {
			return int64(rv.Len()), nil
		}
	}

	if !ctx.methodAllowed(e.Method) {
		return nil, fmt.Errorf("%q: %w", e.Method, ErrMethodDenied)
	}

	args := make([]any, len(e.Args))
	for i, argExpr := range e.Args {
		v, err := argExpr.Eval(ctx)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	return callReflectMethod(obj, e.Method, args)
}
func (e *MethodCallExpr) String() string {
	var args []string
	for _, a := range e.Args {
		args = append(args, a.String())
	}
	return fmt.Sprintf("%s.%s(%s)", e.Left.String(), e.Method, strings.Join(args, ", "))
}

type CallExpr struct {
	Name string
	Args []Expr
}

func (e *CallExpr) Eval(ctx Context) (any, error) {
	// 1. Try to find a global function first
	fn, ok := ctx.Fns[strings.ToLower(e.Name)]
	if ok {
		args := make([]any, len(e.Args))
		for i, argExpr := range e.Args {
			v, err := argExpr.Eval(ctx)
			if err != nil {
				return nil, err
			}
			args[i] = v
		}
		return fn(args)
	}

	// 2. FALLBACK: Try to find the method on the root Data object. Only fall
	// through to "not found" when the method genuinely does not exist; if it
	// exists but fails, surface that real error instead of masking it.
	if ctx.Data != nil && hasMethod(ctx.Data, e.Name) {
		if !ctx.methodAllowed(e.Name) {
			return nil, fmt.Errorf("%q: %w", e.Name, ErrMethodDenied)
		}
		args := make([]any, len(e.Args))
		for i, argExpr := range e.Args {
			v, err := argExpr.Eval(ctx)
			if err != nil {
				return nil, err
			}
			args[i] = v
		}
		return callReflectMethod(ctx.Data, e.Name, args)
	}

	return nil, fmt.Errorf("%q: %w", e.Name, ErrNotFound)
}

func (e *CallExpr) String() string {
	var args []string
	for _, a := range e.Args {
		args = append(args, a.String())
	}
	return fmt.Sprintf("%s(%s)", e.Name, strings.Join(args, ", "))
}

type UnaryExpr struct {
	Op    string
	Right Expr
}

func (e *UnaryExpr) Eval(ctx Context) (any, error) {
	rv, err := e.Right.Eval(ctx)
	if err != nil {
		return nil, err
	}
	switch e.Op {
	case "!":
		return !toBool(rv), nil
	case "-":
		if i, ok := toInt64(rv); ok {
			return -i, nil
		}
		if f, ok := toFloat(rv); ok {
			return -f, nil
		}
		return nil, fmt.Errorf("invalid unary - for %T", rv)
	case "~":
		i, ok := toInt64(rv)
		if !ok {
			return nil, fmt.Errorf("invalid unary ~ for %T", rv)
		}
		return ^i, nil
	default:
		return nil, fmt.Errorf("unknown unary operator %q", e.Op)
	}
}

func (e *UnaryExpr) String() string {
	return fmt.Sprintf("(%s%s)", e.Op, e.Right.String())
}

type InfixExpr struct {
	Left  Expr
	Op    string
	Right Expr
}

func (e *InfixExpr) Eval(ctx Context) (any, error) {
	lv, err := e.Left.Eval(ctx)
	if err != nil {
		return nil, err
	}
	if e.Op == "&&" {
		if !toBool(lv) {
			return false, nil
		}
		rv, err := e.Right.Eval(ctx)
		if err != nil {
			return nil, err
		}
		return toBool(rv), nil
	}
	if e.Op == "||" {
		if toBool(lv) {
			return true, nil
		}
		rv, err := e.Right.Eval(ctx)
		if err != nil {
			return nil, err
		}
		return toBool(rv), nil
	}
	rv, err := e.Right.Eval(ctx)
	if err != nil {
		return nil, err
	}
	switch e.Op {
	case "==":
		return valuesEqual(lv, rv), nil
	case "!=":
		return !valuesEqual(lv, rv), nil
	case "+":
		if ls, ok := lv.(string); ok {
			return ls + fmt.Sprint(rv), nil
		}
		return evalMath(lv, rv, '+')
	case "-":
		return evalMath(lv, rv, '-')
	case "*":
		return evalMath(lv, rv, '*')
	case "/":
		return evalMath(lv, rv, '/')
	case "%":
		return evalMath(lv, rv, '%')
	case ">", "<", ">=", "<=":
		return compare(lv, rv, e.Op)
	case "&", "|", "^", "<<", ">>":
		return evalBitwise(lv, rv, e.Op)
	case "in":
		return evalIn(lv, rv)
	case "not in":
		v, err := evalIn(lv, rv)
		if err != nil {
			return nil, err
		}
		return !v.(bool), nil
	}
	return nil, nil
}

// valuesEqual implements the equality used by == / != / in: exact DeepEqual
// first, then a numeric comparison when both sides are numbers. A string is
// never treated as numerically equal to a number (so 1 == '1' is false).
func valuesEqual(lv, rv any) bool {
	// Fast path for common scalar types, avoiding reflect.DeepEqual.
	switch l := lv.(type) {
	case string:
		r, ok := rv.(string)
		return ok && l == r // a string never equals a number
	case bool:
		r, ok := rv.(bool)
		return ok && l == r
	case int64:
		if r, ok := rv.(int64); ok {
			return l == r
		}
	case float64:
		if r, ok := rv.(float64); ok {
			return l == r
		}
	}
	if isStringVal(lv) || isStringVal(rv) {
		return false
	}
	if reflect.DeepEqual(lv, rv) {
		return true
	}
	lf, okL := toFloat(lv)
	rf, okR := toFloat(rv)
	return okL && okR && lf == rf
}

func isStringVal(v any) bool {
	_, ok := v.(string)
	return ok
}

// evalIn implements `needle in haystack`: membership over slice/array
// elements or map keys, and substring for strings. It never panics.
func evalIn(needle, haystack any) (any, error) {
	if haystack == nil {
		return false, nil
	}
	if hs, ok := haystack.(string); ok {
		sub, ok := needle.(string)
		if !ok {
			return nil, fmt.Errorf("invalid 'in': need string on left, got %T", needle)
		}
		return strings.Contains(hs, sub), nil
	}
	rv := reflect.ValueOf(haystack)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return false, nil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			if valuesEqual(needle, rv.Index(i).Interface()) {
				return true, nil
			}
		}
		return false, nil
	case reflect.Map:
		for _, k := range rv.MapKeys() {
			if valuesEqual(needle, k.Interface()) {
				return true, nil
			}
		}
		return false, nil
	}
	return nil, fmt.Errorf("invalid 'in' on type %T", haystack)
}
func (e *InfixExpr) String() string {
	return fmt.Sprintf("(%s %s %s)", e.Left.String(), e.Op, e.Right.String())
}

type TernaryExpr struct {
	Cond Expr
	Then Expr
	Else Expr
}

func (e *TernaryExpr) Eval(ctx Context) (any, error) {
	cond, err := e.Cond.Eval(ctx)
	if err != nil {
		return nil, err
	}
	if toBool(cond) {
		return e.Then.Eval(ctx)
	}
	return e.Else.Eval(ctx)
}

func (e *TernaryExpr) String() string {
	return fmt.Sprintf("(%s ? %s : %s)", e.Cond.String(), e.Then.String(), e.Else.String())
}

// -----------------------------------------------------------------------------
// Reflection & Math Logic
// -----------------------------------------------------------------------------

func getMember(ctx Context, obj any, key string) (any, error) {
	if obj == nil {
		return ctx.miss("cannot access %q on nil", key)
	}
	rv := reflect.ValueOf(obj)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return ctx.miss("cannot access %q on nil", key)
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Map:
		kv := reflect.ValueOf(key)
		if !kv.Type().AssignableTo(rv.Type().Key()) {
			if i, err := strconv.ParseInt(key, 10, 64); err == nil {
				kv = reflect.ValueOf(i).Convert(rv.Type().Key())
			} else {
				return ctx.miss("map has no key %q", key)
			}
		}
		res := rv.MapIndex(kv)
		if !res.IsValid() {
			return ctx.miss("map has no key %q", key)
		}
		return res.Interface(), nil
	case reflect.Slice, reflect.Array:
		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx >= rv.Len() {
			return ctx.miss("index %q out of range (len %d)", key, rv.Len())
		}
		return rv.Index(idx).Interface(), nil
	case reflect.Struct:
		meta := getStructMeta(rv.Type())
		if idx, ok := meta.fields[key]; ok {
			return rv.Field(idx).Interface(), nil
		}
		if _, ok := meta.methods[key]; ok {
			if !ctx.methodAllowed(key) {
				return nil, fmt.Errorf("%q: %w", key, ErrMethodDenied)
			}
			m := rv.MethodByName(key)
			if !m.IsValid() && rv.CanAddr() {
				m = rv.Addr().MethodByName(key)
			}
			if m.IsValid() && m.Type().NumIn() == 0 && m.Type().NumOut() > 0 {
				return m.Call(nil)[0].Interface(), nil
			}
		}
		return ctx.miss("unknown field %q on %s", key, rv.Type())
	}
	return ctx.miss("cannot access %q on %T", key, obj)
}

// hasMethod reports whether obj (or its addressable pointer form) has an
// exported method called name. Used to distinguish "method absent" from
// "method present but failed" so real errors are not masked as not-found.
func hasMethod(obj any, name string) bool {
	rv := reflect.ValueOf(obj)
	if rv.MethodByName(name).IsValid() {
		return true
	}
	if rv.Kind() != reflect.Pointer && rv.IsValid() {
		addr := reflect.New(rv.Type())
		addr.Elem().Set(rv)
		return addr.MethodByName(name).IsValid()
	}
	return false
}

func callReflectMethod(obj any, name string, args []any) (any, error) {
	rv := reflect.ValueOf(obj)
	mv := rv.MethodByName(name)
	if !mv.IsValid() {
		if rv.Kind() != reflect.Pointer {
			// If obj is a non-pointer value held in an interface, it is not addressable.
			// Create an addressable copy so we can still call pointer-receiver methods.
			addr := reflect.New(rv.Type())
			addr.Elem().Set(rv)
			mv = addr.MethodByName(name)
		}
	}
	if !mv.IsValid() {
		return nil, fmt.Errorf("method %s not found on %T", name, obj)
	}

	mType := mv.Type()
	numIn := mType.NumIn()
	if mType.IsVariadic() {
		if len(args) < numIn-1 {
			return nil, fmt.Errorf("%s: expected at least %d args, got %d", name, numIn-1, len(args))
		}
	} else {
		if len(args) != numIn {
			return nil, fmt.Errorf("%s: expected %d args, got %d", name, numIn, len(args))
		}
	}

	in := make([]reflect.Value, len(args))
	for i, arg := range args {
		var t reflect.Type
		if mType.IsVariadic() && i >= numIn-1 {
			t = mType.In(numIn - 1).Elem()
		} else {
			t = mType.In(i)
		}
		if arg == nil {
			in[i] = reflect.Zero(t)
		} else {
			v := reflect.ValueOf(arg)
			if v.Type().ConvertibleTo(t) {
				in[i] = v.Convert(t)
			} else {
				return nil, fmt.Errorf("method %s arg %d: cannot use %T as %v", name, i, arg, t)
			}
		}
	}

	var out []reflect.Value
	var panicErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicErr = fmt.Errorf("panic calling %s: %v", name, r)
			}
		}()
		out = mv.Call(in)
	}()

	if panicErr != nil {
		return nil, panicErr
	}
	if len(out) == 0 {
		return nil, nil
	}
	if len(out) > 1 && out[len(out)-1].Type().Implements(reflect.TypeFor[error]()) {
		if !out[len(out)-1].IsNil() {
			return nil, out[len(out)-1].Interface().(error)
		}
		return out[0].Interface(), nil
	}
	return out[0].Interface(), nil
}

func evalMath(lv, rv any, op rune) (any, error) {
	li, okL := toInt64(lv)
	ri, okR := toInt64(rv)
	if okL && okR {
		switch op {
		case '+':
			return li + ri, nil
		case '-':
			return li - ri, nil
		case '*':
			return li * ri, nil
		case '/':
			if ri == 0 {
				return nil, ErrDivByZero
			}
			return li / ri, nil
		case '%':
			if ri == 0 {
				return nil, ErrModByZero
			}
			return li % ri, nil
		}
	}
	lf, okL := toFloat(lv)
	rf, okR := toFloat(rv)
	if !okL || !okR {
		return nil, fmt.Errorf("invalid arithmetic %c between %T and %T", op, lv, rv)
	}
	switch op {
	case '+':
		return lf + rf, nil
	case '-':
		return lf - rf, nil
	case '*':
		return lf * rf, nil
	case '/':
		if rf == 0 {
			return nil, ErrDivByZero
		}
		return lf / rf, nil
	case '%':
		return nil, ErrFloatModulo
	}
	return nil, nil
}

func compare(lv, rv any, op string) (bool, error) {
	// When both sides are strings, compare lexically. (Numeric strings still
	// compare numerically if one side is an actual number, via toFloat below.)
	if ls, ok := lv.(string); ok {
		if rs, ok := rv.(string); ok {
			switch op {
			case ">":
				return ls > rs, nil
			case "<":
				return ls < rs, nil
			case ">=":
				return ls >= rs, nil
			case "<=":
				return ls <= rs, nil
			}
		}
	}
	lf, okL := toFloat(lv)
	rf, okR := toFloat(rv)
	if !okL || !okR {
		return false, fmt.Errorf("invalid comparison between %T and %T", lv, rv)
	}
	switch op {
	case ">":
		return lf > rf, nil
	case "<":
		return lf < rf, nil
	case ">=":
		return lf >= rf, nil
	case "<=":
		return lf <= rf, nil
	}
	return false, nil
}

// -----------------------------------------------------------------------------
// Lexer & Parser
// -----------------------------------------------------------------------------

type tokType int

const (
	tEOF tokType = iota
	tNumber
	tString
	tIdent
	tLParen
	tRParen
	tComma
	tOp
)

type token struct {
	typ tokType
	val string
	pos int
}

type lexer struct {
	s   string
	pos int
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// lexNumber consumes a numeric run starting at start. It accepts decimal and
// hex (0x) integers, floats with an optional exponent, and underscores. The
// run is validated later in nud; invalid runs produce a parse error rather
// than being silently truncated.
func (l *lexer) lexNumber(start int) token {
	if l.s[start] == '0' && start+1 < len(l.s) && (l.s[start+1] == 'x' || l.s[start+1] == 'X') {
		l.pos = start + 2
		for l.pos < len(l.s) && (isHexDigit(l.s[l.pos]) || l.s[l.pos] == '_') {
			l.pos++
		}
		return token{tNumber, l.s[start:l.pos], start}
	}
	l.pos = start
	for l.pos < len(l.s) {
		c := l.s[l.pos]
		if (c >= '0' && c <= '9') || c == '.' || c == '_' {
			l.pos++
			continue
		}
		if c == 'e' || c == 'E' {
			l.pos++
			if l.pos < len(l.s) && (l.s[l.pos] == '+' || l.s[l.pos] == '-') {
				l.pos++
			}
			continue
		}
		break
	}
	return token{tNumber, l.s[start:l.pos], start}
}

// lexString reads a quoted string beginning just after the opening quote q.
func (l *lexer) lexString(q byte, start int) (token, error) {
	var sb strings.Builder
	for l.pos < len(l.s) {
		curr := l.s[l.pos]
		if curr == q {
			l.pos++
			return token{tString, sb.String(), start}, nil
		}
		if curr == '\\' {
			if l.pos+1 < len(l.s) && l.s[l.pos+1] == q {
				sb.WriteByte(q)
				l.pos += 2
				continue
			}
			val, _, tail, err := strconv.UnquoteChar(l.s[l.pos:], q)
			if err != nil {
				return token{}, err
			}
			consumed := len(l.s[l.pos:]) - len(tail)
			l.pos += consumed
			sb.WriteRune(val)
			continue
		}
		l.pos++
		sb.WriteByte(curr)
	}
	return token{}, errors.New("unterminated string")
}

func (l *lexer) nextToken() (token, error) {
	// Skip whitespace (rune-aware for multi-byte spaces).
	for l.pos < len(l.s) {
		r, size := utf8.DecodeRuneInString(l.s[l.pos:])
		if !unicode.IsSpace(r) {
			break
		}
		l.pos += size
	}
	if l.pos >= len(l.s) {
		return token{tEOF, "", l.pos}, nil
	}
	start := l.pos
	r, size := utf8.DecodeRuneInString(l.s[l.pos:])

	switch {
	case r >= '0' && r <= '9':
		return l.lexNumber(start), nil
	case unicode.IsLetter(r) || r == '_':
		l.pos += size
		for l.pos < len(l.s) {
			c, csize := utf8.DecodeRuneInString(l.s[l.pos:])
			if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' {
				l.pos += csize
				continue
			}
			break
		}
		return token{tIdent, l.s[start:l.pos], start}, nil
	case r == '"' || r == '\'':
		l.pos += size // consume the opening quote (ASCII)
		return l.lexString(byte(r), start)
	}

	// Punctuation and operators are all ASCII.
	l.pos += size
	switch r {
	case '[':
		return token{tOp, "[", start}, nil
	case ']':
		return token{tOp, "]", start}, nil
	case '(':
		return token{tLParen, "(", start}, nil
	case ')':
		return token{tRParen, ")", start}, nil
	case ',':
		return token{tComma, ",", start}, nil
	case '.':
		return token{tOp, ".", start}, nil
	}
	ops := []string{"==", "!=", "<=", ">=", "&&", "||", "<<", ">>"}
	for _, op := range ops {
		if strings.HasPrefix(l.s[start:], op) {
			l.pos = start + len(op)
			return token{tOp, op, start}, nil
		}
	}
	return token{tOp, string(r), start}, nil
}

type parser struct {
	lex      *lexer
	curr     token
	next     token
	lexErr   error
	maxDepth int
}

// newParser builds a parser over s with the given nesting limit. A non-positive
// limit falls back to MaxStackDepth.
func newParser(s string, maxDepth int) *parser {
	if maxDepth <= 0 {
		maxDepth = MaxStackDepth
	}
	p := &parser{lex: &lexer{s: s}, maxDepth: maxDepth}
	p.advance()
	p.advance()
	return p
}

func (p *parser) advance() {
	p.curr = p.next
	if p.lexErr != nil {
		p.next = token{tEOF, "", p.lex.pos}
		return
	}
	n, err := p.lex.nextToken()
	if err != nil {
		p.lexErr = err
		p.next = token{tEOF, "", p.lex.pos}
		return
	}
	p.next = n
}

func (p *parser) parse(rbp int, depth int) (Expr, error) {
	if depth > p.maxDepth {
		return nil, fmt.Errorf("expression nesting too deep (max %d)", p.maxDepth)
	}
	if p.lexErr != nil {
		return nil, p.lexErr
	}
	t := p.curr
	p.advance()
	if p.lexErr != nil {
		return nil, p.lexErr
	}
	left, err := p.nud(t, depth)
	if err != nil {
		return nil, err
	}
	for rbp < p.curLbp() {
		t = p.curr
		p.advance()
		if p.lexErr != nil {
			return nil, p.lexErr
		}
		left, err = p.led(t, left, depth)
		if err != nil {
			return nil, err
		}
	}
	return left, nil
}

// curLbp is the binding power of the current token, treating the two-word
// `not in` operator (curr == "not", next == "in") as a single infix operator.
func (p *parser) curLbp() int {
	if p.curr.typ == tIdent && p.curr.val == "not" && p.next.typ == tIdent && p.next.val == "in" {
		return lbpIn
	}
	return lbp(p.curr)
}

// parseNumber turns a numeric token into an int64 or float64 literal,
// returning an error for malformed numbers instead of silently yielding 0.
func parseNumber(raw string) (Expr, error) {
	s := strings.ReplaceAll(raw, "_", "")
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		i, err := strconv.ParseInt(s, 0, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q: %w", raw, err)
		}
		return &LiteralExpr{i}, nil
	}
	if strings.ContainsAny(s, ".eE") {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q: %w", raw, err)
		}
		return &LiteralExpr{f}, nil
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid number %q: %w", raw, err)
	}
	return &LiteralExpr{i}, nil
}

func (p *parser) nud(t token, depth int) (Expr, error) {
	switch t.typ {
	case tNumber:
		return parseNumber(t.val)
	case tString:
		return &LiteralExpr{t.val}, nil
	case tIdent:
		if t.val == "true" {
			return &LiteralExpr{true}, nil
		}
		if t.val == "false" {
			return &LiteralExpr{false}, nil
		}
		if p.curr.typ == tLParen {
			p.advance()
			args, err := p.parseArgs(depth)
			if err != nil {
				return nil, err
			}
			return &CallExpr{t.val, args}, nil
		}
		return &VariableExpr{t.val}, nil
	case tLParen:
		e, err := p.parse(0, depth+1)
		if err != nil {
			return nil, err
		}
		if p.curr.typ != tRParen {
			return nil, fmt.Errorf("missing ) at position %d", p.curr.pos)
		}
		p.advance()
		return e, nil
	case tOp:
		switch t.val {
		case "!", "-", "~":
			right, err := p.parse(60, depth+1)
			if err != nil {
				return nil, err
			}
			return &UnaryExpr{Op: t.val, Right: right}, nil
		case "[":
			// List literal: [a, b, c] or []
			var elems []Expr
			for p.curr.typ != tOp || p.curr.val != "]" {
				if p.curr.typ == tEOF {
					return nil, errors.New("missing ] in list literal")
				}
				el, err := p.parse(0, depth+1)
				if err != nil {
					return nil, err
				}
				elems = append(elems, el)
				if p.curr.typ == tComma {
					p.advance()
				}
			}
			p.advance() // consume ]
			return &ListExpr{Elems: elems}, nil
		default:
			return nil, fmt.Errorf("unexpected token %s", t.val)
		}
	default:
		return nil, fmt.Errorf("unexpected token %s", t.val)
	}
}

func (p *parser) led(t token, left Expr, depth int) (Expr, error) {
	// Two-word `not in` operator: t is "not" and curr is "in".
	if t.typ == tIdent && t.val == "not" {
		if p.curr.typ != tIdent || p.curr.val != "in" {
			return nil, fmt.Errorf("expected 'in' after 'not' at position %d", p.curr.pos)
		}
		p.advance() // consume "in"
		right, err := p.parse(lbpIn, depth+1)
		if err != nil {
			return nil, err
		}
		return &InfixExpr{Left: left, Op: "not in", Right: right}, nil
	}
	if t.val == "?" {
		thenExpr, err := p.parse(0, depth+1)
		if err != nil {
			return nil, err
		}
		if p.curr.typ != tOp || p.curr.val != ":" {
			return nil, fmt.Errorf("missing : in ternary expression at position %d", p.curr.pos)
		}
		p.advance()
		elseExpr, err := p.parse(lbp(t)-1, depth+1)
		if err != nil {
			return nil, err
		}
		return &TernaryExpr{Cond: left, Then: thenExpr, Else: elseExpr}, nil
	}
	if t.val == "[" {
		idxExpr, err := p.parse(0, depth+1)
		if err != nil {
			return nil, err
		}
		if p.curr.typ != tOp || p.curr.val != "]" {
			return nil, fmt.Errorf("missing ] in index expression at position %d", p.curr.pos)
		}
		p.advance()
		return &IndexExpr{Left: left, Index: idxExpr}, nil
	}
	if t.val == "." {
		if p.curr.typ == tOp && p.curr.val == "[" {
			p.advance()
			idxExpr, err := p.parse(0, depth+1)
			if err != nil {
				return nil, err
			}
			if p.curr.typ != tOp || p.curr.val != "]" {
				return nil, fmt.Errorf("missing ] in index expression at position %d", p.curr.pos)
			}
			p.advance()
			return &IndexExpr{Left: left, Index: idxExpr}, nil
		}
		member := p.curr.val
		p.advance()
		if p.curr.typ == tLParen {
			p.advance()
			args, err := p.parseArgs(depth)
			if err != nil {
				return nil, err
			}
			return &MethodCallExpr{Left: left, Method: member, Args: args}, nil
		}
		return &MemberAccessExpr{Left: left, Key: member}, nil
	}
	right, err := p.parse(lbp(t), depth+1)
	return &InfixExpr{Left: left, Op: t.val, Right: right}, err
}

func (p *parser) parseArgs(depth int) ([]Expr, error) {
	var args []Expr
	for p.curr.typ != tRParen && p.curr.typ != tEOF {
		a, err := p.parse(0, depth+1)
		if err != nil {
			return nil, err
		}
		args = append(args, a)
		if p.curr.typ == tComma {
			p.advance()
		}
	}
	if p.curr.typ != tRParen {
		return nil, errors.New("missing ) in args")
	}
	p.advance()
	return args, nil
}

// lbpIn is the binding power of `in` / `not in`; it sits on the comparison
// tier, matching how most languages treat membership.
const lbpIn = 35

func lbp(t token) int {
	switch t.typ {
	case tOp:
		switch t.val {
		case ".":
			return 100
		case "[":
			return 100
		case "*", "/", "%", "<<", ">>", "&":
			return 50
		case "+", "-", "|", "^":
			return 40
		case "<", ">", "<=", ">=":
			return lbpIn
		case "==", "!=":
			return 30
		case "&&":
			return 20
		case "||":
			return 10
		case "?":
			return 5
		}
	case tIdent:
		if t.val == "in" {
			return lbpIn
		}
		return 0
	default:
		return 0
	}
	return 0
}

// -----------------------------------------------------------------------------
// Engine & Utils
// -----------------------------------------------------------------------------

type Engine struct {
	funcs        atomic.Value
	maxDepth     atomic.Int64
	strict       atomic.Bool
	methodFilter atomic.Value // holds methodPolicy
}

// methodPolicy wraps the optional method filter so it can live in an
// atomic.Value (which needs a consistent concrete type and rejects nil).
type methodPolicy struct{ fn func(name string) bool }

func defaultFuncs() map[string]CustomFunc {
	return map[string]CustomFunc{
		"len": func(args []any) (any, error) {
			if len(args) == 0 {
				return int64(0), nil
			}
			rv := reflect.ValueOf(args[0])
			for rv.Kind() == reflect.Pointer {
				if rv.IsNil() {
					return int64(0), nil
				}
				rv = rv.Elem()
			}
			switch rv.Kind() {
			case reflect.Slice, reflect.Array, reflect.Map, reflect.String:
				return int64(rv.Len()), nil
			}
			return int64(0), nil
		},
		"now":        func(args []any) (any, error) { return time.Now().Unix(), nil },
		"contains":   strBinFunc("contains", strings.Contains),
		"startswith": strBinFunc("startsWith", strings.HasPrefix),
		"endswith":   strBinFunc("endsWith", strings.HasSuffix),
		"lower":      strUnaryFunc("lower", strings.ToLower),
		"upper":      strUnaryFunc("upper", strings.ToUpper),
		"trim":       strUnaryFunc("trim", strings.TrimSpace),
	}
}

// asString coerces an argument to a string, accepting Go strings directly and
// otherwise falling back to fmt.Sprint so numbers/bools are usable too.
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func strBinFunc(name string, fn func(string, string) bool) CustomFunc {
	return func(args []any) (any, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s: expected 2 args, got %d", name, len(args))
		}
		return fn(asString(args[0]), asString(args[1])), nil
	}
}

func strUnaryFunc(name string, fn func(string) string) CustomFunc {
	return func(args []any) (any, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s: expected 1 arg, got %d", name, len(args))
		}
		return fn(asString(args[0])), nil
	}
}

func (e *Engine) loadFuncs() (m map[string]CustomFunc) {
	defer func() {
		if recover() != nil {
			m = defaultFuncs()
			e.funcs.Store(m)
		}
	}()
	return e.funcs.Load().(map[string]CustomFunc)
}

func NewEngine() *Engine {
	e := &Engine{}
	e.funcs.Store(defaultFuncs())
	e.maxDepth.Store(MaxStackDepth)
	e.methodFilter.Store(methodPolicy{nil})
	return e
}

// SetMaxNestingDepth overrides the parser nesting limit for expressions
// compiled by this Engine. A non-positive value restores the default
// (MaxStackDepth). Safe to call concurrently.
func (e *Engine) SetMaxNestingDepth(n int) {
	if n <= 0 {
		n = MaxStackDepth
	}
	e.maxDepth.Store(int64(n))
}

func (e *Engine) depthLimit() int {
	if n := int(e.maxDepth.Load()); n > 0 {
		return n
	}
	return MaxStackDepth
}

// SetStrict controls strict lookups: when true, accessing a missing struct
// field, map key, out-of-range index, or a member of nil returns an error
// (wrapping ErrUnknownField) instead of nil. Off by default. Safe to call
// concurrently.
func (e *Engine) SetStrict(strict bool) { e.strict.Store(strict) }

// SetMethodFilter installs a predicate consulted before every reflected method
// or getter invocation; names for which it returns false are denied with
// ErrMethodDenied. Pass nil to allow all (the default). Safe to call
// concurrently.
func (e *Engine) SetMethodFilter(filter func(name string) bool) {
	e.methodFilter.Store(methodPolicy{filter})
}

func (e *Engine) methodFilterFn() func(string) bool {
	v := e.methodFilter.Load()
	if v == nil {
		return nil
	}
	return v.(methodPolicy).fn
}

func (e *Engine) RegisterFunc(name string, fn CustomFunc) error {
	if name == "" {
		return errors.New("func name cannot be empty")
	}
	if fn == nil {
		return errors.New("func cannot be nil")
	}
	curr := e.loadFuncs()
	next := make(map[string]CustomFunc, len(curr)+1)
	maps.Copy(next, curr)
	// Lookup in CallExpr.Eval normalizes names to lower case, so store the
	// key the same way to keep registration case-insensitive.
	next[strings.ToLower(name)] = fn
	e.funcs.Store(next)
	return nil
}

// Program is a parsed expression bound to the Engine it was compiled on.
// Parsing is done once; Eval can then be called repeatedly against different
// data without re-lexing/re-parsing, which is the common rules-engine pattern
// of evaluating one rule many times. It always reads the latest registered
// funcs from its Engine, so RegisterFunc still takes effect after Compile.
type Program struct {
	ast    Expr
	engine *Engine
}

// Compile parses exprStr once and returns a reusable Program, honoring the
// Engine's nesting limit. Parse-time panics are recovered and returned as
// errors, mirroring Eval.
func (e *Engine) Compile(exprStr string) (prog *Program, err error) {
	defer func() {
		if r := recover(); r != nil {
			prog = nil
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	ast, err := parseWithDepth(exprStr, e.depthLimit())
	if err != nil {
		return nil, err
	}
	return &Program{ast: foldConstants(ast), engine: e}, nil
}

// Eval evaluates a compiled Program against data. Evaluation panics are
// recovered and returned as errors.
func (p *Program) Eval(data any) (res any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
			res = nil
		}
	}()
	return p.ast.Eval(Context{
		Data:         data,
		Fns:          p.engine.loadFuncs(),
		Strict:       p.engine.strict.Load(),
		MethodFilter: p.engine.methodFilterFn(),
	})
}

// Vars returns the distinct root variable/field identifiers the program reads
// from the data object, sorted. Useful for validating a rule against a schema
// or building dependency indexes before running it.
func (p *Program) Vars() []string {
	set := map[string]struct{}{}
	walk(p.ast, func(e Expr) {
		if v, ok := e.(*VariableExpr); ok {
			set[v.Name] = struct{}{}
		}
	})
	return sortedKeys(set)
}

// Funcs returns the distinct function names the program calls, sorted.
func (p *Program) Funcs() []string {
	set := map[string]struct{}{}
	walk(p.ast, func(e Expr) {
		if c, ok := e.(*CallExpr); ok {
			set[c.Name] = struct{}{}
		}
	})
	return sortedKeys(set)
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// walk visits e and all of its sub-expressions, calling fn on each.
func walk(e Expr, fn func(Expr)) {
	fn(e)
	switch n := e.(type) {
	case *UnaryExpr:
		walk(n.Right, fn)
	case *InfixExpr:
		walk(n.Left, fn)
		walk(n.Right, fn)
	case *TernaryExpr:
		walk(n.Cond, fn)
		walk(n.Then, fn)
		walk(n.Else, fn)
	case *MemberAccessExpr:
		walk(n.Left, fn)
	case *IndexExpr:
		walk(n.Left, fn)
		walk(n.Index, fn)
	case *MethodCallExpr:
		walk(n.Left, fn)
		for _, a := range n.Args {
			walk(a, fn)
		}
	case *CallExpr:
		for _, a := range n.Args {
			walk(a, fn)
		}
	case *ListExpr:
		for _, el := range n.Elems {
			walk(el, fn)
		}
	}
}

// foldConstants replaces sub-expressions built entirely from literals with the
// literal they evaluate to, so a compiled Program does not recompute constant
// arithmetic on every Eval. Folding is skipped for any subtree whose evaluation
// errors (e.g. `1/0`), preserving the original error-at-eval semantics.
func foldConstants(e Expr) Expr {
	switch n := e.(type) {
	case *UnaryExpr:
		n.Right = foldConstants(n.Right)
		if isLiteral(n.Right) {
			return tryFold(n)
		}
	case *InfixExpr:
		n.Left = foldConstants(n.Left)
		n.Right = foldConstants(n.Right)
		if isLiteral(n.Left) && isLiteral(n.Right) {
			return tryFold(n)
		}
	case *TernaryExpr:
		n.Cond = foldConstants(n.Cond)
		n.Then = foldConstants(n.Then)
		n.Else = foldConstants(n.Else)
		if lit, ok := n.Cond.(*LiteralExpr); ok {
			if toBool(lit.Value) {
				return n.Then
			}
			return n.Else
		}
	case *ListExpr:
		allLit := true
		for i := range n.Elems {
			n.Elems[i] = foldConstants(n.Elems[i])
			if !isLiteral(n.Elems[i]) {
				allLit = false
			}
		}
		if allLit {
			return tryFold(n)
		}
	}
	return e
}

// tryFold evaluates a fully-constant node with an empty context; on any error
// (or panic) it returns the node unchanged so the error surfaces at Eval time.
func tryFold(e Expr) (out Expr) {
	defer func() {
		if recover() != nil {
			out = e
		}
	}()
	v, err := e.Eval(Context{})
	if err != nil {
		return e
	}
	return &LiteralExpr{v}
}

func isLiteral(e Expr) bool {
	_, ok := e.(*LiteralExpr)
	return ok
}

func (e *Engine) Eval(exprStr string, data any) (res any, err error) {
	prog, err := e.Compile(exprStr)
	if err != nil {
		return nil, err
	}
	return prog.Eval(data)
}

func toInt64(v any) (int64, bool) {
	if v == nil {
		return 0, false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(rv.Uint()), true
	default:
		return 0, false
	}
}

func toFloat(v any) (float64, bool) {
	if i, ok := toInt64(v); ok {
		return float64(i), true
	}
	rv := reflect.ValueOf(v)
	if k := rv.Kind(); k == reflect.Float32 || k == reflect.Float64 {
		return rv.Float(), true
	}
	if s, ok := v.(string); ok {
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	}
	return 0, false
}

func toBool(v any) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	if s, ok := v.(string); ok {
		return s != "" && !strings.EqualFold(s, "false")
	}
	f, _ := toFloat(v)
	return f != 0
}

// EvalTo evaluates the expression and attempts to cast/convert the result to
// type T. Note: converting a float result to an integer T truncates toward
// zero (e.g. EvalTo[int] of 1.9 yields 1). Any panic from the conversion is
// recovered and returned as an error.
func EvalTo[T any](e *Engine, exprStr string, data any) (out T, retErr error) {
	var zero T
	defer func() {
		if r := recover(); r != nil {
			out = zero
			retErr = fmt.Errorf("panic: %v", r)
		}
	}()
	raw, err := e.Eval(exprStr, data)
	if err != nil {
		return zero, err
	}

	// 1. Try direct type assertion
	if val, ok := raw.(T); ok {
		return val, nil
	}

	// 2. Handle numeric conversions (e.g., int64 from engine to int in T)
	rv := reflect.ValueOf(raw)
	targetType := reflect.TypeOf(zero)
	if targetType == nil {
		return zero, fmt.Errorf("target type cannot be nil")
	}

	// For non-numeric types, use reflect conversion if possible.
	// Numeric conversions are handled below to keep behavior flexible (e.g., string->float via toFloat).
	if rv.IsValid() && rv.Type().ConvertibleTo(targetType) {
		switch targetType.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Float32, reflect.Float64:
			// handled below
		default:
			return rv.Convert(targetType).Interface().(T), nil
		}
	}

	// 3. Fallback for numeric conversions.
	switch targetType.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if i, ok := toInt64(raw); ok {
			return reflect.ValueOf(i).Convert(targetType).Interface().(T), nil
		}
		if f, ok := toFloat(raw); ok {
			return reflect.ValueOf(int64(f)).Convert(targetType).Interface().(T), nil
		}
	case reflect.Float32, reflect.Float64:
		if f, ok := toFloat(raw); ok {
			return reflect.ValueOf(f).Convert(targetType).Interface().(T), nil
		}
	}

	return zero, fmt.Errorf("result type %T is not compatible with target type %T", raw, zero)
}

// parseWithDepth parses s with the given nesting limit. Any panic is recovered
// and returned as an error so parsing can never crash the caller.
func parseWithDepth(s string, maxDepth int) (ast Expr, err error) {
	defer func() {
		if r := recover(); r != nil {
			ast = nil
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	p := newParser(s, maxDepth)
	ast, err = p.parse(0, 0)
	if err != nil {
		return nil, err
	}
	if p.curr.typ != tEOF {
		return nil, fmt.Errorf("extra token %s at position %d", p.curr.val, p.curr.pos)
	}
	return ast, nil
}

// ParseExpr parses s into an AST using the default nesting limit. It never
// panics; malformed input is returned as an error.
func ParseExpr(s string) (Expr, error) {
	return parseWithDepth(s, MaxStackDepth)
}
