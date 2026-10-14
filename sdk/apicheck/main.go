// Command apicheck reports how much of the Audiobookshelf API sdk/abs-byhand covers, and fails when a route is missing.
//
// The public API docs say they are unmaintained, and the server's own OpenAPI file covers under a quarter of the routes and stopped in 2024. So the
// route table in the server source is the reference: this reads it, reads the paths sdk/abs-byhand builds, and lists every route nothing builds.
//
//	go run ./sdk/apicheck              # against the server's master branch
//	go run ./sdk/apicheck -list        # also list every route not covered
//	go run ./sdk/apicheck -ref v2.37.1 # against a release
//
// Run it from the repository root, as make apicheck does.
package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// routerURL is the server's route table at a git ref.
const routerURL = "https://raw.githubusercontent.com/advplyr/audiobookshelf/%s/server/routers/ApiRouter.js"

// anyVerb marks a path the client builds where the verb cannot be seen, such as one put in a variable first or handed to a helper. It can
// only make a route look covered that is not, never hide one that is, and the package is ours.
const anyVerb = "*"

var (
	routeRE    = regexp.MustCompile(`router\.(get|post|patch|delete|put)\(\s*'([^']+)'`)
	slashesRE  = regexp.MustCompile(`/+`)
	paramRE    = regexp.MustCompile(`:\w+`)
	optionalRE = regexp.MustCompile(`:\w+\?`)
)

// requestVerbs are the client's request helpers that name their verb, and the verb each sends.
var requestVerbs = map[string]string{
	"get":   http.MethodGet,
	"post":  http.MethodPost,
	"patch": http.MethodPatch,
	"del":   http.MethodDelete,
}

// route is one verb and path.
type route struct {
	verb string
	path string
}

// check is one run: where the route table is, where the client's source is, and whether to list what is missing.
type check struct {
	ref    string // the server's git ref, named in the report
	router string // the route table's address
	dir    string // the client's source
	list   bool   // list every route not covered
}

func main() {
	ref := flag.String("ref", "master", "the Audiobookshelf git ref to check against")
	list := flag.Bool("list", false, "list every route not covered")
	flag.Parse()

	c := check{ref: *ref, router: fmt.Sprintf(routerURL, *ref), dir: "sdk/abs-byhand", list: *list}
	missing, err := c.run(context.Background(), os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apicheck:", err)
		os.Exit(1)
	}

	// a gap is a regression: the client is meant to cover the whole API
	if missing > 0 {
		os.Exit(1)
	}
}

// run writes the report and returns how many routes nothing in the client builds.
func (c check) run(ctx context.Context, w io.Writer) (int, error) {
	src, err := fetch(ctx, c.router)
	if err != nil {
		return 0, err
	}

	routes := serverRoutes(src)
	if len(routes) == 0 {
		return 0, fmt.Errorf("no routes found at %s; did the file move?", c.router)
	}

	ours, err := clientPaths(c.dir)
	if err != nil {
		return 0, err
	}

	var missing []route
	for _, r := range routes {
		if !covered(ours, r) {
			missing = append(missing, r)
		}
	}

	done := len(routes) - len(missing)
	_, _ = fmt.Fprintf(w, "Audiobookshelf %s: %d routes\n", c.ref, len(routes))
	_, _ = fmt.Fprintf(w, "  covered by sdk/abs-byhand   %3d   %d%%\n", done, int(math.Round(100*float64(done)/float64(len(routes)))))
	_, _ = fmt.Fprintf(w, "  not implemented      %3d\n", len(missing))

	if len(missing) > 0 && c.list {
		_, _ = fmt.Fprintln(w, "\nnot implemented:")
		for _, r := range missing {
			_, _ = fmt.Fprintf(w, "  %-6s %s\n", r.verb, r.path)
		}
	}

	return len(missing), nil
}

// fetch reads the route table.
func fetch(ctx context.Context, addr string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, http.NoBody)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %s", addr, resp.Status)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", addr, err)
	}

	return string(b), nil
}

// serverRoutes are the routes in the server's route table, each once, in order of path and then verb.
func serverRoutes(src string) []route {
	var out []route
	for _, m := range routeRE.FindAllStringSubmatch(src, -1) {
		r := route{verb: strings.ToUpper(m[1]), path: "/api" + m[2]}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}

	slices.SortFunc(out, func(a, b route) int {
		return cmp.Or(strings.Compare(a.path, b.path), strings.Compare(a.verb, b.verb))
	})

	return out
}

// covered is whether the client builds the route, with or without its trailing optional parameters.
func covered(ours map[route]bool, r route) bool {
	for _, p := range variants(r.path) {
		if ours[route{r.verb, p}] || ours[route{anyVerb, p}] {
			return true
		}
	}

	return false
}

// variants are a route's path with every parameter written :id, and again without each trailing optional parameter in turn.
func variants(path string) []string {
	p := optionalRE.ReplaceAllString(path, "\x00")
	p = paramRE.ReplaceAllString(p, ":id")

	out := []string{trimSlash(strings.ReplaceAll(p, "\x00", ":id"))}
	for strings.HasSuffix(p, "/\x00") {
		p = strings.TrimSuffix(p, "/\x00")
		out = append(out, trimSlash(strings.ReplaceAll(p, "\x00", ":id")))
	}

	return out
}

// clientPaths are the paths the client's source builds, each with the verb it is sent with, or anyVerb where that cannot be seen.
func clientPaths(dir string) (map[route]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading the client's source (run from the repository root): %w", err)
	}

	fset := token.NewFileSet()
	ours := map[route]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}

		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if r, ok := requestRoute(call); ok {
					ours[r] = true
				}
				return true
			}

			if e, ok := n.(ast.Expr); ok {
				if p, ok := apiPath(e); ok {
					ours[route{anyVerb, p}] = true
					return false
				}
			}

			return true
		})
	}

	return ours, nil
}

// requestRoute is the verb and path of a call to one of the client's request helpers: c.get(ctx, path, ...) and the like, or
// c.do(ctx, http.MethodPut, path, ...).
func requestRoute(call *ast.CallExpr) (route, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !isIdent(sel.X, "c") {
		return route{}, false
	}

	if verb, ok := requestVerbs[sel.Sel.Name]; ok && len(call.Args) > 1 {
		return route{verb, buildPath(operands(call.Args[1]))}, true
	}

	if (sel.Sel.Name == "do" || sel.Sel.Name == "doRaw") && len(call.Args) > 2 {
		if m, ok := call.Args[1].(*ast.SelectorExpr); ok && isIdent(m.X, "http") && strings.HasPrefix(m.Sel.Name, "Method") {
			return route{strings.ToUpper(strings.TrimPrefix(m.Sel.Name, "Method")), buildPath(operands(call.Args[2]))}, true
		}
	}

	return route{}, false
}

// apiPath is the path an expression builds from its first "/api/" text on, when it has one.
func apiPath(e ast.Expr) (string, bool) {
	ops := operands(e)
	for i, o := range ops {
		if s, ok := text(o); ok && strings.HasPrefix(s, "/api/") {
			return buildPath(ops[i:]), true
		}
	}

	return "", false
}

// operands are the pieces of a string joined with +, in order.
func operands(e ast.Expr) []ast.Expr {
	if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.ADD {
		return append(operands(b.X), operands(b.Y)...)
	}

	return []ast.Expr{e}
}

// buildPath joins the pieces of a path, with anything that is not written out, such as url.PathEscape(id), as :id.
func buildPath(ops []ast.Expr) string {
	var b strings.Builder
	for _, o := range ops {
		if s, ok := text(o); ok {
			b.WriteString(s)
		} else {
			b.WriteString(":id")
		}
	}

	return paramRE.ReplaceAllString(trimSlash(slashesRE.ReplaceAllString(b.String(), "/")), ":id")
}

// text is the value of a string written out in the source.
func text(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}

	s, err := strconv.Unquote(lit.Value)

	return s, err == nil
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// trimSlash drops a trailing slash, leaving the root as "/".
func trimSlash(p string) string {
	return cmp.Or(strings.TrimRight(p, "/"), "/")
}
