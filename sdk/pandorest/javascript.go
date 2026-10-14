package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

// program is the server's JavaScript, every file parsed, so a handler can be followed into whatever it calls.
type program struct {
	files map[string]*jsFile // by path from the checkout's root, with forward slashes
}

// jsFile is one parsed file and its top-level names: what it requires, its classes, functions and other bindings, and what it exports.
type jsFile struct {
	path     string
	ast      *js.AST
	requires map[string]required
	classes  map[string]*jsClass
	funcs    map[string]*js.FuncDecl
	consts   map[string]js.IExpr
	exports  js.IExpr            // module.exports = ...
	exported map[string]js.IExpr // module.exports.name = ...
}

// required is a name a file takes from another with require: the whole module, or one member of it.
type required struct {
	file   string // the file, or "" for a package from outside the server
	pkg    string // the package, when it is from outside
	member string // the member taken, as in const { DataTypes } = require('sequelize')
}

// jsClass is a class and its members, by name.
type jsClass struct {
	name    string
	file    *jsFile
	extends js.IExpr
	methods map[string]*js.MethodDecl
	getters map[string]*js.MethodDecl
	statics map[string]*js.MethodDecl
	fields  map[string][]classField
}

// classField is one value a class gives a field: a declared field, or this.name = value in a method, the constructor's first.
type classField struct {
	init   js.IExpr
	method *js.MethodDecl // the method that sets it, or nil for a declared field
	op     js.TokenType   // for this.name += value, the operator; 0 for =
}

// load parses every .js file under dir, which is a path from the checkout's root.
func load(root fs.FS, dir string) (*program, error) {
	p := &program{files: map[string]*jsFile{}}

	if err := fs.WalkDir(root, dir, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".js") {
			return err
		}

		b, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}

		ast, err := js.Parse(parse.NewInputBytes(b), js.Options{})
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}

		p.files[name] = readFile(name, ast)

		return nil
	}); err != nil {
		return nil, err
	}

	return p, nil
}

// readFile finds a file's top-level names.
func readFile(name string, ast *js.AST) *jsFile {
	f := &jsFile{path: name, ast: ast, requires: map[string]required{}, classes: map[string]*jsClass{}, funcs: map[string]*js.FuncDecl{}, consts: map[string]js.IExpr{}, exported: map[string]js.IExpr{}}

	for _, st := range ast.List {
		switch st := st.(type) {
		case *js.ClassDecl:
			if st.Name != nil {
				f.classes[string(st.Name.Name())] = readClass(f, st)
			}

		case *js.FuncDecl:
			if st.Name != nil {
				f.funcs[string(st.Name.Name())] = st
			}

		case *js.VarDecl:
			f.readDecl(st)

		case *js.ExprStmt:
			f.readExport(st.Value)
		}
	}

	return f
}

// readDecl takes the names a top-level const, let or var declares: a require, a class or function written as a value, or anything else.
func (f *jsFile) readDecl(d *js.VarDecl) {
	for _, el := range d.List {
		if el.Default == nil {
			continue
		}

		switch b := el.Binding.(type) {
		case *js.Var:
			name := string(b.Name())
			if r, ok := f.requireOf(el.Default, ""); ok {
				f.requires[name] = r
				continue
			}

			if c, ok := el.Default.(*js.ClassDecl); ok {
				f.classes[name] = readClass(f, c)
				continue
			}

			f.consts[name] = el.Default

		case *js.BindingObject:
			for _, item := range b.List {
				v, ok := item.Value.Binding.(*js.Var)
				if !ok || item.Key == nil {
					continue
				}

				if r, ok := f.requireOf(el.Default, propertyName(item.Key)); ok {
					f.requires[string(v.Name())] = r
				}
			}
		}
	}
}

// requireOf is what require('...') in e names, with the member taken, and whether e is a require at all. require('x').member counts too.
func (f *jsFile) requireOf(e js.IExpr, member string) (required, bool) {
	if dot, ok := e.(*js.DotExpr); ok && member == "" {
		member = identName(dot.Y)
		e = dot.X
	}

	call, ok := e.(*js.CallExpr)
	if !ok || identName(call.X) != "require" || len(call.Args.List) != 1 {
		return required{}, false
	}

	spec, ok := stringValue(call.Args.List[0].Value)
	if !ok {
		return required{}, false
	}

	if !strings.HasPrefix(spec, ".") {
		return required{pkg: spec, member: member}, true
	}

	return required{file: path.Join(path.Dir(f.path), spec), member: member}, true
}

// readExport takes module.exports = ... and module.exports.name = ....
func (f *jsFile) readExport(e js.IExpr) {
	set, ok := e.(*js.BinaryExpr)
	if !ok || set.Op != js.EqToken {
		return
	}

	dot, ok := set.X.(*js.DotExpr)
	if !ok {
		return
	}

	if isMember(dot, "module", "exports") {
		f.exports = set.Y
		return
	}

	if inner, ok := dot.X.(*js.DotExpr); ok && isMember(inner, "module", "exports") {
		f.exported[identName(dot.Y)] = set.Y
	}
}

// readClass takes a class's methods, getters, static methods and fields.
func readClass(f *jsFile, c *js.ClassDecl) *jsClass {
	name := ""
	if c.Name != nil {
		name = string(c.Name.Name())
	}

	cl := &jsClass{name: name, file: f, extends: c.Extends, methods: map[string]*js.MethodDecl{}, getters: map[string]*js.MethodDecl{}, statics: map[string]*js.MethodDecl{}, fields: map[string][]classField{}}
	for _, el := range c.List {
		if el.Method == nil {
			if el.Init != nil && !el.Static {
				n := propertyName(&el.Name.PropertyName)
				cl.fields[n] = append(cl.fields[n], classField{init: el.Init})
			}
			continue
		}

		m := el.Method
		n := propertyName(&m.Name.PropertyName)
		switch {
		case m.Static:
			cl.statics[n] = m
		case m.Get:
			cl.getters[n] = m
		case m.Set:
		default:
			cl.methods[n] = m
		}
	}

	// every value the methods give a field, the constructor's first: a field set to 0 there and to a fraction later holds both
	names := sortedKeys(cl.methods)
	if i := slices.Index(names, "constructor"); i > 0 {
		names = slices.Insert(slices.Delete(names, i, i+1), 0, "constructor")
	}
	for _, mn := range names {
		m := cl.methods[mn]
		walkStmts(m.Body.List, func(st js.IStmt) {
			es, ok := st.(*js.ExprStmt)
			if !ok {
				return
			}

			set, ok := es.Value.(*js.BinaryExpr)
			if !ok || set.Op != js.EqToken && !compound(set.Op) {
				return
			}

			if dot, ok := set.X.(*js.DotExpr); ok && isThis(dot.X) {
				if n := identName(dot.Y); n != "" {
					f := classField{init: set.Y, method: m}
					if set.Op != js.EqToken {
						f.op = set.Op
					}
					cl.fields[n] = append(cl.fields[n], f)
				}
			}
		})
	}

	return cl
}

// file is the parsed file a require names, which may leave out .js or name a folder's index.js.
func (p *program) file(name string) *jsFile {
	for _, n := range []string{name, name + ".js", path.Join(name, "index.js")} {
		if f, ok := p.files[n]; ok {
			return f
		}
	}

	return nil
}

// walkStmts calls fn for every statement in list and in the blocks inside them, but not inside a function, which runs when called and not here.
func walkStmts(list []js.IStmt, fn func(js.IStmt)) {
	for _, st := range list {
		walkStmt(st, fn)
	}
}

func walkStmt(st js.IStmt, fn func(js.IStmt)) {
	// a try with no catch holds a nil block, which is not a nil statement
	if b, ok := st.(*js.BlockStmt); st == nil || ok && b == nil {
		return
	}

	fn(st)

	switch st := st.(type) {
	case *js.BlockStmt:
		walkStmts(st.List, fn)
	case *js.IfStmt:
		walkStmt(st.Body, fn)
		walkStmt(st.Else, fn)
	case *js.ForStmt:
		walkStmt(st.Body, fn)
	case *js.ForInStmt:
		walkStmt(st.Body, fn)
	case *js.ForOfStmt:
		walkStmt(st.Body, fn)
	case *js.WhileStmt:
		walkStmt(st.Body, fn)
	case *js.DoWhileStmt:
		walkStmt(st.Body, fn)
	case *js.LabelledStmt:
		walkStmt(st.Value, fn)
	case *js.TryStmt:
		walkStmt(st.Body, fn)
		walkStmt(st.Catch, fn)
		walkStmt(st.Finally, fn)
	case *js.SwitchStmt:
		for _, c := range st.List {
			walkStmts(c.List, fn)
		}
	}
}

// walkExpr calls fn for e and every expression inside it, but not inside a function written in it.
func walkExpr(e js.IExpr, fn func(js.IExpr)) {
	if e == nil {
		return
	}

	fn(e)

	switch e := e.(type) {
	case *js.DotExpr:
		walkExpr(e.X, fn)
	case *js.IndexExpr:
		walkExpr(e.X, fn)
		walkExpr(e.Y, fn)
	case *js.CallExpr:
		walkExpr(e.X, fn)
		for _, a := range e.Args.List {
			walkExpr(a.Value, fn)
		}
	case *js.NewExpr:
		walkExpr(e.X, fn)
		if e.Args != nil {
			for _, a := range e.Args.List {
				walkExpr(a.Value, fn)
			}
		}
	case *js.UnaryExpr:
		walkExpr(e.X, fn)
	case *js.BinaryExpr:
		walkExpr(e.X, fn)
		walkExpr(e.Y, fn)
	case *js.CondExpr:
		walkExpr(e.Cond, fn)
		walkExpr(e.X, fn)
		walkExpr(e.Y, fn)
	case *js.GroupExpr:
		walkExpr(e.X, fn)
	case *js.CommaExpr:
		for _, x := range e.List {
			walkExpr(x, fn)
		}
	case *js.ArrayExpr:
		for _, el := range e.List {
			walkExpr(el.Value, fn)
		}
	case *js.ObjectExpr:
		for _, p := range e.List {
			walkExpr(p.Value, fn)
		}
	case *js.TemplateExpr:
		for _, part := range e.List {
			walkExpr(part.Expr, fn)
		}
	}
}

// stmtExprs are the expressions a statement holds directly, not those of the statements inside it.
func stmtExprs(st js.IStmt) []js.IExpr {
	switch st := st.(type) {
	case *js.ExprStmt:
		return []js.IExpr{st.Value}
	case *js.ReturnStmt:
		return []js.IExpr{st.Value}
	case *js.VarDecl:
		var out []js.IExpr
		for _, el := range st.List {
			out = append(out, el.Default)
		}
		return out
	case *js.IfStmt:
		return []js.IExpr{st.Cond}
	case *js.ForOfStmt:
		return []js.IExpr{st.Value}
	case *js.ForStmt:
		return []js.IExpr{st.Init, st.Cond, st.Post}
	case *js.WhileStmt:
		return []js.IExpr{st.Cond}
	case *js.DoWhileStmt:
		return []js.IExpr{st.Cond}
	case *js.SwitchStmt:
		return []js.IExpr{st.Init}
	case *js.ThrowStmt:
		return []js.IExpr{st.Value}
	}

	return nil
}

// identName is the name of a plain identifier or an identifier-like literal, such as the y of x.y; "" for anything else.
func identName(e js.IExpr) string {
	switch e := e.(type) {
	case *js.Var:
		return string(e.Name())
	case *js.LiteralExpr:
		if e.TokenType != js.StringToken {
			return string(e.Data)
		}
	case js.LiteralExpr:
		if e.TokenType != js.StringToken {
			return string(e.Data)
		}
	}

	return ""
}

// stringValue is the text of a string written out in the source, and whether e is one.
func stringValue(e js.IExpr) (string, bool) {
	switch e := e.(type) {
	case *js.LiteralExpr:
		if e.TokenType == js.StringToken && len(e.Data) >= 2 {
			return string(e.Data[1 : len(e.Data)-1]), true
		}
	case *js.TemplateExpr:
		if e.Tag == nil && len(e.List) == 0 {
			return string(e.Tail[1 : len(e.Tail)-1]), true
		}
	}

	return "", false
}

// propertyName is a property's name as written, unquoted; "" for one computed when the code runs.
func propertyName(p *js.PropertyName) string {
	if p == nil || p.IsComputed() {
		return ""
	}

	if p.Literal.TokenType == js.StringToken {
		s, _ := stringValue(&p.Literal)
		return s
	}

	return string(p.Literal.Data)
}

func isThis(e js.IExpr) bool {
	lit, ok := e.(*js.LiteralExpr)
	return ok && lit.TokenType == js.ThisToken
}

// isMember is whether e is x.y written with those names.
func isMember(e js.IExpr, x, y string) bool {
	dot, ok := e.(*js.DotExpr)
	return ok && identName(dot.X) == x && identName(dot.Y) == y
}

// readVersion is the release a checkout is at, from its package.json.
func readVersion(root fs.FS) (string, error) {
	b, err := fs.ReadFile(root, "package.json")
	if err != nil {
		return "", fmt.Errorf("reading the checkout's package.json (is it an Audiobookshelf checkout?): %w", err)
	}

	v, err := packageVersion(b)
	if err != nil {
		return "", fmt.Errorf("package.json: %w", err)
	}

	return v, nil
}

// checkout opens an Audiobookshelf checkout for reading.
func checkout(dir string) (fs.FS, error) {
	st, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}

	return os.DirFS(dir), nil
}
