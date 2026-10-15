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
	numbers   map[string]string // the query parameters the code turns into numbers, each "integer" or "number"
	body      []string
	files     []string
	bodyWhole bool
	bodyList  bool // the body is used as a list, req.body.map(...) or for (const x of req.body)
	attached  map[string]*jsType
	reached   []reqUse
}

// reqUse is a function given the request, or its query, body or files on their own, under the name it gives it, read afterwards for
// every parameter it takes.
type reqUse struct {
	body []js.IStmt
	name string
	part string // "" for the request, or the part it was given: query, body or files
}

func newReqInfo() *reqInfo {
	return &reqInfo{attached: map[string]*jsType{}, numbers: map[string]string{}}
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

		case *js.BindingElement:
			if v, ok := n.Binding.(*js.Var); ok {
				for _, part := range []string{"query", "body"} {
					if from(n.Default, part) {
						aliases[string(v.Name())] = part
					}
				}
				return
			}

			obj, ok := n.Binding.(*js.BindingObject)
			if !ok {
				return
			}
			for _, part := range []string{"query", "body", "files"} {
				if from(n.Default, part) {
					for _, item := range obj.List {
						name := propertyName(item.Key)
						if name == "" {
							if v, ok := item.Value.Binding.(*js.Var); ok {
								name = string(v.Name())
							}
						}
						add(part, name)
					}
				}
			}

		case *js.CallExpr:
			for _, a := range n.Args.List {
				if from(a.Value, "body") {
					req.bodyWhole = true
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
