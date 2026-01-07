package core

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const MaxStackDepth = 256

// -----------------------------------------------------------------------------
// Core Types & Context
// -----------------------------------------------------------------------------

type CustomFunc func(args []any) (any, error)

type Context struct {
	Data any
	Fns  map[string]CustomFunc
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
			return nil, errors.New("negative shift count")
		}
		return li << uint64(ri), nil
	case ">>":
		if ri < 0 {
			return nil, errors.New("negative shift count")
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
	pt := reflect.PtrTo(t)
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
		return strconv.Quote(s)
	}
	return fmt.Sprint(e.Value)
}

type VariableExpr struct{ Name string }

func (e *VariableExpr) Eval(ctx Context) (any, error) {
	return getMember(ctx.Data, e.Name)
}
func (e *VariableExpr) String() string { return e.Name }

type MemberAccessExpr struct {
	Left    Expr
	Key     string
	IsIndex bool
}

func (e *MemberAccessExpr) Eval(ctx Context) (any, error) {
	val, err := e.Left.Eval(ctx)
	if err != nil || val == nil {
		return nil, err
	}
	return getMember(val, e.Key)
}
func (e *MemberAccessExpr) String() string {
	if e.IsIndex {
		return fmt.Sprintf("%s[%s]", e.Left.String(), e.Key)
	}
	return fmt.Sprintf("%s.%s", e.Left.String(), e.Key)
}

type IndexExpr struct {
	Left  Expr
	Index Expr
}

func (e *IndexExpr) Eval(ctx Context) (any, error) {
	obj, err := e.Left.Eval(ctx)
	if err != nil || obj == nil {
		return nil, err
	}
	idx, err := e.Index.Eval(ctx)
	if err != nil {
		return nil, err
	}

	rv := reflect.ValueOf(obj)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil, nil
		}
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		i, ok := toInt64(idx)
		if !ok {
			return nil, nil
		}
		if i < 0 || i >= int64(rv.Len()) {
			return nil, nil
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
						return nil, nil
					}
				case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
					if u, err := strconv.ParseUint(s, 10, 64); err == nil {
						kv = reflect.ValueOf(u).Convert(kt)
					} else {
						return nil, nil
					}
				default:
					return nil, nil
				}
			} else {
				return nil, nil
			}
		}
		res := rv.MapIndex(kv)
		if !res.IsValid() {
			return nil, nil
		}
		return res.Interface(), nil
	}
	return nil, nil
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
		for rv.Kind() == reflect.Ptr {
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

	// 2. FALLBACK: Try to find the method on the root Data object
	if ctx.Data != nil {
		args := make([]any, len(e.Args))
		for i, argExpr := range e.Args {
			v, err := argExpr.Eval(ctx)
			if err != nil {
				return nil, err
			}
			args[i] = v
		}
		// Attempt to call it as a method on the root object
		res, err := callReflectMethod(ctx.Data, e.Name, args)
		if err == nil {
			return res, nil
		}
	}

	return nil, fmt.Errorf("function or method %s not found", e.Name)
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
		if reflect.DeepEqual(lv, rv) {
			return true, nil
		}
		lf, okL := toFloat(lv)
		rf, okR := toFloat(rv)
		if okL && okR {
			return lf == rf, nil
		}
		return false, nil
	case "!=":
		if reflect.DeepEqual(lv, rv) {
			return false, nil
		}
		lf, okL := toFloat(lv)
		rf, okR := toFloat(rv)
		if okL && okR {
			return lf != rf, nil
		}
		return true, nil
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
	}
	return nil, nil
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

func getMember(obj any, key string) (any, error) {
	if obj == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(obj)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil, nil
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
				return nil, nil
			}
		}
		res := rv.MapIndex(kv)
		if !res.IsValid() {
			return nil, nil
		}
		return res.Interface(), nil
	case reflect.Slice, reflect.Array:
		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx >= rv.Len() {
			return nil, nil
		}
		return rv.Index(idx).Interface(), nil
	case reflect.Struct:
		meta := getStructMeta(rv.Type())
		if idx, ok := meta.fields[key]; ok {
			return rv.Field(idx).Interface(), nil
		}
		if _, ok := meta.methods[key]; ok {
			m := rv.MethodByName(key)
			if !m.IsValid() && rv.CanAddr() {
				m = rv.Addr().MethodByName(key)
			}
			if m.IsValid() && m.Type().NumIn() == 0 && m.Type().NumOut() > 0 {
				return m.Call(nil)[0].Interface(), nil
			}
		}
	}
	return nil, nil
}

func callReflectMethod(obj any, name string, args []any) (any, error) {
	rv := reflect.ValueOf(obj)
	mv := rv.MethodByName(name)
	if !mv.IsValid() {
		if rv.Kind() != reflect.Ptr {
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
	if len(out) > 1 && out[len(out)-1].Type().Implements(reflect.TypeOf((*error)(nil)).Elem()) {
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
				return nil, errors.New("div by zero")
			}
			return li / ri, nil
		case '%':
			if ri == 0 {
				return nil, errors.New("div by zero")
			}
			return li % ri, nil
		}
	}
	lf, _ := toFloat(lv)
	rf, _ := toFloat(rv)
	switch op {
	case '+':
		return lf + rf, nil
	case '-':
		return lf - rf, nil
	case '*':
		return lf * rf, nil
	case '/':
		if rf == 0 {
			return nil, errors.New("div by zero")
		}
		return lf / rf, nil
	}
	return nil, nil
}

func compare(lv, rv any, op string) (bool, error) {
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
	tPathKey
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

func (l *lexer) nextToken() (token, error) {
	for l.pos < len(l.s) && unicode.IsSpace(rune(l.s[l.pos])) {
		l.pos++
	}
	if l.pos >= len(l.s) {
		return token{tEOF, "", l.pos}, nil
	}
	start := l.pos
	r := l.s[l.pos]
	l.pos++
	switch {
	case unicode.IsDigit(rune(r)):
		for l.pos < len(l.s) && (unicode.IsDigit(rune(l.s[l.pos])) || l.s[l.pos] == '.') {
			l.pos++
		}
		return token{tNumber, l.s[start:l.pos], start}, nil
	case unicode.IsLetter(rune(r)) || r == '_':
		for l.pos < len(l.s) && (unicode.IsLetter(rune(l.s[l.pos])) || unicode.IsDigit(rune(l.s[l.pos])) || l.s[l.pos] == '_') {
			l.pos++
		}
		return token{tIdent, l.s[start:l.pos], start}, nil
	case r == '"' || r == '\'':
		q := r
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
				val, _, tail, err := strconv.UnquoteChar(l.s[l.pos:], byte(q))
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
	case r == '[':
		return token{tOp, "[", start}, nil
	case r == ']':
		return token{tOp, "]", start}, nil
	case r == '(':
		return token{tLParen, "(", start}, nil
	case r == ')':
		return token{tRParen, ")", start}, nil
	case r == ',':
		return token{tComma, ",", start}, nil
	case r == '.':
		return token{tOp, ".", start}, nil
	default:
		ops := []string{"==", "!=", "<=", ">=", "&&", "||", "<<", ">>"}
		for _, op := range ops {
			if strings.HasPrefix(l.s[start:], op) {
				l.pos = start + len(op)
				return token{tOp, op, start}, nil
			}
		}
		return token{tOp, string(r), start}, nil
	}
}

type parser struct {
	lex    *lexer
	curr   token
	next   token
	lexErr error
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
	if depth > MaxStackDepth {
		return nil, errors.New("stack overflow")
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
	for rbp < lbp(p.curr) {
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

func (p *parser) nud(t token, depth int) (Expr, error) {
	switch t.typ {
	case tNumber:
		if strings.Contains(t.val, ".") {
			f, _ := strconv.ParseFloat(t.val, 64)
			return &LiteralExpr{f}, nil
		}
		i, _ := strconv.ParseInt(t.val, 10, 64)
		return &LiteralExpr{i}, nil
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
		default:
			return nil, fmt.Errorf("unexpected token %s", t.val)
		}
	default:
		return nil, fmt.Errorf("unexpected token %s", t.val)
	}
}

func (p *parser) led(t token, left Expr, depth int) (Expr, error) {
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
			return 35
		case "==", "!=":
			return 30
		case "&&":
			return 20
		case "||":
			return 10
		case "?":
			return 5
		}
	default:
		return 0
	}
	return 0
}

// -----------------------------------------------------------------------------
// Engine & Utils
// -----------------------------------------------------------------------------

type Engine struct{ funcs atomic.Value }

func defaultFuncs() map[string]CustomFunc {
	return map[string]CustomFunc{
		"len": func(args []any) (any, error) {
			if len(args) == 0 {
				return 0, nil
			}
			rv := reflect.ValueOf(args[0])
			if rv.Kind() == reflect.Slice || rv.Kind() == reflect.String || rv.Kind() == reflect.Map {
				return int64(rv.Len()), nil
			}
			return int64(0), nil
		},
		"now": func(args []any) (any, error) { return time.Now().Unix(), nil },
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
	return e
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
	for k, v := range curr {
		next[k] = v
	}
	next[name] = fn
	e.funcs.Store(next)
	return nil
}

func (e *Engine) Eval(exprStr string, data any) (res any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
			res = nil
		}
	}()

	l := &lexer{s: exprStr}
	p := &parser{lex: l}
	p.advance()
	p.advance()
	ast, err := p.parse(0, 0)
	if err != nil {
		return nil, err
	}
	if p.curr.typ != tEOF {
		return nil, fmt.Errorf("unexpected token %q at %d", p.curr.val, p.curr.pos)
	}
	return ast.Eval(Context{Data: data, Fns: e.loadFuncs()})
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
		return s != "" && s != "false"
	}
	f, _ := toFloat(v)
	return f != 0
}

// EvalTo evaluates the expression and attempts to cast/convert the result to type T.
func EvalTo[T any](e *Engine, exprStr string, data any) (T, error) {
	var zero T
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

func ParseExpr(s string) (Expr, error) {
	l := &lexer{s: s}
	p := &parser{lex: l}
	p.advance() // Initialize curr
	p.advance() // Initialize next

	ast, err := p.parse(0, 0)
	if err != nil {
		return nil, err
	}
	if p.curr.typ != tEOF {
		return nil, fmt.Errorf("extra token %s at position %d", p.curr.val, p.curr.pos)
	}
	return ast, nil
}
