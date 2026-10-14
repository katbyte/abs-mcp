package main

import (
	"slices"
	"strings"

	"github.com/tdewolff/parse/v2/js"
)

// mounts are the routers the document covers and where the server mounts each. The login and single sign-on routes in Auth.js and the
// audio streams in HlsRouter.js are left out: an API key needs neither.
var mounts = []struct {
	file   string
	prefix string
}{
	{"server/routers/ApiRouter.js", "/api"},
	{"server/routers/PublicRouter.js", "/public"},
	{"server/Server.js", ""},
}

var verbs = []string{"get", "post", "patch", "put", "delete"}

// route is one verb and path, the middleware and handler it runs in order, and the scope they are written in.
type route struct {
	verb    string
	path    string // as Express writes it, /api/items/:id/play/:episodeId?
	file    string
	chain   []js.IExpr
	scope   *scope
	handler string // the last of the chain, as written: LibraryItemController.startPlaybackSession
}

// readRoutes finds every router.get('/path', ...) and its kin in the methods of each mounted file's classes.
func readRoutes(e *evaluator) []*route {
	var out []*route
	for _, m := range mounts {
		f := e.prog.files[m.file]
		if f == nil {
			continue
		}

		for _, cn := range sortedKeys(f.classes) {
			c := f.classes[cn]
			for _, mn := range sortedKeys(c.methods) {
				meth := c.methods[mn]
				s := fileScope(f)
				s.this = e.constructed(c)
				declare(e, meth.Body.List, s)

				walkStmts(meth.Body.List, func(st js.IStmt) {
					for _, x := range stmtExprs(st) {
						walkExpr(x, func(x js.IExpr) {
							if r := routeOf(x, f.path, m.prefix, s); r != nil {
								out = append(out, r)
							}
						})
					}
				})
			}
		}
	}

	return out
}

// routeOf is the route a call declares, or nil: this.router.get('/path', ...handlers) or router.get(...).
func routeOf(x js.IExpr, file, prefix string, s *scope) *route {
	call, ok := x.(*js.CallExpr)
	if !ok || len(call.Args.List) < 2 {
		return nil
	}

	dot, ok := call.X.(*js.DotExpr)
	if !ok || !slices.Contains(verbs, identName(dot.Y)) {
		return nil
	}

	on := dot.X
	if inner, isDot := on.(*js.DotExpr); isDot && isThis(inner.X) {
		on = inner.Y
	}
	if identName(on) != "router" {
		return nil
	}

	p, ok := stringValue(call.Args.List[0].Value)
	if !ok || strings.Contains(p, "*") {
		return nil
	}

	r := &route{verb: identName(dot.Y), path: prefix + p, file: file, scope: s}
	for _, a := range call.Args.List[1:] {
		r.chain = append(r.chain, a.Value)
	}
	r.handler = handlerName(r.chain[len(r.chain)-1])

	return r
}

// handlerName is how a handler is written, without .bind(this): LibraryController.findAll, or "inline" for a function written in place.
func handlerName(x js.IExpr) string {
	if call, ok := x.(*js.CallExpr); ok {
		if dot, ok := call.X.(*js.DotExpr); ok && identName(dot.Y) == "bind" {
			x = dot.X
		}
	}

	if dot, ok := x.(*js.DotExpr); ok {
		return identName(dot.X) + "." + identName(dot.Y)
	}

	return "inline"
}

// variants are a path's forms with each trailing optional parameter left off in turn, as OpenAPI has no optional path parameters:
// /items/:id/play/:episodeId? is /items/:id/play/:episodeId and /items/:id/play.
func variants(p string) []string {
	parts := strings.Split(p, "/")
	out := []string{strings.ReplaceAll(p, "?", "")}

	for len(parts) > 0 && strings.HasPrefix(parts[len(parts)-1], ":") && strings.HasSuffix(parts[len(parts)-1], "?") {
		parts = parts[:len(parts)-1]
		out = append(out, strings.ReplaceAll(strings.Join(parts, "/"), "?", ""))
	}

	return out
}

// openAPIPath is an Express path as OpenAPI writes it, with its parameters: /items/:id becomes /items/{id}.
func openAPIPath(p string) (written string, params []string) {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") {
			name := strings.TrimSuffix(part[1:], "?")
			params = append(params, name)
			parts[i] = "{" + name + "}"
		}
	}

	return strings.Join(parts, "/"), params
}
