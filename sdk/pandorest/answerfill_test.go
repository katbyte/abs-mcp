package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/pandorest/answers"
)

// decoded is JSON as the answers package reads it, numbers kept as written.
func decoded(t *testing.T, js string) any {
	t.Helper()

	d := json.NewDecoder(strings.NewReader(js))
	d.UseNumber()

	var v any
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}

	return v
}

func TestInferAll(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		`[1, 2]`:                     "integer",
		`[1, 2.5]`:                   "number",
		`["a", null]`:                "string?",
		`["a", 1]`:                   "",
		`[{"a": 1}, {"b": "x"}]`:     "object{a:integer,b:string}",
		`[[1], []]`:                  "array[integer]",
		`[[]]`:                       "array[]",
		`[{"2026-10-14": 1}]`:        "map[integer]",
		`[{"1": 1}, {"2": 2.5}]`:     "map[number]",
		`[{"a": null}, {"a": true}]`: "object{a:boolean?}",
	}
	for in, want := range cases {
		samples, ok := decoded(t, in).([]any)
		if !ok {
			t.Fatalf("%s is not a list of samples", in)
		}
		if got := describe(inferAll(samples)); got != want {
			t.Errorf("inferAll(%s) = %s, want %s", in, got, want)
		}
	}
}

// describe writes a schema short, for comparing in a test.
func describe(s *schema) string {
	if s == nil {
		return "nil"
	}

	out := s.Type
	switch {
	case s.Type == "object" && s.Values != nil:
		out = "map[" + describe(s.Values) + "]"
	case s.Type == "object" && s.AdditionalProperties && len(s.Properties) == 0:
		out += "*"
	case s.Type == "object":
		var fields []string
		for _, n := range sortedKeys(s.Properties) {
			fields = append(fields, n+":"+describe(s.Properties[n]))
		}
		out += "{" + strings.Join(fields, ",") + "}"
	case s.Type == "array":
		out += "[" + describe(s.Items) + "]"
	}
	if s.Nullable {
		out += "?"
	}

	return out
}

func TestAnswerFillMerge(t *testing.T) {
	t.Parallel()

	components := map[string]*schema{"Book": {Type: "object", Properties: map[string]*schema{"title": {Type: "string"}}}}
	to := &schema{Type: "object", Properties: map[string]*schema{
		"untyped":  {},
		"count":    {Type: "integer"},
		"name":     {Type: "string"},
		"duration": {Type: "number"},
		"offset":   {Type: "number", whole: true},
		"settings": {Type: "object", AdditionalProperties: true},
		"book":     {Ref: "#/components/schemas/Book"},
		"label":    {Type: "string"},
		"shelf":    {OneOf: []*schema{{Ref: "#/components/schemas/Book"}}},
	}}
	a := newAnswerFill()
	a.merge(to, inferAll([]any{decoded(t, `{"untyped": {"n": 1}, "count": 2.5, "name": null, "duration": 3, "offset": 1.5, "settings": {"theme": "dark"}, "book": {"title": "T", "isbn": "1"}, "label": 7, "added": true, "shelf": {"title": "S", "entities": []}}`)}), components, "answer", place{})

	if got := describe(to.Properties["untyped"]); got != "object{n:number}" || !to.Properties["untyped"].Properties["n"].whole {
		t.Errorf("an untyped value: %s, want filled, its whole number a number seen whole", got)
	}
	if to.Properties["count"].Type != "number" {
		t.Errorf("a whole number sent with a fraction: %s, want a number", to.Properties["count"].Type)
	}
	if !to.Properties["name"].Nullable {
		t.Error("a value sent null: want it nullable")
	}
	if to.Properties["duration"].Type != "number" || to.Properties["duration"].whole {
		t.Errorf("a number sent whole: %+v, want it left a number", to.Properties["duration"])
	}
	if to.Properties["offset"].whole {
		t.Error("a number seen whole elsewhere, sent with a fraction: want it no longer seen whole")
	}
	if components["Book"].Properties["isbn"] == nil {
		t.Error("a field a shared record lacks: want it added")
	}
	if shelf := to.Properties["shelf"]; len(shelf.OneOf) != 1 || shelf.Type != "" || components["Book"].Properties["entities"] != nil {
		t.Errorf("a value of several kinds: %+v, want it left as stated, its kinds untouched", shelf)
	}
	if to.Properties["label"].Type != "string" {
		t.Errorf("a disagreement: %s, want it left as the source says", to.Properties["label"].Type)
	}
	if to.Properties["added"] == nil {
		t.Error("a field the record lacked: want it added")
	}

	if a.filled != 2 {
		t.Errorf("filled %d, want the untyped value and the open record's field", a.filled)
	}
	wantCorrected := []string{"Book.isbn: the server sends it, and the document did not have it", "answer.added: the server sends it, and the document did not have it", "answer.count: the server sent a fraction", "answer.name: the server sent null"}
	if got := slices.Sorted(slices.Values(a.corrected)); !slices.Equal(got, slices.Sorted(slices.Values(wantCorrected))) {
		t.Errorf("corrected:\n%q\nwant\n%q", got, wantCorrected)
	}
	wantConflicts := []string{"answer.label: the source says string, the server sent integer"}
	if got := slices.Sorted(slices.Values(a.conflicts)); !slices.Equal(got, wantConflicts) {
		t.Errorf("conflicts:\n%q\nwant\n%q", got, wantConflicts)
	}

	var report bytes.Buffer
	a.write(&report, "answers", true)
	for _, line := range []string{"filled 2 values", "corrected 4", "1 differ", "corrected: answer.count: the server sent a fraction", "differs: answer.label"} {
		if !strings.Contains(report.String(), line) {
			t.Errorf("report has no %q:\n%s", line, report.String())
		}
	}
}

// A map's keys are data whatever they look like: an answer keyed by day names fills what every key holds rather than adding a field a day, and a
// map the source writes as {} is filled from an answer keyed by days.
func TestAnswerFillIntoAMap(t *testing.T) {
	t.Parallel()

	to := &schema{Type: "object", Properties: map[string]*schema{
		"dayOfWeek": {Type: "object", Values: &schema{Type: "integer"}},
		"days":      {Type: "object"},
	}}
	a := newAnswerFill()
	a.merge(to, inferAll([]any{decoded(t, `{"dayOfWeek": {"Thursday": 1.5}, "days": {"2026-10-14": {"t": 1}}}`)}), nil, "answer", place{})

	if got := describe(to.Properties["dayOfWeek"]); got != "map[number]" {
		t.Errorf("a map keyed by names: %s, want map[number], what the key held, and no field for the day", got)
	}
	if got := describe(to.Properties["days"]); got != "map[object{t:number}]" {
		t.Errorf("a map the source left empty: %s, want map[object{t:number}]", got)
	}
}

// A map's values are written as the document's additionalProperties and read back the same; a record that may hold more fields stays true.
func TestSchemaMapJSON(t *testing.T) {
	t.Parallel()

	for _, in := range []*schema{
		{Type: "object", Values: &schema{Type: "number"}},
		{Type: "object", AdditionalProperties: true},
		{Type: "object", Properties: map[string]*schema{"days": {Type: "object", Values: &schema{Ref: "#/components/schemas/Day"}}}},
	} {
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var out schema
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		if fingerprint(&out) != fingerprint(in) || out.AdditionalProperties != in.AdditionalProperties {
			t.Errorf("%s read back as %s", b, describe(&out))
		}
	}

	b, _ := json.Marshal(&schema{Type: "object", Values: &schema{Type: "number"}})
	if string(b) != `{"type":"object","additionalProperties":{"type":"number"}}` {
		t.Errorf("a map of numbers: %s", b)
	}
}

// A shared record answered again, with a field null that came with a value before, is filled again: the null is a shape of its own.
func TestAnswerFillSeesANullInASharedRecord(t *testing.T) {
	t.Parallel()

	components := map[string]*schema{"Series": {Type: "object", Properties: map[string]*schema{"sequence": {Type: "string"}}}}
	to := &schema{Ref: "#/components/schemas/Series"}
	a := newAnswerFill()
	a.merge(to, inferAll([]any{decoded(t, `{"sequence": "1"}`)}), components, "first", place{})
	a.merge(to, inferAll([]any{decoded(t, `{"sequence": "1"}`), decoded(t, `{"sequence": null}`)}), components, "second", place{})

	if !components["Series"].Properties["sequence"].Nullable {
		t.Error("a field a later answer sent null: want it nullable")
	}
}

// What a client sent only fills what the document leaves untyped: it shows what one client chose to send, not what the server reads.
func TestAnswerFillMergeSent(t *testing.T) {
	t.Parallel()

	to := &schema{Type: "object", Properties: map[string]*schema{"userId": {}, "isActive": {Type: "boolean"}, "count": {Type: "integer"}}}

	a := newAnswerFill()
	a.merge(to, inferAll([]any{decoded(t, `{"userId": "u1", "isActive": null, "count": 1.5, "name": "x"}`)}), nil, "body", place{sent: true})

	if to.Properties["userId"].Type != "string" || a.filled != 1 {
		t.Errorf("an untyped field: %+v, filled %d, want it typed", to.Properties["userId"], a.filled)
	}
	if to.Properties["isActive"].Nullable || to.Properties["count"].Type != "integer" || to.Properties["name"] != nil {
		t.Errorf("body: %s, want nothing but the untyped field changed", describe(to))
	}
	if len(a.corrected) != 0 {
		t.Errorf("corrected: %q, want none from what a client sent", a.corrected)
	}
	wantConflicts := []string{"body.count: the source says integer, the client sends number", "body.name: the client sends it, and the source does not show the server reading it"}
	if got := slices.Sorted(slices.Values(a.conflicts)); !slices.Equal(got, wantConflicts) {
		t.Errorf("conflicts:\n%q\nwant\n%q", got, wantConflicts)
	}

	// a body the source hands on whole may hold any field, so what a working client sends fills it in
	open := &schema{Type: "object", AdditionalProperties: true, Properties: map[string]*schema{}}
	a.merge(open, inferAll([]any{decoded(t, `{"email": "e"}`)}), nil, "open", place{sent: true})
	if open.Properties["email"] == nil || a.filled != 2 {
		t.Errorf("an open body: %s, filled %d, want the field sent", describe(open), a.filled)
	}
}

func TestAnswerFillMatchesOperations(t *testing.T) {
	t.Parallel()

	items := &schema{}
	files := []*answers.File{
		{Operation: "ItemList", Method: "GET", Path: "/api/items", Answers: map[string][]any{"200": {decoded(t, `[{"id": "a"}]`)}, "201": {decoded(t, `{}`)}}},
		{Operation: "Gone", Method: "GET", Path: "/api/gone"},
	}

	a := newAnswerFill()
	a.fill(&document{Paths: map[string]map[string]*operation{"/api/items": {"get": {Responses: map[string]response{"200": {Content: map[string]media{"application/json": {Schema: items}}}}}}}}, files)

	if describe(items) != "array[object{id:string}]" || !a.ops["GET /api/items"] {
		t.Errorf("answer: %s, operations %v", describe(items), a.ops)
	}
	if !slices.Equal(a.unmatched, []string{"GET /api/gone (Gone)"}) {
		t.Errorf("unmatched: %q", a.unmatched)
	}
	if !slices.Equal(a.conflicts, []string{"GET /api/items: the server answered 201 with JSON, which the document does not say"}) {
		t.Errorf("conflicts: %q", a.conflicts)
	}
}

// The hand-written client settles a number the server only ever sent whole.
func TestByhandSettlesAWholeNumber(t *testing.T) {
	t.Parallel()

	to := &schema{Type: "number", whole: true}
	b := &byhand{visitedRef: map[string]bool{}}
	b.merge(to, &schema{Type: "integer"}, nil, "count")
	if to.Type != "integer" || to.whole || b.settled != 1 || b.filled != 0 {
		t.Errorf("settled: %+v, settled %d, filled %d", to, b.settled, b.filled)
	}

	plain := &schema{Type: "number"}
	b.merge(plain, &schema{Type: "integer"}, nil, "duration")
	if plain.Type != "number" {
		t.Errorf("a number the source says: %s, want it left a number", plain.Type)
	}
}

// The hand-written client adds the fields it sends to a record a stated place made where the scan saw nothing, as it would to an untyped one,
// and adds none to a record the source wrote.
func TestByhandFillsAnUnseenRecord(t *testing.T) {
	t.Parallel()

	from := &schema{Type: "object", Properties: map[string]*schema{"title": {Type: "string"}, "feedUrl": {Type: "string"}}}
	unseen := &schema{Type: "object", unseen: true, Properties: map[string]*schema{"title": {}}}
	written := &schema{Type: "object", Properties: map[string]*schema{"title": {}}}

	b := &byhand{visitedRef: map[string]bool{}}
	b.merge(unseen, from, nil, "metadata")
	b.merge(written, from, nil, "metadata")

	if unseen.Properties["title"].Type != "string" || unseen.Properties["feedUrl"] == nil {
		t.Errorf("the unseen record: %s, want its field typed and the field it lacked added", describe(unseen))
	}
	if written.Properties["title"].Type != "string" || written.Properties["feedUrl"] != nil {
		t.Errorf("the source's record: %s, want its field typed and nothing added", describe(written))
	}
}

// A value of several kinds is stated, so the hand-written client neither types it nor counts it as a disagreement.
func TestByhandLeavesAUnion(t *testing.T) {
	t.Parallel()

	to := &schema{OneOf: []*schema{{Ref: "#/components/schemas/Book"}}}
	b := &byhand{visitedRef: map[string]bool{}}
	b.merge(to, &schema{Type: "object", Properties: map[string]*schema{"title": {Type: "string"}}}, nil, "shelf")
	if to.Type != "" || len(to.OneOf) != 1 || b.filled != 0 || len(b.conflicts) != 0 {
		t.Errorf("a union: %+v, filled %d, conflicts %q", to, b.filled, b.conflicts)
	}
}

// Each record where the document has a union fills the member its field names, and a kind the union has no member for is listed.
func TestAnswerFillThroughUnions(t *testing.T) {
	t.Parallel()

	components := map[string]*schema{
		"Item":       {Type: "object", Properties: map[string]*schema{"id": {Type: "string"}}},
		"ItemShelf":  {Type: "object", Properties: map[string]*schema{"type": {Type: "string"}, "entities": {Type: "array", Items: &schema{Ref: "#/components/schemas/Item"}}}},
		"OtherShelf": {Type: "object", Properties: map[string]*schema{"type": {Type: "string"}, "total": {}}},
		"Shelf": {
			OneOf:         []*schema{{Ref: "#/components/schemas/ItemShelf"}, {Ref: "#/components/schemas/OtherShelf"}},
			Discriminator: &discriminator{PropertyName: "type", Mapping: map[string]string{"book": "#/components/schemas/ItemShelf", "other": "#/components/schemas/OtherShelf"}},
		},
	}
	answer := &schema{Type: "array", Items: &schema{Ref: "#/components/schemas/Shelf"}}

	a := newAnswerFill()
	a.throughUnions(answer, decoded(t, `[{"type": "book", "entities": [{"id": "a", "recentEpisode": {"title": "E"}}]}, {"type": "other", "total": 3}, {"type": "series"}]`), components, "answer")

	if ep := components["Item"].Properties["recentEpisode"]; ep == nil || ep.Type != "object" {
		t.Errorf("a field of a record a member holds: %+v, want it added", ep)
	}
	if total := components["OtherShelf"].Properties["total"]; total.Type != "number" || !total.whole {
		t.Errorf("an untyped field of the other member: %+v, want it filled", total)
	}
	if components["ItemShelf"].Properties["total"] != nil {
		t.Error("the other member's field went to the item shelf")
	}
	if want := []string{`Shelf: the server sent a type of "series", which the union has no record for`}; !slices.Equal(a.conflicts, want) {
		t.Errorf("conflicts: %q, want %q", a.conflicts, want)
	}
}
