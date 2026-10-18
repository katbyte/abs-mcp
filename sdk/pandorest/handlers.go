package main

import (
	"slices"

	"github.com/tdewolff/parse/v2/js"
)

// answer is what one route was seen to send back, by status: JSON of a shape, text, nothing, or a file.
type answer struct {
	json   map[int]*jsType
	text   map[int]bool
	empty  map[int]bool
	status map[int]bool // res.sendStatus(n), which sends the status's own name as text
	file   bool
}

func newAnswer() *answer {
	return &answer{json: map[int]*jsType{}, text: map[int]bool{}, empty: map[int]bool{}, status: map[int]bool{}}
}

func (a *answer) addJSON(status int, t *jsType) { a.json[status] = merge(a.json[status], t) }
func (a *answer) addText(status int)            { a.text[status] = true }
func (a *answer) addEmpty(status int)           { a.empty[status] = true }
func (a *answer) addStatus(status int)          { a.status[status] = true }

// statuses are every status the route was seen to answer, in order.
func (a *answer) statuses() []int {
	var out []int
	for _, m := range []map[int]bool{a.text, a.empty, a.status} {
		for n := range m {
			out = append(out, n)
		}
	}
	for n := range a.json {
		out = append(out, n)
	}
	slices.Sort(out)

	return slices.Compact(out)
}

// reqInfo is what one route was seen to read from its request: query parameters, body fields and uploaded files, and the records its
// middleware attaches, such as req.libraryItem.
type reqInfo struct {
	query     []string
	numbers   map[string]string   // the query parameters the code turns into numbers, each "integer" or "number"
	values    map[string][]string // the values a query parameter or body field is checked against, by "query sort" or "body mediaItemType"
	body      []string
	files     []string
	bodyWhole bool
	bodyList  bool // the body is used as a list, req.body.map(...) or for (const x of req.body)
	// files are uploaded under any name, Object.values(req.files), which the route takes as a form like files with names
	filesWhole bool
	attached   map[string]*jsType
	reached    []reqUse
}

// reqUse is a function given the request, or its query, body or files on their own, under the name it gives it, read afterwards for
// every parameter it takes.
type reqUse struct {
	body []js.IStmt
	name string
	part string // "" for the request, or the part it was given: query, body or files
}

func newReqInfo() *reqInfo {
	return &reqInfo{attached: map[string]*jsType{}, numbers: map[string]string{}, values: map[string][]string{}}
}

func (r *reqInfo) addQuery(n string) { r.query = addName(r.query, n) }
func (r *reqInfo) addBody(n string)  { r.body = addName(r.body, n) }
func (r *reqInfo) addFile(n string)  { r.files = addName(r.files, n) }

func addName(list []string, n string) []string {
	if n == "" || slices.Contains(list, n) {
		return list
	}

	return append(list, n)
}

// analyze runs a route's middleware and handler with a request and a response that record what is read and written.
func (e *evaluator) analyze(r *route) (*answer, *reqInfo) {
	ans, req := newAnswer(), newReqInfo()
	reqT := &jsType{kind: requestKind, req: req}
	resT := &jsType{kind: responseKind, resp: ans}
	next := builtinFunc("next", func(*evaluator, *jsType, []*jsType) *jsType { return null })

	for _, h := range r.chain {
		t := e.eval(h, r.scope)
		if t.kind != funcKind {
			continue
		}
		e.invoke(t.fn, []*jsType{reqT, resT, next})
	}

	for _, use := range req.reached {
		readRequest(use, req)
	}

	// in name order, which stays put when the code is reordered
	slices.Sort(req.query)
	slices.Sort(req.body)
	slices.Sort(req.files)

	return ans, req
}

// readRequest finds every query parameter, body field and upload a function reads from the request, by reading its code rather than
// running it, so a name read only on a path the run did not follow is still found.
func readRequest(use reqUse, req *reqInfo) {
	// names given the request's query or body whole, const payload = req.body, whose fields are the request's
	aliases := map[string]string{}

	from := func(x js.IExpr, part string) bool {
		if v, ok := x.(*js.Var); ok && aliases[string(v.Name())] == part {
			return true
		}
		if use.part != "" {
			return use.part == part && identName(x) == use.name
		}

		dot, ok := x.(*js.DotExpr)
		return ok && identName(dot.X) == use.name && identName(dot.Y) == part
	}

	add := func(part, name string) {
		switch part {
		case "query":
			req.addQuery(name)
		case "body":
			req.addBody(name)
		case "files":
			req.addFile(name)
		}
	}

	// names given one value of the request, const { mediaItemType } = req.body or let orderKey = req.query.sort || 'updatedAt', by "query sort",
	// and names given a list of text written out, for the values a check of the one against the other allows
	reads := map[string]string{}
	lists := map[string][]string{}

	// bindPart adds the names a pattern takes out of one part of the request, const { limit, page } = req.query
	bindPart := func(obj *js.BindingObject, part string) {
		for _, item := range obj.List {
			name := propertyName(item.Key)
			v, named := item.Value.Binding.(*js.Var)
			if name == "" && named {
				name = string(v.Name())
			}
			add(part, name)
			if named && name != "" {
				reads[string(v.Name())] = part + " " + name
			}
		}
	}

	// readOf is the "part name" x reads, through a default given with || or ??, or "" for none
	readOf := func(x js.IExpr) string {
		x = withoutDefault(x)
		if v, ok := x.(*js.Var); ok {
			return reads[string(v.Name())]
		}
		for _, part := range []string{"query", "body"} {
			switch x := x.(type) {
			case *js.DotExpr:
				if from(x.X, part) {
					return part + " " + identName(x.Y)
				}
			case *js.IndexExpr:
				if k, ok := stringValue(x.Y); ok && from(x.X, part) {
					return part + " " + k
				}
			}
		}

		return ""
	}

	v := visitor(func(n js.INode) {
		switch n := n.(type) {
		case *js.DotExpr:
			for _, part := range []string{"query", "body", "files"} {
				if !from(n.X, part) {
					continue
				}
				if part == "body" && slices.Contains(listMembers, identName(n.Y)) {
					req.bodyList = true
					continue
				}
				add(part, identName(n.Y))
			}

		case *js.ForOfStmt:
			if from(n.Value, "body") {
				req.bodyList = true
			}

		case *js.IndexExpr:
			for _, part := range []string{"query", "body", "files"} {
				if !from(n.X, part) {
					continue
				}
				// req.body[key]: a field the code works out, so any may be read
				if k, ok := stringValue(n.Y); ok {
					add(part, k)
				} else if part == "body" {
					req.bodyWhole = true
				}
			}

		case *js.ForInStmt:
			// for (const key in req.body): every field is read
			if from(n.Value, "body") {
				req.bodyWhole = true
			}

		case *js.ObjectExpr:
			// { ...req.body, libraryItemId }: every field is handed on
			for _, p := range n.List {
				if p.Spread && from(p.Value, "body") {
					req.bodyWhole = true
				}
			}

		case *js.BindingElement:
			if v, ok := n.Binding.(*js.Var); ok {
				if n.Default == nil {
					return
				}
				for _, part := range []string{"query", "body"} {
					if from(withoutDefault(n.Default), part) {
						aliases[string(v.Name())] = part
					}
				}
				if r := readOf(n.Default); r != "" {
					reads[string(v.Name())] = r
				}
				if list, ok := textList(n.Default); ok {
					lists[string(v.Name())] = list
				}
				return
			}

			obj, ok := n.Binding.(*js.BindingObject)
			if !ok {
				return
			}
			for _, part := range []string{"query", "body", "files"} {
				if from(withoutDefault(n.Default), part) {
					bindPart(obj, part)
				}
			}

			// const { query: { width, raw } } = req: parts taken out of the request whole, each then taken apart
			if use.part == "" && identName(withoutDefault(n.Default)) == use.name {
				for _, item := range obj.List {
					inner, ok := item.Value.Binding.(*js.BindingObject)
					if part := propertyName(item.Key); ok && slices.Contains([]string{"query", "body", "files"}, part) {
						bindPart(inner, part)
					}
				}
			}

		case *js.CallExpr:
			for _, a := range n.Args.List {
				if from(a.Value, "body") {
					req.bodyWhole = true
				}
				if from(a.Value, "files") {
					req.filesWhole = true
				}
			}

			// getQueryParamAsString(req.query, 'title', ''): a helper handed the query or the body and a name reads that name from it
			for i, a := range n.Args.List[:max(len(n.Args.List)-1, 0)] {
				for _, part := range []string{"query", "body"} {
					if from(a.Value, part) {
						if k, ok := stringValue(n.Args.List[i+1].Value); ok {
							add(part, k)
						}
					}
				}
			}

			// ['book', 'podcastEpisode'].includes(mediaItemType): a value of the request is checked against text written out, which are the values
			// the server acts on
			if dot, ok := n.X.(*js.DotExpr); ok && identName(dot.Y) == "includes" && len(n.Args.List) == 1 {
				list, ok := textList(dot.X)
				if v, named := dot.X.(*js.Var); named && !ok {
					list, ok = lists[string(v.Name())]
				}
				if r := readOf(n.Args.List[0].Value); ok && r != "" {
					req.values[r] = list
				}
			}

			// toNumber(req.query.page, 0): a query value made a number
			if t := numberCall(identName(n.X)); t != "" && len(n.Args.List) > 0 {
				if name := queryName(n.Args.List[0].Value, from); name != "" {
					req.numbers[name] = t
				}
			}

		case *js.BinaryExpr:
			// req.query.limit || 0: a query value whose default is a number
			if n.Op != js.OrToken && n.Op != js.NullishToken {
				return
			}
			if name := queryName(n.X, from); name != "" {
				if lit, ok := n.Y.(*js.LiteralExpr); ok && lit.TokenType == js.IntegerToken {
					req.numbers[name] = "integer"
				} else if ok && lit.TokenType == js.DecimalToken {
					req.numbers[name] = "number"
				}
			}
		}
	})

	for _, st := range use.body {
		js.Walk(v, st)
	}
}

// textList is the values of a list of text written out, ['book', 'podcast'], and false for any other value.
func textList(x js.IExpr) ([]string, bool) {
	arr, ok := x.(*js.ArrayExpr)
	if !ok || len(arr.List) == 0 {
		return nil, false
	}

	out := make([]string, 0, len(arr.List))
	for _, e := range arr.List {
		s, ok := stringValue(e.Value)
		if !ok || e.Spread {
			return nil, false
		}
		out = append(out, s)
	}

	return out, true
}

// withoutDefault is x without the default it is given, req.body for req.body || {} or req.query.sort ?? 'title'.
func withoutDefault(x js.IExpr) js.IExpr {
	if b, ok := x.(*js.BinaryExpr); ok && (b.Op == js.OrToken || b.Op == js.NullishToken) {
		return b.X
	}

	return x
}

// listMembers are what a list has of its own, which a body used as a list is read by and which are not fields of it.
var listMembers = []string{"length", "map", "flatMap", "forEach", "filter", "find", "findIndex", "some", "every", "includes", "indexOf", "reduce", "slice", "join", "at"}

// queryName is the query parameter x reads, req.query.page or req.query['page'], or "" when it reads none.
func queryName(x js.IExpr, from func(js.IExpr, string) bool) string {
	switch x := x.(type) {
	case *js.DotExpr:
		if from(x.X, "query") {
			return identName(x.Y)
		}
	case *js.IndexExpr:
		if from(x.X, "query") {
			k, _ := stringValue(x.Y)
			return k
		}
	}

	return ""
}

// numberCall is the type of number a call of the function named makes of text: every number Audiobookshelf reads from a query is whole,
// a page, a limit, a folder depth or a start time in seconds, and parseFloat, which it never uses, is the one way to ask for a fraction.
// toNumber is its own helper.
func numberCall(name string) string {
	switch name {
	case "Number", "parseInt", "isNaN", "toNumber":
		return "integer"
	case "parseFloat":
		return "number"
	}

	return ""
}

// visitor walks every node, into functions too, calling fn on each.
type visitor func(js.INode)

func (v visitor) Enter(n js.INode) js.IVisitor {
	v(n)
	return v
}

func (visitor) Exit(js.INode) {}
