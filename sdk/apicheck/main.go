// Command apicheck reports how much of the Audiobookshelf API the API document covers, and fails when a route is missing. The client in
// sdk/abs is generated from the document, an operation for each of its routes, so what the document covers the client does.
//
// The public API docs say they are unmaintained, and the server's own OpenAPI file covers under a quarter of the routes and stopped in 2024. So the
// route table in the server source is the reference: this reads it, reads the routes of the document in api-defs, and lists every route the
// document lacks, as when the server gains one after the version the document was read from.
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
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// routerURL is the server's route table at a git ref.
const routerURL = "https://raw.githubusercontent.com/advplyr/audiobookshelf/%s/server/routers/ApiRouter.js"

var (
	routeRE    = regexp.MustCompile(`router\.(get|post|patch|delete|put)\(\s*'([^']+)'`)
	paramRE    = regexp.MustCompile(`:\w+`)
	optionalRE = regexp.MustCompile(`:\w+\?`)
	// documentParamRE is a parameter as the document writes one in a path, {id}
	documentParamRE = regexp.MustCompile(`\{\w+\}`)
)

// route is one verb and path.
type route struct {
	verb string
	path string
}

// check is one run: where the route table is, where the API document is, and whether to list what is missing.
type check struct {
	ref      string // the server's git ref, named in the report
	router   string // the route table's address
	document string // the API document's file
	list     bool   // list every route not covered
}

func main() {
	ref := flag.String("ref", "master", "the Audiobookshelf git ref to check against")
	list := flag.Bool("list", false, "list every route not covered")
	flag.Parse()

	c := check{ref: *ref, router: fmt.Sprintf(routerURL, *ref), document: filepath.Join("api-defs", "audiobookshelf-openapi-"+abs.DocumentVersion+".json"), list: *list}
	missing, err := c.run(context.Background(), os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apicheck:", err)
		os.Exit(1)
	}

	// a gap is a regression: the document, and so the client, is meant to cover the whole API
	if missing > 0 {
		os.Exit(1)
	}
}

// run writes the report and returns how many routes the document lacks.
func (c check) run(ctx context.Context, w io.Writer) (int, error) {
	src, err := fetch(ctx, c.router)
	if err != nil {
		return 0, err
	}

	routes := serverRoutes(src)
	if len(routes) == 0 {
		return 0, fmt.Errorf("no routes found at %s; did the file move?", c.router)
	}

	ours, err := documentRoutes(c.document)
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
	_, _ = fmt.Fprintf(w, "  in the API document    %3d   %d%%\n", done, int(math.Round(100*float64(done)/float64(len(routes)))))
	_, _ = fmt.Fprintf(w, "  not in it              %3d\n", len(missing))

	if len(missing) > 0 && c.list {
		_, _ = fmt.Fprintln(w, "\nnot in the API document:")
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

// covered is whether the document has the route, with or without its trailing optional parameters.
func covered(ours map[route]bool, r route) bool {
	for _, p := range variants(r.path) {
		if ours[route{r.verb, p}] {
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

// documentRoutes are the routes of the API document, each verb and path with every parameter written :id.
func documentRoutes(file string) (map[route]bool, error) {
	b, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return nil, fmt.Errorf("reading the API document (run from the repository root): %w", err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("reading %s: %w", file, err)
	}
	if len(doc.Paths) == 0 {
		return nil, fmt.Errorf("%s has no routes", file)
	}

	ours := map[route]bool{}
	for path, verbs := range doc.Paths {
		for verb := range verbs {
			ours[route{strings.ToUpper(verb), trimSlash(documentParamRE.ReplaceAllString(path, ":id"))}] = true
		}
	}

	return ours, nil
}

// trimSlash drops a trailing slash, leaving the root as "/".
func trimSlash(p string) string {
	return cmp.Or(strings.TrimRight(p, "/"), "/")
}
