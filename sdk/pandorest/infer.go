package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tdewolff/parse/v2/js"
)

// kind is what sort of value the scan found an expression to be.
type kind uint8

const (
	unknownKind  kind = iota // nothing the scan could tell
	nullKind                 // null or undefined
	stringKind               // text
	numberKind               // a number that may have a fraction
	integerKind              // a whole number
	booleanKind              // true or false
	dateKind                 // a Date, written into JSON as text
	arrayKind                // a list
	objectKind               // a record of named fields
	classKind                // a class itself, for its static methods
	instanceKind             // an instance of a class
	funcKind                 // a function, to be called
	requestKind              // the request a handler is given
	responseKind             // the response a handler answers through
	mixedKind                // a value that is one of several kinds that do not merge
)

// jsType is what the scan knows of a value.
type jsType struct {
	kind     kind
	nullable bool      // it may also be null
	elem     *jsType   // a list's elements
	props    []prop    // a record's fields, in the order written
	open     bool      // a record with fields the scan could not see, from a spread it could not follow
	name     string    // the schema a record is written as, such as LibraryItemExpanded
	class    *jsClass  // a class, or the class of an instance
	fn       *jsFunc   // a function
	resp     *answer   // the answer a response writes into
	status   int       // a response's status, once res.status(n) has set one
	of       []*jsType // a mixed value's kinds
	req      *reqInfo  // the request a handler is given
	ctorArgs []*jsType // what an instance was made with, for the fields its constructor sets from them
	literals []string  // the strings a value is seen to take, such as a mediaType's 'book' and 'podcast'
	written  bool      // a whole number written out in the source, such as the 0 of total = 0, which says nothing of what else the value takes
}

type prop struct {
	name string
	typ  *jsType
}

// jsFunc is a function or method to call, with what it closes over and what this is inside it.
type jsFunc struct {
	params  js.Params
	body    []js.IStmt
	scope   *scope
	this    *jsType
	name    string // for the cycle guard and the report: Class.method or file:function
	builtin func(e *evaluator, this *jsType, args []*jsType) *jsType
}

var (
	unknown = &jsType{kind: unknownKind}
	null    = &jsType{kind: nullKind, nullable: true}
	str     = &jsType{kind: stringKind}
	num     = &jsType{kind: numberKind}
	integer = &jsType{kind: integerKind}
	// a whole number written out, which another value of the same name may not be
	writtenInt = &jsType{kind: integerKind, written: true}
	boolean    = &jsType{kind: booleanKind}
	date       = &jsType{kind: dateKind}
)

func listOf(t *jsType) *jsType { return &jsType{kind: arrayKind, elem: t} }

func instanceOf(c *jsClass) *jsType { return &jsType{kind: instanceKind, class: c} }

// field is a record's field by name, or nil.
func (t *jsType) field(name string) *jsType {
	for _, p := range t.props {
		if p.name == name {
			return p.typ
		}
	}

	return nil
}

// withField is the record with one more field, or the field's type merged with what it had.
func (t *jsType) withField(name string, ft *jsType) *jsType {
	out := *t
	out.props = slices.Clone(t.props)
	for i, p := range out.props {
		if p.name == name {
			out.props[i].typ = merge(p.typ, ft)
			return &out
		}
	}
	out.props = append(out.props, prop{name, ft})

	return &out
}

// orNull is the type that may also be null.
func (t *jsType) orNull() *jsType {
	if t.nullable {
		return t
	}

	out := *t
	out.nullable = true

	return &out
}

// notNull is the type without its null, as after x || y has ruled it out.
func (t *jsType) notNull() *jsType {
	if t.kind == nullKind {
		return unknown
	}
	if !t.nullable {
		return t
	}

	out := *t
	out.nullable = false

	return &out
}

// widen is a value merged with one the scan could not tell: a whole number written out, as the 0 of x || 0, then says only that it is a
// number, since what it stands in for may have a fraction, and text written out, as the 'book' of x || 'book', may be any text.
func widen(t *jsType) *jsType {
	switch {
	case t.kind == stringKind && t.literals != nil:
		out := *t
		out.literals = nil
		return &out
	case t.kind != integerKind || !t.written:
		return t
	case t.nullable:
		return num.orNull()
	}

	return num
}

// merge is what a value is when it may be either a or b.
func merge(a, b *jsType) *jsType {
	switch {
	case a == nil || a == b:
		return b
	case b == nil:
		return a
	case a.kind == unknownKind:
		return widen(b)
	case b.kind == unknownKind:
		return widen(a)
	case a.kind == nullKind:
		return b.orNull()
	case b.kind == nullKind:
		return a.orNull()
	}

	nullable := a.nullable || b.nullable
	out := mergeKinds(a, b)
	if nullable {
		out = out.orNull()
	}

	return out
}

func mergeKinds(a, b *jsType) *jsType {
	switch {
	case a.kind == b.kind:
		switch a.kind {
		case arrayKind:
			return listOf(merge(a.elem, b.elem))

		case objectKind:
			out := &jsType{kind: objectKind, open: a.open || b.open}
			if a.name == b.name {
				out.name = a.name
			}
			for _, p := range a.props {
				out = out.withField(p.name, p.typ)
			}
			for _, p := range b.props {
				out = out.withField(p.name, p.typ)
			}
			return out

		case instanceKind, classKind:
			if a.class == b.class {
				return a
			}
			return &jsType{kind: mixedKind, of: []*jsType{a, b}}

		case mixedKind:
			out := &jsType{kind: mixedKind, of: slices.Clone(a.of)}
			for _, m := range b.of {
				out.of = addKind(out.of, m)
			}
			return out

		case stringKind:
			out := *a
			out.literals = mergeLiterals(a.literals, b.literals)
			return &out

		case integerKind:
			// a whole number the code works out is one whatever was written beside it
			if a.written && !b.written {
				return b
			}
			return a

		default:
			return a
		}

	case a.kind == integerKind && b.kind == numberKind, a.kind == numberKind && b.kind == integerKind:
		return num
	}

	// kinds that do not merge stay side by side
	out := &jsType{kind: mixedKind}
	for _, t := range []*jsType{a, b} {
		if t.kind == mixedKind {
			for _, m := range t.of {
				out.of = addKind(out.of, m)
			}
			continue
		}
		out.of = addKind(out.of, t)
	}

	return out
}

func addKind(list []*jsType, t *jsType) []*jsType {
	for i, m := range list {
		if m.kind == t.kind && (m.kind != instanceKind && m.kind != classKind || m.class == t.class) {
			list[i] = mergeKinds(m, t)
			return list
		}
	}

	return append(list, t)
}

func mergeLiterals(a, b []string) []string {
	if a == nil || b == nil {
		return nil // one side is any text, so the whole is
	}

	out := slices.Clone(a)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}

	return out
}

// scope is the names visible in a function as it runs: its own, then those of the code around it, then the file's.
type scope struct {
	parent *scope
	file   *jsFile
	vars   map[string]*binding
	this   *jsType
	sets   map[string][]fieldSet // name.field = value, written in this function
}

// binding is one name: its value, worked out the first time it is asked for from every value given it.
type binding struct {
	inits  []func() *jsType
	typ    *jsType
	active bool
	done   bool
}

// fieldSet is one name.field = value or name.field += value, or with no field one name.push(value).
type fieldSet struct {
	field  string
	value  js.IExpr
	spread bool
	op     js.TokenType  // for name.field += value, the operator; 0 for =
	in     func() *scope // for one written in a callback, the callback's scope; nil for the function's own
}

// scope is where the value is worked out, or nil when the callback it is written in has a name of its own that hides the one set.
func (fs fieldSet) scope(name string, home *scope) *scope {
	if fs.in == nil {
		return home
	}

	in := fs.in()
	for sc := in; sc != nil && sc != home; sc = sc.parent {
		if _, ok := sc.vars[name]; ok {
			return nil
		}
	}

	return in
}

// fieldSets are what x does to a name's fields or items, and the name: name.field = value, name.field += value, name.push(value).
func fieldSets(x js.IExpr) (string, []fieldSet) {
	switch x := x.(type) {
	case *js.CallExpr:
		dot, ok := x.X.(*js.DotExpr)
		if !ok || identName(dot.Y) != "push" && identName(dot.Y) != "unshift" {
			return "", nil
		}

		v, ok := dot.X.(*js.Var)
		if !ok {
			return "", nil
		}

		sets := make([]fieldSet, 0, len(x.Args.List))
		for _, a := range x.Args.List {
			sets = append(sets, fieldSet{value: a.Value, spread: a.Rest})
		}

		return string(v.Name()), sets

	case *js.BinaryExpr:
		to, ok := x.X.(*js.DotExpr)
		if !ok || x.Op != js.EqToken && !compound(x.Op) {
			return "", nil
		}

		v, ok := to.X.(*js.Var)
		if !ok {
			return "", nil
		}

		fs := fieldSet{field: identName(to.Y), value: x.Y}
		if x.Op != js.EqToken {
			fs.op = x.Op
		}

		return string(v.Name()), []fieldSet{fs}
	}

	return "", nil
}

// outerSets records what a function written as an argument does to names outside it, such as list.forEach(s => { stats.today += s.time }), so
// they gain it as they would from a loop. Its values are worked out in its own scope, its first parameter an item of the list it is called on.
func outerSets(e *evaluator, call *js.CallExpr, arg js.IExpr, home *scope, parent func() *scope) {
	var params js.Params
	var body []js.IStmt
	switch f := arg.(type) {
	case *js.ArrowFunc:
		params, body = f.Params, f.Body.List
	case *js.FuncDecl:
		params, body = f.Params, f.Body.List
	default:
		return
	}

	var inner *scope
	in := func() *scope {
		if inner != nil {
			return inner
		}

		p := parent()
		inner = &scope{parent: p, file: p.file, vars: map[string]*binding{}, this: p.this, sets: map[string][]fieldSet{}}

		var args []*jsType
		if dot, ok := call.X.(*js.DotExpr); ok {
			if t := e.eval(dot.X, p); t.kind == arrayKind {
				args = []*jsType{t.elem, integer}
			}
		}
		e.bindParams(params, args, inner)
		declare(e, body, inner)

		return inner
	}

	walkStmts(body, func(st js.IStmt) {
		for _, x := range stmtExprs(st) {
			walkExpr(x, func(x js.IExpr) {
				if n, sets := fieldSets(x); n != "" {
					for i := range sets {
						sets[i].in = in
					}
					home.sets[n] = append(home.sets[n], sets...)
				}

				if c, ok := x.(*js.CallExpr); ok {
					for _, a := range c.Args.List {
						outerSets(e, c, a.Value, home, in)
					}
				}
			})
		}
	})
}

func (s *scope) lookup(name string) *binding {
	for sc := s; sc != nil; sc = sc.parent {
		if b, ok := sc.vars[name]; ok {
			return b
		}
	}

	return nil
}

func (s *scope) bind(name string, init func() *jsType) {
	b, ok := s.vars[name]
	if !ok {
		b = &binding{}
		s.vars[name] = b
	}
	b.inits = append(b.inits, init)
}

func (s *scope) bindType(name string, t *jsType) {
	s.vars[name] = &binding{typ: t, done: true}
}

// evaluator works out types, following calls into the functions they reach. Returns are kept by function and the kinds of its arguments,
// and a function reached again while it is being worked out is unknown there, so recursion ends.
type evaluator struct {
	prog   *program
	models *models
	cache  map[string]*jsType
	active map[string]bool
	depth  int
	extra  map[string][]prop // fields a handler adds to a named record, such as item.userMediaProgress = ...
}

const maxDepth = 200

func newEvaluator(p *program, m *models) *evaluator {
	return &evaluator{prog: p, models: m, cache: map[string]*jsType{}, active: map[string]bool{}, extra: map[string][]prop{}}
}

// eval is the type of e, in scope s.
func (e *evaluator) eval(x js.IExpr, s *scope) *jsType {
	if x == nil || e.depth > maxDepth {
		return unknown
	}

	e.depth++
	defer func() { e.depth-- }()

	switch x := x.(type) {
	case *js.LiteralExpr:
		return literal(x, s)

	case *js.TemplateExpr:
		if x.Tag != nil {
			return unknown
		}
		for _, part := range x.List {
			e.eval(part.Expr, s)
		}
		return str

	case *js.Var:
		return e.name(string(x.Name()), s)

	case *js.GroupExpr:
		return e.eval(x.X, s)

	case *js.CommaExpr:
		var t *jsType
		for _, y := range x.List {
			t = e.eval(y, s)
		}
		return t

	case *js.UnaryExpr:
		return e.unary(x, s)

	case *js.BinaryExpr:
		return e.binary(x, s)

	case *js.CondExpr:
		e.eval(x.Cond, s)
		return merge(e.eval(x.X, s), e.eval(x.Y, s))

	case *js.ArrayExpr:
		var elem *jsType
		for _, el := range x.List {
			t := e.eval(el.Value, s)
			if el.Spread {
				if t.kind == arrayKind {
					t = t.elem
				} else {
					t = unknown
				}
			}
			elem = merge(elem, t)
		}
		if elem == nil {
			elem = unknown
		}
		return listOf(elem)

	case *js.ObjectExpr:
		return e.object(x, s)

	case *js.DotExpr:
		return e.member(e.eval(x.X, s), identName(x.Y), x.Optional)

	case *js.IndexExpr:
		obj := e.eval(x.X, s)
		if k, ok := stringValue(x.Y); ok {
			return e.member(obj, k, x.Optional)
		}
		e.eval(x.Y, s)
		if obj.kind == arrayKind {
			return obj.elem
		}
		return unknown

	case *js.CallExpr:
		return e.call(x, s)

	case *js.NewExpr:
		return e.construct(x, s)

	case *js.ArrowFunc:
		return &jsType{kind: funcKind, fn: &jsFunc{params: x.Params, body: x.Body.List, scope: s, this: s.this, name: "arrow"}}

	case *js.FuncDecl:
		return &jsType{kind: funcKind, fn: &jsFunc{params: x.Params, body: x.Body.List, scope: s, name: "function"}}

	case *js.ClassDecl:
		return &jsType{kind: classKind, class: readClass(s.file, x)}
	}

	return unknown
}

func literal(x *js.LiteralExpr, s *scope) *jsType {
	switch x.TokenType {
	case js.StringToken:
		v, _ := stringValue(x)
		return &jsType{kind: stringKind, literals: []string{v}}
	case js.IntegerToken, js.BinaryToken, js.OctalToken, js.HexadecimalToken:
		return writtenInt
	case js.DecimalToken:
		return num
	case js.TrueToken, js.FalseToken:
		return boolean
	case js.NullToken:
		return null
	case js.ThisToken:
		if s.this != nil {
			return s.this
		}
	default:
		// a regular expression or a template part is nothing the answers hold
	}

	return unknown
}

// name is the type of a name: a local or one of the code around, then a top-level name of the file.
func (e *evaluator) name(n string, s *scope) *jsType {
	if n == "undefined" {
		return null
	}

	if b := s.lookup(n); b != nil {
		return resolve(b)
	}

	return e.topLevel(s.file, n)
}

func resolve(b *binding) *jsType {
	if b.done {
		return b.typ
	}
	if b.active {
		return unknown
	}

	b.active = true
	var t *jsType
	for _, init := range b.inits {
		t = merge(t, init())
	}
	b.active = false

	if t == nil {
		t = unknown
	}
	b.typ, b.done = t, true

	return t
}

// topLevel is the type of a name declared at the top of file f: a require, a class, a function or a constant.
func (e *evaluator) topLevel(f *jsFile, n string) *jsType {
	if f == nil {
		return unknown
	}

	key := "top:" + f.path + ":" + n
	if t, ok := e.cache[key]; ok {
		return t
	}
	if e.active[key] {
		return unknown
	}
	e.active[key] = true
	defer delete(e.active, key)

	t := unknown
	if r, ok := f.requires[n]; ok {
		t = e.require(r)
	} else if c, ok := f.classes[n]; ok {
		t = &jsType{kind: classKind, class: c}
	} else if fd, ok := f.funcs[n]; ok {
		t = &jsType{kind: funcKind, fn: &jsFunc{params: fd.Params, body: fd.Body.List, scope: fileScope(f), name: f.path + ":" + n}}
	} else if c, ok := f.consts[n]; ok {
		t = e.eval(c, fileScope(f))
	} else if b, ok := builtins[n]; ok {
		t = b
	}

	e.cache[key] = t

	return t
}

func fileScope(f *jsFile) *scope {
	return &scope{file: f, vars: map[string]*binding{}, sets: map[string][]fieldSet{}}
}

// require is what a require gives: the file's exports, or the member taken from them. Packages from outside the server are unknown, but
// for sequelize's DataTypes and Model, which the models are written against.
func (e *evaluator) require(r required) *jsType {
	if r.file == "" {
		if r.pkg == "sequelize" && r.member != "" {
			return &jsType{kind: objectKind, name: "sequelize." + r.member}
		}
		return unknown
	}

	f := e.prog.file(r.file)
	if f == nil {
		return unknown
	}

	t := e.exports(f)
	if r.member != "" {
		return e.member(t, r.member, false)
	}

	return t
}

// exports is what a file exports.
func (e *evaluator) exports(f *jsFile) *jsType {
	key := "exports:" + f.path
	if t, ok := e.cache[key]; ok {
		return t
	}
	if e.active[key] {
		return unknown
	}
	e.active[key] = true
	defer delete(e.active, key)

	t := unknown
	if f.exports != nil {
		t = e.eval(f.exports, fileScope(f))
	}

	if len(f.exported) > 0 {
		if t.kind != objectKind {
			t = &jsType{kind: objectKind}
		}
		for _, n := range sortedKeys(f.exported) {
			t = t.withField(n, e.eval(f.exported[n], fileScope(f)))
		}
	}

	e.cache[key] = t

	return t
}

func (e *evaluator) object(x *js.ObjectExpr, s *scope) *jsType {
	out := &jsType{kind: objectKind}
	for _, p := range x.List {
		if p.Spread {
			t := e.eval(p.Value, s)
			switch t.kind {
			case objectKind:
				for _, f := range t.props {
					out = out.withField(f.name, f.typ)
				}
				out.open = out.open || t.open
			case nullKind:
			default:
				out.open = true
			}
			continue
		}

		// a method written in place keeps its name on the method: { async getFilterData() { ... } }
		if m, ok := p.Value.(*js.MethodDecl); ok {
			name := propertyName(&m.Name.PropertyName)
			if name == "" {
				out.open = true
				continue
			}
			out = out.withField(name, &jsType{kind: funcKind, fn: &jsFunc{params: m.Params, body: m.Body.List, scope: s, this: s.this, name: name}})
			continue
		}

		name := propertyName(p.Name)
		if name == "" && p.Name == nil {
			name = identName(p.Value) // { a, b }
		}
		if name == "" {
			out.open = true
			continue
		}

		out = out.withField(name, e.eval(p.Value, s))
	}

	return out
}

func (e *evaluator) unary(x *js.UnaryExpr, s *scope) *jsType {
	switch x.Op {
	case js.AwaitToken, js.PreIncrToken, js.PreDecrToken, js.PostIncrToken, js.PostDecrToken:
		return e.eval(x.X, s)
	case js.NotToken:
		e.eval(x.X, s)
		return boolean
	case js.TypeofToken:
		return str
	case js.NegToken, js.PosToken, js.BitNotToken:
		t := e.eval(x.X, s)
		if t.kind == integerKind {
			return integer
		}
		return num
	case js.VoidToken, js.DeleteToken:
		return null
	default:
		// anything else is nothing the scan can tell
	}

	return unknown
}

func (e *evaluator) binary(x *js.BinaryExpr, s *scope) *jsType {
	switch x.Op {
	case js.EqToken:
		v := e.eval(x.Y, s)

		// middleware attaching a record to the request: req.libraryItem = await ...
		if dot, ok := x.X.(*js.DotExpr); ok {
			if on := e.eval(dot.X, s); on.kind == requestKind {
				n := identName(dot.Y)
				on.req.attached[n] = merge(on.req.attached[n], v)
			}
		}

		return v

	case js.OrToken, js.NullishToken:
		left, right := e.eval(x.X, s), e.eval(x.Y, s)
		if left.kind == nullKind {
			return right // null || 2 is always 2
		}
		return merge(left.notNull(), right)

	case js.AndToken:
		left := e.eval(x.X, s)
		right := e.eval(x.Y, s)
		if left.kind == booleanKind {
			return merge(boolean, right)
		}
		return right.orNull()

	case js.EqEqToken, js.EqEqEqToken, js.NotEqToken, js.NotEqEqToken, js.LtToken, js.GtToken, js.LtEqToken, js.GtEqToken, js.InstanceofToken, js.InToken:
		return boolean

	case js.AddToken:
		a, b := e.eval(x.X, s), e.eval(x.Y, s)
		switch {
		case a.kind == stringKind || b.kind == stringKind:
			return str
		case a.kind == integerKind && b.kind == integerKind:
			return integer
		case isNumber(a) && isNumber(b):
			return num
		}
		return unknown

	case js.SubToken, js.MulToken, js.ModToken, js.ExpToken:
		a, b := e.eval(x.X, s), e.eval(x.Y, s)
		if a.kind == integerKind && b.kind == integerKind {
			return integer
		}
		return num

	case js.DivToken:
		e.eval(x.X, s)
		e.eval(x.Y, s)
		return num

	case js.BitAndToken, js.BitOrToken, js.BitXorToken, js.LtLtToken, js.GtGtToken, js.GtGtGtToken:
		return integer
	default:
		// assignments such as += and the rest are nothing the scan can tell
	}

	return unknown
}

func isNumber(t *jsType) bool {
	return t.kind == numberKind || t.kind == integerKind || t.kind == dateKind
}

// construct is the type of new C(...): an instance of C, or a Date or a list.
func (e *evaluator) construct(x *js.NewExpr, s *scope) *jsType {
	switch identName(x.X) {
	case "Date":
		return date
	case "Array", "Set":
		return listOf(unknown)
	case "Map":
		return &jsType{kind: objectKind, open: true}
	case "Error", "TypeError":
		return unknown
	}

	t := e.eval(x.X, s)
	var args []*jsType
	if x.Args != nil {
		args = e.args(&x.Args.List, s)
	}

	if t.kind == classKind {
		inst := instanceOf(t.class)
		inst.ctorArgs = args
		return inst
	}

	// new Promise((resolve, reject) => { ... }) and the like run what they are given
	e.callbacks(args)

	return unknown
}

func (e *evaluator) args(list *[]js.Arg, s *scope) []*jsType {
	out := make([]*jsType, 0, len(*list))
	for _, a := range *list {
		out = append(out, e.eval(a.Value, s))
	}

	return out
}

// call is the type of a call: a method of what it is called on, a function, or one of the language's own.
func (e *evaluator) call(x *js.CallExpr, s *scope) *jsType {
	if dot, ok := x.X.(*js.DotExpr); ok {
		recv := e.eval(dot.X, s)
		name := identName(dot.Y)
		args := e.args(&x.Args.List, s)

		if t, ok := e.builtinMethod(recv, name, args, x, s); ok {
			return t
		}

		fn := e.member(recv, name, dot.Optional)
		if fn.kind == funcKind {
			return e.invoke(fn.fn, args)
		}

		e.callbacks(args)

		return unknown
	}

	fn := e.eval(x.X, s)
	args := e.args(&x.Args.List, s)
	if fn.kind == funcKind {
		return e.invoke(fn.fn, args)
	}

	if n := identName(x.X); n != "" && isGlobal(n, s) {
		if t, ok := globalCall(n, args); ok {
			return t
		}
	}

	e.callbacks(args)

	return unknown
}

// callbacks runs the functions handed to a call the scan cannot follow, such as promise.then(() => res.sendStatus(200)), so what they
// write into the answer is still seen.
func (e *evaluator) callbacks(args []*jsType) {
	for _, a := range args {
		if a.kind == funcKind && a.fn.builtin == nil {
			e.invoke(a.fn, nil)
		}
	}
}

// invoke runs a function with the arguments given and is the type of what it returns.
func (e *evaluator) invoke(fn *jsFunc, args []*jsType) *jsType {
	if fn.builtin != nil {
		return fn.builtin(e, fn.this, args)
	}

	// a call that is given the request or the response writes the answer, so it is followed every time and its result is not kept
	keep := true
	sig := make([]string, 0, len(args))
	for _, a := range args {
		if a.kind == requestKind || a.kind == responseKind {
			keep = false
		}
		sig = append(sig, typeKey(a))
	}

	key := fmt.Sprintf("call:%p:%s:%s", fn.body, typeKey(fn.this), strings.Join(sig, ","))
	if keep {
		if t, ok := e.cache[key]; ok {
			return t
		}
	}
	if e.active[key] {
		return unknown
	}
	e.active[key] = true
	defer delete(e.active, key)

	s := &scope{parent: fn.scope, file: fn.scope.file, vars: map[string]*binding{}, this: fn.this, sets: map[string][]fieldSet{}}
	if fn.this == nil && fn.scope != nil {
		s.this = fn.scope.this
	}
	e.bindParams(fn.params, args, s)
	declare(e, fn.body, s)

	// a function given the request, or its body or query on their own, is read afterwards for every parameter it takes
	for i, a := range args {
		if i >= len(fn.params.List) || a.req == nil {
			continue
		}

		v, ok := fn.params.List[i].Binding.(*js.Var)
		if !ok {
			continue
		}

		switch {
		case a.kind == requestKind:
			a.req.reached = append(a.req.reached, reqUse{body: fn.body, name: string(v.Name())})
		case a.kind == objectKind && strings.HasPrefix(a.name, "req."):
			a.req.reached = append(a.req.reached, reqUse{body: fn.body, name: string(v.Name()), part: strings.TrimPrefix(a.name, "req.")})
		}
	}

	t := e.run(fn.body, s)

	// a record written by a method such as toOldJSONExpanded is named for its class and the method
	if t != nil && t.kind == objectKind && t.name == "" {
		if class, method, ok := strings.Cut(fn.name, "."); ok {
			if n := jsonName(class, method); n != "" {
				named := *t
				named.name = n
				t = &named
			}
		}
	}

	if keep {
		e.cache[key] = t
	}

	return t
}

// bindParams gives each parameter the argument passed for it, or its default.
func (e *evaluator) bindParams(params js.Params, args []*jsType, s *scope) {
	for i, p := range params.List {
		var arg *jsType
		if i < len(args) {
			arg = args[i]
		}

		// the request and the response are known at once, so a statement using them is seen to write the answer
		if v, ok := p.Binding.(*js.Var); ok && arg != nil && (arg.kind == requestKind || arg.kind == responseKind) {
			s.bindType(string(v.Name()), arg)
			continue
		}

		def := p.Default
		e.bindPattern(p.Binding, func() *jsType {
			if arg != nil && arg.kind != unknownKind {
				if def != nil {
					return merge(arg, e.eval(def, s))
				}
				return arg
			}
			if def != nil {
				return e.eval(def, s)
			}
			if arg != nil {
				return arg
			}
			return null
		}, s)
	}

	if params.Rest != nil {
		e.bindPattern(params.Rest, func() *jsType { return listOf(unknown) }, s)
	}
}

// bindPattern binds the names in a binding, which may take fields of the value apart: const { a, b: c } = x.
func (e *evaluator) bindPattern(b js.IBinding, value func() *jsType, s *scope) {
	switch b := b.(type) {
	case *js.Var:
		s.bind(string(b.Name()), value)

	case *js.BindingObject:
		for _, item := range b.List {
			key := propertyName(item.Key)
			if key == "" {
				if v, ok := item.Value.Binding.(*js.Var); ok && item.Key == nil {
					key = string(v.Name())
				}
			}

			def := item.Value.Default
			e.bindPattern(item.Value.Binding, func() *jsType {
				t := e.member(value(), key, false)
				if def != nil {
					t = merge(t.notNull(), e.eval(def, s))
				}
				return t
			}, s)
		}
		if b.Rest != nil {
			s.bind(string(b.Rest.Name()), func() *jsType { return &jsType{kind: objectKind, open: true} })
		}

	case *js.BindingArray:
		for _, el := range b.List {
			if el.Binding == nil {
				continue
			}
			e.bindPattern(el.Binding, func() *jsType {
				t := value()
				if t.kind == arrayKind {
					return t.elem
				}
				return unknown
			}, s)
		}
	}
}

// declare binds every name a function body declares or assigns, so a use anywhere in it sees every value it is given.
func declare(e *evaluator, body []js.IStmt, s *scope) {
	walkStmts(body, func(st js.IStmt) {
		switch st := st.(type) {
		case *js.VarDecl:
			for _, el := range st.List {
				if el.Default == nil {
					if v, ok := el.Binding.(*js.Var); ok {
						s.bind(string(v.Name()), func() *jsType { return null })
					}
					continue
				}
				init := el.Default
				e.bindPattern(el.Binding, func() *jsType { return e.eval(init, s) }, s)
			}

		case *js.FuncDecl:
			if st.Name != nil {
				fd := st
				s.bindType(string(st.Name.Name()), &jsType{kind: funcKind, fn: &jsFunc{params: fd.Params, body: fd.Body.List, scope: s, name: string(fd.Name.Name())}})
			}

		case *js.ForOfStmt:
			value := st.Value
			if d, ok := st.Init.(*js.VarDecl); ok && len(d.List) == 1 {
				e.bindPattern(d.List[0].Binding, func() *jsType {
					t := e.eval(value, s)
					if t.kind == arrayKind {
						return t.elem
					}
					return unknown
				}, s)
			}

		case *js.ForInStmt:
			if d, ok := st.Init.(*js.VarDecl); ok && len(d.List) == 1 {
				e.bindPattern(d.List[0].Binding, func() *jsType { return str }, s)
			}

		case *js.TryStmt:
			if st.Binding != nil {
				e.bindPattern(st.Binding, func() *jsType { return unknown }, s)
			}
		}

		for _, x := range stmtExprs(st) {
			walkExpr(x, func(x js.IExpr) {
				if n, sets := fieldSets(x); n != "" {
					s.sets[n] = append(s.sets[n], sets...)
				}

				if call, ok := x.(*js.CallExpr); ok {
					for _, a := range call.Args.List {
						outerSets(e, call, a.Value, s, func() *scope { return s })
					}
					return
				}

				set, ok := x.(*js.BinaryExpr)
				if !ok {
					return
				}

				// total += x: the total takes the type of what is added
				if to, ok := set.X.(*js.Var); ok && compound(set.Op) {
					value, op := set.Y, set.Op
					s.bind(string(to.Name()), func() *jsType { return compoundType(e.eval(value, s), op) })
					return
				}

				if set.Op != js.EqToken {
					return
				}

				if to, ok := set.X.(*js.Var); ok {
					name, value := string(to.Name()), set.Y

					// x = x.map(...): the new value replaces the one it is made from, rather than joining it
					if b, ok := s.vars[name]; ok && refersTo(value, name) {
						prev := b.inits
						b.inits = []func() *jsType{func() *jsType {
							var t *jsType
							for _, p := range prev {
								t = merge(t, p())
							}
							if t == nil {
								t = unknown
							}

							inner := &scope{parent: s, file: s.file, vars: map[string]*binding{}, this: s.this, sets: map[string][]fieldSet{}}
							inner.bindType(name, t)

							// sort(x).slice(0, n) from a package the scan cannot follow leaves x as it was
							if v := e.eval(value, inner); v.kind != unknownKind {
								return v
							}

							return t
						}}
						return
					}

					s.bind(name, func() *jsType { return e.eval(value, s) })
				}
			})
		}
	})

	// a record held in a name gains the fields set on it later: const payload = {...}; payload.total = count
	for n, sets := range s.sets {
		b, ok := s.vars[n]
		if !ok {
			continue
		}

		inits := b.inits
		b.inits = []func() *jsType{func() *jsType {
			var t *jsType
			for _, init := range inits {
				t = merge(t, init())
			}
			// a name pushed onto is a list, whatever the scan made of its value
			if t != nil && t.kind == unknownKind && slices.ContainsFunc(sets, func(fs fieldSet) bool { return fs.field == "" }) {
				t = listOf(unknown)
			}
			if t != nil && t.kind == arrayKind {
				var pushed *jsType
				for _, fs := range sets {
					in := fs.scope(n, s)
					if fs.field != "" || in == nil {
						continue
					}
					v := e.eval(fs.value, in)
					if fs.spread && v.kind == arrayKind {
						v = v.elem
					}
					pushed = merge(pushed, v)
				}

				// a list whose items are not known, such as [], holds what is pushed onto it
				elem := t.elem
				switch {
				case pushed == nil:
				case elem.kind == unknownKind:
					elem = pushed
				default:
					elem = merge(elem, pushed)
				}
				out := listOf(elem)
				out.nullable = t.nullable
				return out
			}
			if t == nil || (t.kind != objectKind && t.kind != unknownKind) {
				return t
			}
			if t.kind == unknownKind {
				t = &jsType{kind: objectKind, open: true}
			}
			for _, fs := range sets {
				in := fs.scope(n, s)
				if fs.field == "" || in == nil {
					continue
				}
				ft := e.eval(fs.value, in)
				if fs.op != 0 {
					ft = compoundType(ft, fs.op)
				}
				if t.name != "" {
					e.extra[t.name] = appendProp(e.extra[t.name], fs.field, ft)
				}
				t = t.withField(fs.field, ft)
			}
			return t
		}}
	}
}

// compound is whether an operator assigns the result of arithmetic on the name: +=, -= and the rest.
func compound(op js.TokenType) bool {
	switch op {
	case js.AddEqToken, js.SubEqToken, js.MulEqToken, js.DivEqToken, js.ModEqToken, js.ExpEqToken:
		return true
	default:
		return false
	}
}

// compoundType is what x op= value leaves x holding: text for text added, a whole number for whole numbers, a number otherwise.
func compoundType(value *jsType, op js.TokenType) *jsType {
	switch {
	case op == js.AddEqToken && value.kind == stringKind:
		return str
	case value.kind == integerKind && op != js.DivEqToken:
		return integer
	default:
		return num
	}
}

// refersTo is whether an expression uses a name, outside any function written in it.
func refersTo(x js.IExpr, name string) bool {
	found := false
	walkExpr(x, func(y js.IExpr) {
		if v, ok := y.(*js.Var); ok && string(v.Name()) == name {
			found = true
		}
	})

	return found
}

func appendProp(list []prop, name string, t *jsType) []prop {
	for i, p := range list {
		if p.name == name {
			list[i].typ = merge(p.typ, t)
			return list
		}
	}

	return append(list, prop{name, t})
}

// run works through a function body: every return gives the result, and a statement that uses the request or the response is worked
// out for what it writes into the answer.
func (e *evaluator) run(body []js.IStmt, s *scope) *jsType {
	var out *jsType
	walkStmts(body, func(st js.IStmt) {
		switch st := st.(type) {
		case *js.ReturnStmt:
			if st.Value == nil {
				out = merge(out, null)
				return
			}
			out = merge(out, e.eval(st.Value, s))

		case *js.ExprStmt, *js.IfStmt, *js.VarDecl:
			for _, x := range stmtExprs(st) {
				if touchesAnswer(x, s) {
					e.eval(x, s)
				}
			}
		}
	})

	if out == nil {
		return null
	}

	return out
}

// touchesAnswer is whether an expression uses a name that holds the request or the response, so working it out may write the answer.
func touchesAnswer(x js.IExpr, s *scope) bool {
	if x == nil {
		return false
	}

	// callbacks count: .then(() => res.sendStatus(200)) writes the answer too
	found := false
	js.Walk(visitor(func(n js.INode) {
		if found {
			return
		}
		v, ok := n.(*js.Var)
		if !ok {
			return
		}
		if b := s.lookup(string(v.Name())); b != nil && b.done && (b.typ.kind == requestKind || b.typ.kind == responseKind) {
			found = true
		}
	}), x)

	return found
}

// typeKey is a short form of a type, to tell calls with different arguments apart.
func typeKey(t *jsType) string {
	if t == nil {
		return "-"
	}

	switch t.kind {
	case instanceKind, classKind:
		return fmt.Sprintf("%d:%s", t.kind, t.class.name)
	case arrayKind:
		return "[" + typeKey(t.elem) + "]"
	case objectKind:
		return "{" + t.name + "}"
	case funcKind:
		// a function handed in, as the loaders a timing helper is given, is told apart by its code, as what the call returns is what it does
		if t.fn != nil && t.fn.builtin == nil {
			return fmt.Sprintf("%d:%p", t.kind, t.fn.body)
		}
		return fmt.Sprint(t.kind)
	default:
		return fmt.Sprint(t.kind)
	}
}

// method is a class's method by name, or its parent's.
func (e *evaluator) method(c *jsClass, name string) *js.MethodDecl {
	for cl := c; cl != nil; cl = e.parent(cl) {
		if m := cl.methods[name]; m != nil {
			return m
		}
	}

	return nil
}

// parent is the class a class extends, when it is one of the server's.
func (e *evaluator) parent(c *jsClass) *jsClass {
	if c.extends == nil {
		return nil
	}

	t := e.eval(c.extends, fileScope(c.file))
	if t.kind == classKind && t.class != c {
		return t.class
	}

	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	return keys
}
