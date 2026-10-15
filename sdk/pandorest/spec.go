package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/abs-mcp/sdk/pandorest/answers"
)

// document is the OpenAPI document the scan writes, in the parts pandorest reads.
type document struct {
	OpenAPI    string                           `json:"openapi"`
	Info       info                             `json:"info"`
	Security   []map[string][]string            `json:"security"`
	Paths      map[string]map[string]*operation `json:"paths"`
	Components components                       `json:"components"`
}

type info struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

type operation struct {
	OperationID string              `json:"operationId"`
	Tags        []string            `json:"tags"`
	Parameters  []parameter         `json:"parameters,omitempty"`
	RequestBody *body               `json:"requestBody,omitempty"`
	Responses   map[string]response `json:"responses"`
	Handler     string              `json:"x-abs-handler"`
	Source      string              `json:"x-abs-source"`
}

type parameter struct {
	Name     string  `json:"name"`
	In       string  `json:"in"`
	Required bool    `json:"required,omitempty"`
	Schema   *schema `json:"schema"`
}

type body struct {
	Content map[string]media `json:"content"`
}

type response struct {
	Description string           `json:"description"`
	Content     map[string]media `json:"content,omitempty"`
}

type media struct {
	Schema *schema `json:"schema"`
}

type schema struct {
	Ref                  string             `json:"$ref,omitempty"`
	Type                 string             `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Nullable             bool               `json:"nullable,omitempty"`
	Items                *schema            `json:"items,omitempty"`
	Properties           map[string]*schema `json:"properties,omitempty"`
	AdditionalProperties bool               `json:"additionalProperties,omitempty"`

	// whole is set on a number typed from answers that were all whole, which the hand-written client may settle as a whole number
	whole bool
	// sampled is set on a record typed from answers, which show only the fields that happened to be sent, so the hand-written client may add more
	sampled bool
}

type components struct {
	Schemas         map[string]*schema        `json:"schemas"`
	SecuritySchemes map[string]securityScheme `json:"securitySchemes"`
}

type securityScheme struct {
	Type   string `json:"type"`
	Scheme string `json:"scheme"`
}

// spec reads an Audiobookshelf checkout and writes its API document into dir, then reports how much of it the scan could type.
func spec(args []string, dir string, w io.Writer) error {
	fs := flag.NewFlagSet("spec", flag.ContinueOnError)
	verbose := fs.Bool("v", false, "list every operation and how much of its answer is typed")
	trace := fs.String("trace", "", "print what the scan works out for the handlers named, such as LibraryController.stats")
	client := fs.String("client", "sdk/abs-byhand", "the hand-written client, whose types fill what the source leaves untyped; empty for none")
	answerFlag := fs.String("answers", "", "a folder of answers saved from a real server by the live suites, which fill and correct the document; empty for <dir>/audiobookshelf-<version>/answers")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: go run ./sdk/pandorest spec [-v] [-answers dir] [-client dir] <audiobookshelf checkout>")
	}

	root, err := checkout(fs.Arg(0))
	if err != nil {
		return err
	}

	version, err := readVersion(root)
	if err != nil {
		return err
	}

	prog, err := load(root, "server")
	if err != nil {
		return err
	}

	doc, b, routes := build(prog, version, *trace, w)

	// what a real server answered fills what the source leaves untyped and corrects what it gets wrong, before anything else has a say
	answerDir := cmp.Or(*answerFlag, answers.Dir(dir, version))
	saved, err := answers.Read(answerDir)
	if err != nil {
		return fmt.Errorf("reading the answers saved from a server: %w", err)
	}
	var heard *answerFill
	if len(saved) > 0 {
		heard = newAnswerFill()
		heard.fill(doc, saved)
	}

	// what is still untyped, the hand-written client may type: it is proved against a running server
	var hand *byhand
	if *client != "" {
		if hand, err = readClient(os.DirFS(*client), "."); err != nil {
			return fmt.Errorf("reading the hand-written client: %w", err)
		}
		hand.fill(doc)
	}

	rep := measure(doc, b, routes)

	js, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}

	out := filepath.Join(dir, "audiobookshelf-openapi-"+version+".json")
	if err := os.WriteFile(out, append(js, '\n'), 0o600); err != nil {
		return err
	}

	rep.write(w, version, *verbose)
	if heard != nil {
		heard.write(w, answerDir, *verbose)
	}
	if hand != nil {
		hand.write(w, *client, *verbose)
	}
	_, _ = fmt.Fprintln(w, "wrote", out)

	return nil
}

// packageVersion is the version a package.json gives.
func packageVersion(b []byte) (string, error) {
	var p struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return "", err
	}
	if p.Version == "" {
		return "", errors.New("no version")
	}

	return p.Version, nil
}

// builder turns what the scan found into the document, naming each record once under components.
type builder struct {
	e        *evaluator
	schemas  map[string]*schema
	building map[string]bool
}

// build scans every route and makes the document from what it finds.
func build(prog *program, version, trace string, w io.Writer) (doc *document, b *builder, routes int) {
	e := newEvaluator(prog, readModels(prog))
	b = &builder{e: e, schemas: map[string]*schema{}, building: map[string]bool{}}

	doc = &document{
		OpenAPI: "3.0.3",
		Info: info{
			Title:       "Audiobookshelf API",
			Version:     version,
			Description: "Read from the Audiobookshelf " + version + " server source by abs-mcp (go run ./sdk/pandorest spec). Audiobookshelf publishes no complete document of its own.",
		},
		Security:   []map[string][]string{{"bearer": {}}},
		Paths:      map[string]map[string]*operation{},
		Components: components{Schemas: b.schemas, SecuritySchemes: map[string]securityScheme{"bearer": {Type: "http", Scheme: "bearer"}}},
	}

	ids := map[string]int{}
	for _, r := range readRoutes(e) {
		ans, req := e.analyze(r)
		if trace != "" && slices.Contains(strings.Split(trace, ","), r.handler) {
			for _, n := range ans.statuses() {
				if t := ans.json[n]; t != nil {
					_, _ = fmt.Fprintf(w, "%s %s %d:\n%s", strings.ToUpper(r.verb), r.path, n, dump(t, "  ", 0))
				}
			}
		}
		routes++

		for _, v := range variants(r.path) {
			p, params := openAPIPath(v)
			op := b.operation(r, params, ans, req)

			op.OperationID = operationID(r, v, ids)
			if doc.Paths[p] == nil {
				doc.Paths[p] = map[string]*operation{}
			}
			doc.Paths[p][r.verb] = op
		}
	}

	return doc, b, routes
}

// measure is how much of a document is typed, once the hand-written client has filled what it can.
func measure(doc *document, b *builder, routes int) *report {
	rep := &report{routes: routes}
	for _, p := range sortedKeys(doc.Paths) {
		for _, verb := range sortedKeys(doc.Paths[p]) {
			rep.add(verb, p, doc.Paths[p][verb], b)
		}
	}
	rep.schemas(b)

	return rep
}

// operation is one route as OpenAPI writes it.
func (b *builder) operation(r *route, params []string, ans *answer, req *reqInfo) *operation {
	op := &operation{Tags: []string{tag(r)}, Responses: map[string]response{}, Handler: r.handler, Source: r.file}

	for _, n := range params {
		op.Parameters = append(op.Parameters, parameter{Name: n, In: "path", Required: true, Schema: &schema{Type: "string"}})
	}
	for _, n := range req.query {
		t := cmp.Or(req.numbers[n], "string")
		op.Parameters = append(op.Parameters, parameter{Name: n, In: "query", Schema: &schema{Type: t}})
	}

	if r.verb != "get" && (len(req.body) > 0 || req.bodyWhole || req.bodyList || len(req.files) > 0) {
		props := map[string]*schema{}
		for _, n := range req.body {
			props[n] = &schema{}
		}
		switch {
		case len(req.files) > 0:
			for _, n := range req.files {
				props[n] = &schema{Type: "string", Format: "binary"}
			}
			op.RequestBody = &body{Content: map[string]media{"multipart/form-data": {Schema: &schema{Type: "object", Properties: props}}}}
		case req.bodyList:
			op.RequestBody = &body{Content: map[string]media{"application/json": {Schema: &schema{Type: "array", Items: &schema{}}}}}
		default:
			op.RequestBody = &body{Content: map[string]media{"application/json": {Schema: &schema{Type: "object", Properties: props, AdditionalProperties: req.bodyWhole}}}}
		}
	}

	ok := false
	for _, n := range ans.statuses() {
		code := strconv.Itoa(n)
		switch t := ans.json[n]; {
		case t != nil:
			op.Responses[code] = response{Description: "JSON", Content: map[string]media{"application/json": {Schema: b.schemaOf(t)}}}
		case n < 300 && (ans.text[n] || ans.status[n]):
			op.Responses[code] = response{Description: "text", Content: map[string]media{"text/plain": {Schema: &schema{Type: "string"}}}}
		default:
			op.Responses[code] = response{Description: statusText(n)}
		}
		if n < 300 {
			ok = true
		}
	}

	if ans.file {
		op.Responses["200"] = response{Description: "a file", Content: map[string]media{"application/octet-stream": {Schema: &schema{Type: "string", Format: "binary"}}}}
		ok = true
	}

	if !ok {
		// nothing the scan followed answers; say JSON of no known shape, so the document imports and the gap shows
		op.Responses["200"] = response{Description: "not found by the scan", Content: map[string]media{"application/json": {Schema: &schema{}}}}
	}

	return op
}

// schemaOf is a type as OpenAPI writes it. A record with a name is written once under components and referred to.
func (b *builder) schemaOf(t *jsType) *schema {
	if t == nil {
		return &schema{}
	}

	var s *schema
	switch t.kind {
	case stringKind:
		s = &schema{Type: "string"}
	case dateKind:
		s = &schema{Type: "string", Format: "date-time"}
	case integerKind:
		s = &schema{Type: "integer"}
	case numberKind:
		s = &schema{Type: "number"}
	case booleanKind:
		s = &schema{Type: "boolean"}
	case arrayKind:
		s = &schema{Type: "array", Items: b.schemaOf(t.elem)}
	case objectKind:
		s = b.objectSchema(t)
	case instanceKind:
		s = b.schemaOf(b.e.serialize(t))
	default:
		s = &schema{}
	}

	if t.nullable && s.Ref == "" {
		s.Nullable = true
	}

	return s
}

func (b *builder) objectSchema(t *jsType) *schema {
	if t.name == "" || strings.Contains(t.name, ".") || t.name == "models" {
		return b.inline(t)
	}

	ref := &schema{Ref: "#/components/schemas/" + t.name}
	if b.building[t.name] {
		return ref
	}

	b.building[t.name] = true
	defer delete(b.building, t.name)

	full := t
	for _, p := range b.e.extra[t.name] {
		full = full.withField(p.name, p.typ)
	}

	s := b.inline(full)
	if old, ok := b.schemas[t.name]; ok {
		for n, p := range s.Properties {
			if _, seen := old.Properties[n]; !seen {
				old.Properties[n] = p
			}
		}
		return ref
	}
	b.schemas[t.name] = s

	return ref
}

func (b *builder) inline(t *jsType) *schema {
	s := &schema{Type: "object", Properties: map[string]*schema{}, AdditionalProperties: t.open}
	for _, p := range t.props {
		s.Properties[p.name] = b.schemaOf(p.typ)
	}

	return s
}

// serialize is an instance as JSON.stringify writes it: through its class's toJSON, as a record's columns, or as a plain object's fields.
func (e *evaluator) serialize(t *jsType) *jsType {
	if m := e.method(t.class, "toJSON"); m != nil {
		return e.invoke(&jsFunc{params: m.Params, body: m.Body.List, scope: fileScope(t.class.file), this: t, name: t.class.name + ".toJSON"}, nil)
	}

	if m := e.models.byClass[t.class]; m != nil {
		return m.plain()
	}

	out := &jsType{kind: objectKind, name: t.class.name}
	for _, n := range sortedKeys(t.class.fields) {
		out = out.withField(n, e.member(t, n, false))
	}

	return out
}

// jsonMethod is the schema name a record-writing method's answer is given: toOldJSONExpanded on LibraryItem is LibraryItemExpanded, and
// oldMetadataToJSON on Book is BookMetadata.
var jsonMethod = regexp.MustCompile(`^(?:toOldJSON|toJSON|getOldJSON)(.*)$|^(?:old)?([A-Za-z]+)ToJSON(.*)$`)

func jsonName(class, method string) string {
	m := jsonMethod.FindStringSubmatch(method)
	if m == nil {
		return ""
	}

	if m[2] != "" {
		return class + upperFirst(m[2]) + m[3]
	}

	return class + m[1]
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}

	return strings.ToUpper(s[:1]) + s[1:]
}

// tag is the group an operation goes in: its controller without the word, LibraryItem for LibraryItemController.
func tag(r *route) string {
	if r.handler == "inline" {
		return "Server"
	}

	c, _, _ := strings.Cut(r.handler, ".")

	return strings.TrimSuffix(c, "Controller")
}

// operationID names an operation after its handler, LibraryItemFindOne, telling apart one handler on several paths by the parameters the
// later paths add.
func operationID(r *route, p string, seen map[string]int) string {
	var id string
	if r.handler == "inline" {
		var b strings.Builder
		b.WriteString("Server" + upperFirst(r.verb))
		for part := range strings.SplitSeq(p, "/") {
			if part != "" && !strings.HasPrefix(part, ":") {
				b.WriteString(upperFirst(part))
			}
		}
		id = b.String()
	} else {
		_, m, _ := strings.Cut(r.handler, ".")
		id = tag(r) + upperFirst(m)
	}

	seen[id]++
	if seen[id] == 1 {
		return id
	}

	return id + strconv.Itoa(seen[id])
}

func statusText(n int) string {
	switch n {
	case 400:
		return "the request was refused"
	case 401:
		return "not signed in"
	case 403:
		return "not allowed"
	case 404:
		return "not found"
	case 409:
		return "already there"
	case 500:
		return "the server failed"
	}

	return "status " + strconv.Itoa(n)
}

// report is how much of the document the scan could type.
type report struct {
	routes, ops                          int
	jsonOps, typed, partly, untyped      int
	textOps, fileOps, emptyOps, notFound int
	query, bodyFields                    int
	records, fields, untypedFields       int
	lines                                []string
}

func (r *report) add(verb, p string, op *operation, b *builder) {
	r.ops++
	r.query += len(slices.DeleteFunc(slices.Clone(op.Parameters), func(p parameter) bool { return p.In != "query" }))
	if op.RequestBody != nil {
		for _, m := range op.RequestBody.Content {
			r.bodyFields += len(m.Schema.Properties)
		}
	}

	state := ""
	for _, code := range sortedKeys(op.Responses) {
		if !strings.HasPrefix(code, "2") {
			continue
		}

		resp := op.Responses[code]
		switch {
		case resp.Description == "not found by the scan":
			r.notFound++
			state = "answer not found"
		case resp.Content["application/json"].Schema != nil:
			r.jsonOps++
			all, gaps := b.count(resp.Content["application/json"].Schema, map[string]bool{})
			switch {
			case gaps == 0:
				r.typed++
				state = "JSON, typed"
			case gaps < all:
				r.partly++
				state = fmt.Sprintf("JSON, %d of %d values untyped", gaps, all)
			default:
				r.untyped++
				state = "JSON, untyped"
			}
		case resp.Content["application/octet-stream"].Schema != nil:
			r.fileOps++
			state = "a file"
		case resp.Content["text/plain"].Schema != nil:
			r.textOps++
			state = "text"
		default:
			r.emptyOps++
			state = "nothing"
		}
		break
	}

	r.lines = append(r.lines, fmt.Sprintf("  %-6s %-60s %-55s %s", strings.ToUpper(verb), p, op.Handler, state))
}

// count is how many values a schema holds and how many of them have no type, following references once.
func (b *builder) count(s *schema, seen map[string]bool) (all, gaps int) {
	if s.Ref != "" {
		name := strings.TrimPrefix(s.Ref, "#/components/schemas/")
		if seen[name] {
			return 0, 0
		}
		seen[name] = true
		if t, ok := b.schemas[name]; ok {
			return b.count(t, seen)
		}
		return 1, 1
	}

	switch s.Type {
	case "":
		return 1, 1
	case "array":
		return b.count(s.Items, seen)
	case "object":
		for _, n := range sortedKeys(s.Properties) {
			a, g := b.count(s.Properties[n], seen)
			all += a
			gaps += g
		}
		if all == 0 {
			return 1, 1
		}
		return all, gaps
	}

	return 1, 0
}

func (r *report) schemas(b *builder) {
	r.records = len(b.schemas)
	for _, s := range b.schemas {
		for _, p := range s.Properties {
			r.fields++
			if p.Type == "" && p.Ref == "" {
				r.untypedFields++
			}
		}
	}
}

func (r *report) write(w io.Writer, version string, verbose bool) {
	_, _ = fmt.Fprintf(w, "Audiobookshelf %s: %d routes, %d operations\n", version, r.routes, r.ops)
	_, _ = fmt.Fprintf(w, "  answer JSON          %4d   typed %d, partly typed %d, untyped %d\n", r.jsonOps, r.typed, r.partly, r.untyped)
	_, _ = fmt.Fprintf(w, "  answer text          %4d\n", r.textOps)
	_, _ = fmt.Fprintf(w, "  answer a file        %4d\n", r.fileOps)
	_, _ = fmt.Fprintf(w, "  answer nothing       %4d\n", r.emptyOps)
	_, _ = fmt.Fprintf(w, "  answer not found     %4d\n", r.notFound)
	_, _ = fmt.Fprintf(w, "  records              %4d   %d fields, %d of them untyped\n", r.records, r.fields, r.untypedFields)
	_, _ = fmt.Fprintf(w, "  query parameters     %4d\n", r.query)
	_, _ = fmt.Fprintf(w, "  body fields          %4d\n", r.bodyFields)

	if verbose {
		for _, l := range r.lines {
			_, _ = fmt.Fprintln(w, l)
		}
	}
}

// dump writes a type as an indented tree, for -trace.
func dump(t *jsType, indent string, depth int) string {
	if t == nil {
		return indent + "nil\n"
	}

	names := []string{"unknown", "null", "string", "number", "integer", "boolean", "date", "list", "record", "class", "instance", "function", "request", "response", "mixed"}

	var b strings.Builder
	b.WriteString(indent + names[t.kind])
	if t.nullable {
		b.WriteString("?")
	}
	if t.name != "" {
		b.WriteString(" " + t.name)
	}
	if t.class != nil {
		b.WriteString(" " + t.class.name)
	}
	if t.open {
		b.WriteString(" (open)")
	}
	b.WriteString("\n")

	if depth > 6 {
		return b.String()
	}

	switch t.kind {
	case arrayKind:
		b.WriteString(dump(t.elem, indent+"  ", depth+1))
	case objectKind:
		for _, p := range t.props {
			b.WriteString(indent + "  ." + p.name + "\n" + dump(p.typ, indent+"    ", depth+1))
		}
	case mixedKind:
		for _, m := range t.of {
			b.WriteString(dump(m, indent+"  | ", depth+1))
		}
	default:
		// nothing inside to show
	}

	return b.String()
}
