package main

import (
	"strconv"
	"strings"

	"github.com/tdewolff/parse/v2/js"
)

// dynamicFields are fields the server sets on a record from outside its class, where the scan cannot follow: a library item's media is
// whichever of its book or podcast it has, set in a hook when it is read.
var dynamicFields = map[string]map[string][]string{
	"LibraryItem": {"media": {"Book", "Podcast"}},
}

// member is the type of t.name.
func (e *evaluator) member(t *jsType, name string, optional bool) *jsType {
	if name == "" {
		return unknown
	}

	out := unknown
	switch t.kind {
	case objectKind:
		out = e.objectMember(t, name)

	case arrayKind, stringKind:
		if name == "length" {
			out = integer
		}

	case instanceKind:
		out = e.instanceMember(t, name)

	case classKind:
		out = e.staticMember(t, name)

	case requestKind:
		out = e.requestMember(t, name)

	case mixedKind:
		var m *jsType
		for _, alt := range t.of {
			m = merge(m, e.member(alt, name, optional))
		}
		if m != nil {
			out = m
		}

	default:
		// text, numbers, dates and the rest have no fields the scan reads
	}

	if t.nullable && optional {
		return out.orNull()
	}

	return out
}

func (e *evaluator) objectMember(t *jsType, name string) *jsType {
	switch t.name {
	case "req.params":
		return str
	case "req.query":
		t.req.addQuery(name)
		return str
	case "req.body":
		t.req.addBody(name)
		return unknown
	case "req.files":
		t.req.addFile(name)
		return unknown
	case "models":
		if m := e.models.byName[name]; m != nil {
			return &jsType{kind: classKind, class: m.class}
		}
	}

	if f := t.field(name); f != nil {
		return f
	}

	return unknown
}

// instanceMember is a field, getter or method of an instance: a record's column or link first, then what its class declares.
func (e *evaluator) instanceMember(t *jsType, name string) *jsType {
	c := t.class
	if c.name == "Database" && name == "models" {
		return &jsType{kind: objectKind, name: "models"}
	}

	if m := e.models.byClass[c]; m != nil {
		if col := m.column(name); col != nil {
			return col
		}
		if a, ok := m.assocs[name]; ok {
			if a.many {
				return listOf(instanceOf(a.target.class))
			}
			return instanceOf(a.target.class).orNull()
		}
	}

	if alts, ok := dynamicFields[c.name][name]; ok {
		var out *jsType
		for _, n := range alts {
			if m := e.models.byName[n]; m != nil {
				out = merge(out, instanceOf(m.class))
			}
		}
		if out != nil {
			return out
		}
	}

	for cl := c; cl != nil; cl = e.parent(cl) {
		if g := cl.getters[name]; g != nil {
			return e.invoke(&jsFunc{params: g.Params, body: g.Body.List, scope: fileScope(cl.file), this: t, name: cl.name + "." + name}, nil)
		}
		if m := cl.methods[name]; m != nil {
			return &jsType{kind: funcKind, fn: &jsFunc{params: m.Params, body: m.Body.List, scope: fileScope(cl.file), this: t, name: cl.name + "." + name}}
		}
		if fs, ok := cl.fields[name]; ok {
			return e.field(cl, fs, t, name)
		}
	}

	return unknown
}

// field is every value the methods give a field, each worked out in its method with its locals, and for the constructor with the arguments the
// instance was made with: ApiRouter's this.backupManager = Server.backupManager, where Server is what new ApiRouter(this) was given.
func (e *evaluator) field(cl *jsClass, fs []classField, t *jsType, name string) *jsType {
	sig := make([]string, 0, len(t.ctorArgs))
	for _, a := range t.ctorArgs {
		sig = append(sig, typeKey(a))
	}

	key := "field:" + cl.file.path + ":" + cl.name + "." + name + ":" + strings.Join(sig, ",")
	if v, ok := e.cache[key]; ok {
		return v
	}
	if e.active[key] {
		return unknown
	}
	e.active[key] = true
	defer delete(e.active, key)

	var v *jsType
	for _, f := range fs {
		s := &scope{parent: fileScope(cl.file), file: cl.file, vars: map[string]*binding{}, this: t, sets: map[string][]fieldSet{}}
		if f.method != nil {
			var args []*jsType
			if f.method == cl.methods["constructor"] {
				args = t.ctorArgs
			}
			e.bindParams(f.method.Params, args, s)
			declare(e, f.method.Body.List, s)
		}
		fv := e.eval(f.init, s)
		if f.op != 0 {
			fv = compoundType(fv, f.op)
		}
		v = merge(v, fv)
	}
	if v == nil {
		v = unknown
	}
	e.cache[key] = v

	return v
}

// constructed is an instance of a class as the server makes it, with the arguments of the first new C(...) found: new ApiRouter(this) in
// Server, so the router's fields can be followed to the server's managers. A class made nowhere is a bare instance.
func (e *evaluator) constructed(c *jsClass) *jsType {
	for _, fn := range sortedKeys(e.prog.files) {
		f := e.prog.files[fn]
		for _, cn := range sortedKeys(f.classes) {
			k := f.classes[cn]
			for _, mn := range sortedKeys(k.methods) {
				m := k.methods[mn]
				var site *js.NewExpr
				walkStmts(m.Body.List, func(st js.IStmt) {
					for _, x := range stmtExprs(st) {
						walkExpr(x, func(x js.IExpr) {
							if n, ok := x.(*js.NewExpr); ok && site == nil && identName(n.X) == c.name {
								site = n
							}
						})
					}
				})
				if site == nil {
					continue
				}

				s := &scope{parent: fileScope(f), file: f, vars: map[string]*binding{}, this: instanceOf(k), sets: map[string][]fieldSet{}}
				declare(e, m.Body.List, s)
				inst := instanceOf(c)
				if site.Args != nil {
					inst.ctorArgs = e.args(&site.Args.List, s)
				}
				return inst
			}
		}
	}

	return instanceOf(c)
}

// staticMember is a class's static method or one of a record's own: findOne, findAll, count and the rest.
func (e *evaluator) staticMember(t *jsType, name string) *jsType {
	for cl := t.class; cl != nil; cl = e.parent(cl) {
		if m := cl.statics[name]; m != nil {
			return &jsType{kind: funcKind, fn: &jsFunc{params: m.Params, body: m.Body.List, scope: fileScope(cl.file), this: t, name: cl.name + "." + name}}
		}
	}

	if e.models.byClass[t.class] == nil {
		return unknown
	}

	inst := instanceOf(t.class)
	var out *jsType
	switch name {
	case "findOne", "findByPk", "findOrCreate":
		out = inst.orNull()
	case "create", "build", "upsert":
		out = inst
	case "findAll", "bulkCreate":
		out = listOf(inst)
	case "count":
		out = integer
	case "sum", "max", "min":
		out = num
	case "findAndCountAll":
		out = (&jsType{kind: objectKind}).withField("rows", listOf(inst)).withField("count", integer)
	default:
		return unknown
	}

	return builtinFunc(name, func(*evaluator, *jsType, []*jsType) *jsType { return out })
}

// requestMember is a field of the request a handler is given.
func (e *evaluator) requestMember(t *jsType, name string) *jsType {
	switch name {
	case "user":
		if m := e.models.byName["User"]; m != nil {
			return instanceOf(m.class)
		}
		return unknown
	case "params", "query", "body", "files":
		return &jsType{kind: objectKind, name: "req." + name, req: t.req}
	case "path", "method", "originalUrl", "url", "ip", "hostname", "protocol", "baseUrl":
		return str
	case "headers", "cookies", "session":
		return &jsType{kind: objectKind, open: true}
	}

	if a, ok := t.req.attached[name]; ok {
		return a
	}

	return unknown
}

// builtinMethod is the type of a call of one of the language's own methods, on a list, text, a date, a function, the response, or a
// name the server does not declare such as Object or JSON. ok is false when the call is none of those.
func (e *evaluator) builtinMethod(recv *jsType, name string, args []*jsType, x *js.CallExpr, s *scope) (*jsType, bool) {
	if dot, ok := x.X.(*js.DotExpr); ok {
		if g := identName(dot.X); g != "" && isGlobal(g, s) {
			if t, ok := globalMethod(e, g, name, args); ok {
				return t, true
			}
		}
	}

	// a promise's own methods, on whatever the call before them gives: create(...).catch(...) is still what create gives
	if recv.kind != instanceKind || e.method(recv.class, name) == nil {
		switch name {
		case "catch", "finally":
			e.callbacks(args)
			return recv, true
		case "then":
			if len(args) > 0 && args[0].kind == funcKind && args[0].fn.builtin == nil {
				return e.invoke(args[0].fn, []*jsType{recv}), true
			}
		}
	}

	switch recv.kind {
	case responseKind:
		return e.respond(recv, name, args, x), true

	case arrayKind:
		return e.listMethod(recv, name, args)

	case stringKind:
		switch name {
		case "split", "match", "matchAll":
			return listOf(str), true
		case "includes", "startsWith", "endsWith", "localeCompare":
			if name == "localeCompare" {
				return integer, true
			}
			return boolean, true
		case "indexOf", "lastIndexOf", "search", "charCodeAt", "codePointAt":
			return integer, true
		default:
			return str, true
		}

	case integerKind, numberKind:
		switch name {
		case "toFixed", "toString", "toLocaleString", "toPrecision":
			return str, true
		}

	case dateKind:
		switch name {
		case "valueOf", "getTime", "getFullYear", "getMonth", "getDate", "getDay", "getHours", "getMinutes", "getSeconds", "getMilliseconds", "getTimezoneOffset":
			return integer, true
		case "toISOString", "toJSON", "toString", "toDateString", "toLocaleDateString", "toLocaleString", "toUTCString":
			return str, true
		}

	case funcKind:
		if recv.fn == nil {
			break
		}

		fn := *recv.fn
		switch name {
		case "bind":
			if len(args) > 0 {
				fn.this = args[0]
			}
			return &jsType{kind: funcKind, fn: &fn}, true
		case "call":
			if len(args) > 0 {
				fn.this = args[0]
				return e.invoke(&fn, args[1:]), true
			}
		case "apply":
			if len(args) > 0 {
				fn.this = args[0]
				return e.invoke(&fn, nil), true
			}
		}

	case instanceKind:
		if m := e.models.byClass[recv.class]; m != nil && e.method(recv.class, name) == nil {
			switch name {
			case "save", "update", "reload", "set", "increment", "decrement":
				return recv, true
			case "destroy", "restore":
				return null, true
			case "toJSON":
				return m.plain(), true
			case "get":
				if k, ok := stringValue(argExpr(x, 0)); ok {
					return e.member(recv, k, false), true
				}
				return m.plain(), true
			case "changed", "isNewRecord":
				return boolean, true
			}
		}

	case mixedKind:
		var out *jsType
		for _, alt := range recv.of {
			out = merge(out, e.callOn(alt, name, args, x, s))
		}
		if out != nil {
			return out, true
		}

	default:
		// the rest are followed as the server's own methods by the caller
	}

	// a stream written into the response answers a file
	if name == "pipe" && len(args) > 0 && args[0].kind == responseKind {
		args[0].resp.file = true
		return args[0], true
	}

	return nil, false
}

// callOn is the type of calling a method on one alternative of a mixed value.
func (e *evaluator) callOn(recv *jsType, name string, args []*jsType, x *js.CallExpr, s *scope) *jsType {
	if t, ok := e.builtinMethod(recv, name, args, x, s); ok {
		return t
	}

	fn := e.member(recv, name, false)
	if fn.kind == funcKind {
		return e.invoke(fn.fn, args)
	}

	return unknown
}

func (e *evaluator) listMethod(recv *jsType, name string, args []*jsType) (*jsType, bool) {
	elem := recv.elem
	switch name {
	case "map", "flatMap":
		if len(args) > 0 && args[0].kind == funcKind {
			t := e.invoke(args[0].fn, []*jsType{elem, integer})
			if name == "flatMap" && t.kind == arrayKind {
				t = t.elem
			}
			return listOf(t), true
		}
		return listOf(unknown), true
	case "filter", "sort", "slice", "reverse", "concat", "splice", "toSorted", "toReversed", "flat":
		if name == "filter" && len(args) > 0 && args[0].kind == funcKind {
			e.invoke(args[0].fn, []*jsType{elem, integer})
		}
		return recv, true
	case "find", "at", "pop", "shift", "findLast":
		if len(args) > 0 && args[0].kind == funcKind {
			e.invoke(args[0].fn, []*jsType{elem, integer})
		}
		return elem.orNull(), true
	case "some", "every", "includes":
		return boolean, true
	case "indexOf", "findIndex", "lastIndexOf", "push", "unshift", "findLastIndex":
		return integer, true
	case "join":
		return str, true
	case "forEach":
		if len(args) > 0 && args[0].kind == funcKind {
			e.invoke(args[0].fn, []*jsType{elem, integer})
		}
		return null, true
	case "reduce":
		if len(args) > 1 && args[0].kind == funcKind {
			return merge(args[1], e.invoke(args[0].fn, []*jsType{args[1], elem, integer})), true
		}
		return unknown, true
	case "keys":
		return listOf(integer), true
	case "entries":
		return listOf(listOf(unknown)), true
	}

	return nil, false
}

// respond records what a call on the response writes into the answer.
func (e *evaluator) respond(res *jsType, name string, args []*jsType, x *js.CallExpr) *jsType {
	a := res.resp
	status := res.status
	if status == 0 {
		status = 200
	}

	switch name {
	case "status":
		if n, ok := intValue(argExpr(x, 0)); ok {
			return &jsType{kind: responseKind, resp: a, status: n}
		}
		return res

	case "json":
		if len(args) > 0 {
			a.addJSON(status, args[0])
		}
	case "send":
		switch {
		case len(args) == 0:
			a.addEmpty(status)
		case args[0].kind == stringKind || args[0].kind == unknownKind || args[0].kind == integerKind:
			a.addText(status)
		default:
			a.addJSON(status, args[0])
		}
	case "sendStatus":
		if n, ok := intValue(argExpr(x, 0)); ok {
			a.addStatus(n)
		}
	case "end":
		a.addEmpty(status)
	case "sendFile", "download", "write", "attachment":
		a.file = true
	case "on", "once":
		e.callbacks(args)
	case "redirect":
		a.addEmpty(302)
	}

	return res
}

func argExpr(x *js.CallExpr, i int) js.IExpr {
	if i < len(x.Args.List) {
		return x.Args.List[i].Value
	}

	return nil
}

// intValue is the whole number written out in e, and whether it is one.
func intValue(e js.IExpr) (int, bool) {
	lit, ok := e.(*js.LiteralExpr)
	if !ok || lit.TokenType != js.IntegerToken {
		return 0, false
	}

	n, err := strconv.Atoi(string(lit.Data))

	return n, err == nil
}

// isGlobal is whether a name is the language's own, not one the code declares.
func isGlobal(n string, s *scope) bool {
	if b := s.lookup(n); b != nil {
		return false
	}

	if f := s.file; f != nil {
		if _, ok := f.requires[n]; ok {
			return false
		}
		if _, ok := f.consts[n]; ok {
			return false
		}
		if _, ok := f.classes[n]; ok {
			return false
		}
		if _, ok := f.funcs[n]; ok {
			return false
		}
	}

	return true
}

// globalMethod is the type of a method of one of the language's own objects: Object.keys, JSON.parse, Math.round and the like.
func globalMethod(e *evaluator, g, name string, args []*jsType) (*jsType, bool) {
	first := unknown
	if len(args) > 0 {
		first = args[0]
	}

	switch g {
	case "Object":
		switch name {
		case "keys":
			return listOf(str), true
		case "values":
			var v *jsType
			for _, p := range first.props {
				v = merge(v, p.typ)
			}
			if v == nil {
				v = unknown
			}
			return listOf(v), true
		case "entries":
			return listOf(listOf(unknown)), true
		case "assign":
			out := &jsType{kind: objectKind}
			for _, a := range args {
				if a.kind == objectKind {
					for _, p := range a.props {
						out = out.withField(p.name, p.typ)
					}
					out.open = out.open || a.open
				} else if a.kind != nullKind {
					out.open = true
				}
			}
			return out, true
		case "fromEntries":
			return &jsType{kind: objectKind, open: true}, true
		}

	case "JSON":
		switch name {
		case "stringify":
			return str, true
		case "parse":
			return unknown, true
		}

	case "Math":
		switch name {
		case "floor", "ceil", "round", "trunc", "sign":
			return integer, true
		}
		return num, true

	case "Promise":
		switch name {
		case "all", "allSettled", "resolve":
			return first, true
		}

	case "Date":
		if name == "now" || name == "parse" || name == "UTC" {
			return integer, true
		}

	case "Number":
		switch name {
		case "isInteger", "isNaN", "isFinite":
			return boolean, true
		case "parseInt":
			return integer, true
		}
		return num, true

	case "Array":
		switch name {
		case "isArray":
			return boolean, true
		case "from":
			if len(args) > 1 && args[1].kind == funcKind {
				return listOf(e.invoke(args[1].fn, []*jsType{unknown, integer})), true
			}
			if first.kind == arrayKind {
				return first, true
			}
			return listOf(unknown), true
		}

	case "Buffer", "console", "process":
		return unknown, true
	}

	return nil, false
}

// globalCall is the type of a call of one of the language's own functions, by name.
func globalCall(name string, args []*jsType) (*jsType, bool) {
	switch name {
	case "parseInt":
		return integer, true
	case "parseFloat", "Number":
		return num, true
	case "String", "encodeURIComponent", "decodeURIComponent", "encodeURI", "decodeURI":
		return str, true
	case "Boolean", "isNaN", "isFinite":
		return boolean, true
	case "structuredClone":
		if len(args) > 0 {
			return args[0], true
		}
	case "require", "setTimeout", "clearTimeout", "setInterval":
		return unknown, true
	}

	return nil, false
}

func builtinFunc(name string, fn func(*evaluator, *jsType, []*jsType) *jsType) *jsType {
	return &jsType{kind: funcKind, fn: &jsFunc{name: name, builtin: fn}}
}

// builtins are the language's own names the scan gives a type when they are used as values.
var builtins = map[string]*jsType{}
