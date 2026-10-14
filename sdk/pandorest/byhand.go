package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// byhand is what the hand-written client says of the API: for each route, the type it reads the answer into and the body it sends. The
// live suites prove those types against a running server, so they type what the source leaves open: JSON stored in a column, an object the
// code fills in later, a body field the server reads without saying what it holds.
type byhand struct {
	types      map[string]ast.Expr      // the package's named types
	ownJSON    map[string]bool          // types that read their own JSON, whose Go shape is not what the server sends
	calls      map[string][]clientCall  // by verb and path, GET /api/libraries/:id
	funcs      map[string]*ast.FuncDecl // the client's methods, by name
	unmatched  []string                 // requests whose route no operation has
	conflicts  []string                 // values the client types other than the scan, where and how
	filled     int                      // values the client typed that the scan did not
	filledOps  map[string]bool          // operations a value was typed in
	visitedRef map[string]bool
}

// clientCall is one request the client makes: what it reads the answer into, what it sends, and the method that makes it.
type clientCall struct {
	method string
	answer *schema
	body   *schema
}

// readClient reads the hand-written client's source, a package folder of root.
func readClient(root fs.FS, dir string) (*byhand, error) {
	entries, err := fs.ReadDir(root, dir)
	if err != nil {
		return nil, err
	}

	b := &byhand{types: map[string]ast.Expr{}, ownJSON: map[string]bool{}, calls: map[string][]clientCall{}, funcs: map[string]*ast.FuncDecl{}, filledOps: map[string]bool{}, visitedRef: map[string]bool{}}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		src, err := fs.ReadFile(root, path.Join(dir, name))
		if err != nil {
			return nil, err
		}

		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}

	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					if ts, ok := sp.(*ast.TypeSpec); ok {
						b.types[ts.Name.Name] = ts.Type
					}
				}

			case *ast.FuncDecl:
				recv := receiverName(d)
				if d.Name.Name == "UnmarshalJSON" && recv != "" {
					b.ownJSON[recv] = true
				}
				if recv == "Client" {
					b.funcs[d.Name.Name] = d
				}
			}
		}
	}

	for _, n := range sortedKeys(b.funcs) {
		b.readMethod(b.funcs[n])
	}

	return b, nil
}

func receiverName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return ""
	}

	t := d.Recv.List[0].Type
	if st, ok := t.(*ast.StarExpr); ok {
		t = st.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}

	return ""
}

// requestArgs are where each of the client's request helpers takes the path, the body and the answer: c.get(ctx, path, query, &out).
var requestArgs = map[string]struct {
	verb             string
	path, body, into int
}{
	"get":   {"GET", 1, -1, 3},
	"post":  {"POST", 1, 3, 4},
	"patch": {"PATCH", 1, 2, 3},
	"del":   {"DELETE", 1, -1, -1},
	"do":    {"", 2, 4, 5},
}

// readMethod finds every request a method makes and what it reads and sends.
func (b *byhand) readMethod(fn *ast.FuncDecl) {
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !isGoIdent(sel.X, "c") {
			return true
		}

		args, ok := requestArgs[sel.Sel.Name]
		if !ok || len(call.Args) <= args.path {
			return true
		}

		verb := args.verb
		if verb == "" {
			m, isSel := call.Args[1].(*ast.SelectorExpr)
			if !isSel || !strings.HasPrefix(m.Sel.Name, "Method") {
				return true
			}
			verb = strings.ToUpper(strings.TrimPrefix(m.Sel.Name, "Method"))
		}

		p, ok := requestPath(call.Args[args.path], fn)
		if !ok {
			return true
		}

		c := clientCall{method: fn.Name.Name}
		if args.into >= 0 && args.into < len(call.Args) {
			if t := b.typeOf(call.Args[args.into], fn); t != nil {
				c.answer = b.schemaOf(t, map[string]bool{})
			}
		}
		if args.body >= 0 && args.body < len(call.Args) {
			c.body = b.bodyOf(call.Args[args.body], fn)
		}

		key := verb + " " + p
		b.calls[key] = append(b.calls[key], c)

		return true
	})
}

// requestPath is the route a path expression builds, with every value written :id: "/api/items/"+url.PathEscape(id) is /api/items/:id. A path
// put in a local first is followed to what it was given.
func requestPath(e ast.Expr, fn *ast.FuncDecl) (string, bool) {
	if id, ok := e.(*ast.Ident); ok {
		if v := localValue(id.Name, fn); v != nil {
			e = v
		}
	}

	var sb strings.Builder
	for _, part := range goOperands(e) {
		if lit, ok := part.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return "", false
			}
			sb.WriteString(s)
			continue
		}
		sb.WriteString(":id")
	}

	p, _, _ := strings.Cut(sb.String(), "?")
	if !strings.HasPrefix(p, "/") {
		return "", false
	}

	return normalPath(p), true
}

var paramPart = regexp.MustCompile(`:[A-Za-z]+|\{[A-Za-z]+\}`)

// normalPath is a path with every parameter written :id and no doubled or trailing slash, so the client's and the document's agree.
func normalPath(p string) string {
	p = paramPart.ReplaceAllString(p, ":id")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}

	return strings.TrimSuffix(p, "/")
}

func goOperands(e ast.Expr) []ast.Expr {
	if bin, ok := e.(*ast.BinaryExpr); ok && bin.Op == token.ADD {
		return append(goOperands(bin.X), goOperands(bin.Y)...)
	}

	return []ast.Expr{e}
}

func isGoIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// localValue is the value a name is first given in a function: name := value.
func localValue(name string, fn *ast.FuncDecl) ast.Expr {
	var out ast.Expr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || out != nil || as.Tok != token.DEFINE {
			return out == nil
		}
		for i, l := range as.Lhs {
			if isGoIdent(l, name) && i < len(as.Rhs) {
				out = as.Rhs[i]
			}
		}
		return out == nil
	})

	return out
}

// typeOf is the declared type of what an expression names or makes, or nil: a local, a parameter or result, &T{}, or a field of one.
func (b *byhand) typeOf(e ast.Expr, fn *ast.FuncDecl) ast.Expr {
	switch e := e.(type) {
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return b.typeOf(e.X, fn)
		}

	case *ast.CompositeLit:
		return e.Type

	case *ast.Ident:
		return b.declared(e.Name, fn)

	case *ast.SelectorExpr:
		if st, ok := b.underlying(b.typeOf(e.X, fn)).(*ast.StructType); ok {
			for _, f := range st.Fields.List {
				for _, n := range f.Names {
					if n.Name == e.Sel.Name {
						return f.Type
					}
				}
			}
		}
	}

	return nil
}

// declared is the type a function gives a name: a parameter or result, var name T, or name := value.
func (b *byhand) declared(name string, fn *ast.FuncDecl) ast.Expr {
	for _, list := range []*ast.FieldList{fn.Type.Params, fn.Type.Results} {
		if list == nil {
			continue
		}
		for _, f := range list.List {
			for _, n := range f.Names {
				if n.Name == name {
					return f.Type
				}
			}
		}
	}

	var out ast.Expr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if out != nil {
			return false
		}

		switch n := n.(type) {
		case *ast.ValueSpec:
			for i, id := range n.Names {
				if id.Name != name {
					continue
				}
				if n.Type != nil {
					out = n.Type
				} else if i < len(n.Values) {
					out = b.typeOf(n.Values[i], fn)
				}
			}

		case *ast.AssignStmt:
			if n.Tok != token.DEFINE {
				return true
			}
			for i, l := range n.Lhs {
				if isGoIdent(l, name) && i < len(n.Rhs) {
					out = b.typeOf(n.Rhs[i], fn)
				}
			}
		}

		return out == nil
	})

	return out
}

// underlying is a named type of the package followed to what it is, or the type as it is.
func (b *byhand) underlying(t ast.Expr) ast.Expr {
	for range 8 {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
		case *ast.Ident:
			if d, ok := b.types[x.Name]; ok {
				t = d
				continue
			}
			return t
		default:
			return t
		}
	}

	return t
}

// bodyOf is the schema of what a request sends: a struct of the package, or a map written out with its keys.
func (b *byhand) bodyOf(e ast.Expr, fn *ast.FuncDecl) *schema {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	if id, ok := e.(*ast.Ident); ok {
		if v := localValue(id.Name, fn); v != nil {
			e = v
		}
	}

	if lit, ok := e.(*ast.CompositeLit); ok {
		if _, isMap := lit.Type.(*ast.MapType); isMap {
			s := &schema{Type: "object", Properties: map[string]*schema{}}
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				k, ok := kv.Key.(*ast.BasicLit)
				if !ok || k.Kind != token.STRING {
					continue
				}
				name, err := strconv.Unquote(k.Value)
				if err != nil {
					continue
				}
				s.Properties[name] = b.valueSchema(kv.Value, fn)
			}
			return s
		}
	}

	if t := b.typeOf(e, fn); t != nil {
		return b.schemaOf(t, map[string]bool{})
	}

	return nil
}

// valueSchema is the schema of a value written in a map: a literal, or a name of a declared type.
func (b *byhand) valueSchema(e ast.Expr, fn *ast.FuncDecl) *schema {
	switch e := e.(type) {
	case *ast.BasicLit:
		switch e.Kind {
		case token.STRING:
			return &schema{Type: "string"}
		case token.INT:
			return &schema{Type: "integer"}
		case token.FLOAT:
			return &schema{Type: "number"}
		default:
			return &schema{}
		}
	case *ast.Ident:
		if e.Name == "true" || e.Name == "false" {
			return &schema{Type: "boolean"}
		}
	}

	if t := b.typeOf(e, fn); t != nil {
		return b.schemaOf(t, map[string]bool{})
	}

	return &schema{}
}

// schemaOf is a Go type as the JSON it reads: a struct by its json tags, a pointer as one that may be null, a slice as a list. A type that
// reads its own JSON, json.RawMessage and any say nothing of the shape, so they are left open.
func (b *byhand) schemaOf(t ast.Expr, seen map[string]bool) *schema {
	switch x := t.(type) {
	case *ast.Ident:
		switch x.Name {
		case "string":
			return &schema{Type: "string"}
		case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
			return &schema{Type: "integer"}
		case "float32", "float64":
			return &schema{Type: "number"}
		case "bool":
			return &schema{Type: "boolean"}
		}

		d, ok := b.types[x.Name]
		if !ok || b.ownJSON[x.Name] || seen[x.Name] {
			return &schema{}
		}

		next := maps.Clone(seen)
		next[x.Name] = true

		return b.schemaOf(d, next)

	case *ast.StarExpr:
		s := b.schemaOf(x.X, seen)
		s.Nullable = s.Ref == ""
		return s

	case *ast.ArrayType:
		return &schema{Type: "array", Items: b.schemaOf(x.Elt, seen)}

	case *ast.MapType:
		return &schema{Type: "object", AdditionalProperties: true}

	case *ast.SelectorExpr:
		if isGoIdent(x.X, "time") && x.Sel.Name == "Time" {
			return &schema{Type: "string", Format: "date-time"}
		}

	case *ast.StructType:
		s := &schema{Type: "object", Properties: map[string]*schema{}}
		for _, f := range x.Fields.List {
			name, skip := jsonTag(f)
			if skip {
				continue
			}

			// an embedded struct's fields are the struct's own
			if len(f.Names) == 0 && name == "" {
				if inner := b.schemaOf(f.Type, seen); inner.Type == "object" {
					maps.Copy(s.Properties, inner.Properties)
				}
				continue
			}

			if name == "" && len(f.Names) > 0 {
				name = f.Names[0].Name
			}
			s.Properties[name] = b.schemaOf(f.Type, seen)
		}
		return s
	}

	return &schema{}
}

// jsonTag is the name a field's json tag gives it, and whether the tag leaves it out.
func jsonTag(f *ast.Field) (string, bool) {
	if f.Tag == nil {
		return "", false
	}

	tag, err := strconv.Unquote(f.Tag.Value)
	if err != nil {
		return "", false
	}

	for part := range strings.SplitSeq(tag, " ") {
		if v, ok := strings.CutPrefix(part, `json:"`); ok {
			name, _, _ := strings.Cut(strings.TrimSuffix(v, `"`), ",")
			return name, name == "-"
		}
	}

	return "", false
}

// fill types what the document leaves untyped from what the client reads and sends on the same route. Where the two type a value
// differently, the document's, read from the server, stands, and the difference is counted.
func (b *byhand) fill(doc *document) {
	ops := map[string]*operation{}
	for _, p := range sortedKeys(doc.Paths) {
		for _, verb := range sortedKeys(doc.Paths[p]) {
			ops[strings.ToUpper(verb)+" "+normalPath(p)] = doc.Paths[p][verb]
		}
	}

	for _, key := range sortedKeys(b.calls) {
		op, ok := ops[key]
		if !ok {
			b.unmatched = append(b.unmatched, key+" ("+b.calls[key][0].method+")")
			continue
		}

		for _, c := range b.calls[key] {
			before := b.filled
			if c.answer != nil {
				if s := successJSON(op); s != nil {
					b.merge(s, c.answer, doc.Components.Schemas, key+" answer")
				}
			}
			if c.body != nil && op.RequestBody != nil {
				if m, ok := op.RequestBody.Content["application/json"]; ok {
					b.merge(m.Schema, c.body, doc.Components.Schemas, key+" body")
				}
			}
			if b.filled > before {
				b.filledOps[key] = true
			}
		}
	}
}

// successJSON is the schema of an operation's first successful JSON answer, or nil.
func successJSON(op *operation) *schema {
	for _, code := range sortedKeys(op.Responses) {
		if strings.HasPrefix(code, "2") {
			if m, ok := op.Responses[code].Content["application/json"]; ok {
				return m.Schema
			}
		}
	}

	return nil
}

// untyped is whether a schema says nothing of what it holds.
func untyped(s *schema) bool {
	return s.Type == "" && s.Ref == "" && len(s.Properties) == 0 && s.Items == nil
}

// merge types the untyped places of to from the same places in from, following references into the records they name, once each.
func (b *byhand) merge(to, from *schema, components map[string]*schema, where string) {
	if to == nil || from == nil || untyped(from) {
		return
	}

	if to.Ref != "" {
		name := strings.TrimPrefix(to.Ref, "#/components/schemas/")
		if b.visitedRef[name+"\x00"+fingerprint(from)] {
			return
		}
		b.visitedRef[name+"\x00"+fingerprint(from)] = true
		b.merge(components[name], from, components, name)
		return
	}

	if untyped(to) {
		nullable := to.Nullable
		*to = *clone(from)
		to.Nullable = to.Nullable || nullable
		b.filled++
		return
	}

	switch {
	case to.Type == "object" && from.Type == "object":
		for _, n := range slices.Sorted(maps.Keys(to.Properties)) {
			b.merge(to.Properties[n], from.Properties[n], components, where+"."+n)
		}
		if to.AdditionalProperties {
			for _, n := range slices.Sorted(maps.Keys(from.Properties)) {
				if _, ok := to.Properties[n]; !ok {
					to.Properties[n] = clone(from.Properties[n])
					b.filled++
				}
			}
		}

	case to.Type == "array" && from.Type == "array":
		b.merge(to.Items, from.Items, components, where+"[]")

	case to.Type != from.Type && (to.Type != "number" || from.Type != "integer"):
		b.conflicts = append(b.conflicts, fmt.Sprintf("%s: the source says %s, the client %s", where, to.Type, from.Type))
	}
}

func clone(s *schema) *schema {
	if s == nil {
		return nil
	}

	out := *s
	out.Items = clone(s.Items)
	if s.Properties != nil {
		out.Properties = make(map[string]*schema, len(s.Properties))
		for n, p := range s.Properties {
			out.Properties[n] = clone(p)
		}
	}

	return &out
}

// fingerprint tells two schemas apart, so a record is filled once from each shape the client reads it as.
func fingerprint(s *schema) string {
	if s == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(s.Type + s.Ref)
	for _, n := range slices.Sorted(maps.Keys(s.Properties)) {
		sb.WriteString("," + n + ":" + fingerprint(s.Properties[n]))
	}
	if s.Items != nil {
		sb.WriteString("[" + fingerprint(s.Items) + "]")
	}

	return sb.String()
}

// write reports what the client filled, and with -v the requests it makes that no operation of the document has.
func (b *byhand) write(w io.Writer, dir string, verbose bool) {
	_, _ = fmt.Fprintf(w, "  filled from %s   %d values in %d operations; %d it types otherwise are left as the source has them; %d of its requests match no operation\n", dir, b.filled, len(b.filledOps), len(b.conflicts), len(b.unmatched))

	if verbose {
		for _, c := range b.conflicts {
			_, _ = fmt.Fprintln(w, "  differs:", c)
		}
		for _, u := range b.unmatched {
			_, _ = fmt.Fprintln(w, "  no operation for", u)
		}
	}
}
