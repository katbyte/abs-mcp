package main

import (
	"strings"
	"testing"
)

// recordsDoc is a document with records, and answers holding places the scan left untyped or made a copy at.
func recordsDoc() *document {
	answer := func(s *schema) map[string]response {
		return map[string]response{"200": {Content: map[string]media{"application/json": {Schema: s}}}}
	}

	return &document{
		Paths: map[string]map[string]*operation{
			"/sessions": {"get": {Responses: answer(&schema{Type: "object", Properties: map[string]*schema{"sessions": {Type: "array", Items: &schema{Type: "object", Properties: map[string]*schema{"id": {Type: "string"}}}}}})}},
			"/session":  {"get": {Responses: answer(&schema{Type: "object", Properties: map[string]*schema{"id": {Type: "string"}}})}},
		},
		Components: components{Schemas: map[string]*schema{
			"Author":  {Type: "object", Properties: map[string]*schema{"name": {Type: "string"}, "libraryItems": {}}},
			"Item":    {Type: "object", Properties: map[string]*schema{"id": {Type: "string"}}},
			"Session": {Type: "object", Properties: map[string]*schema{"id": {Type: "string"}}},
		}},
	}
}

// Each place is pointed at its record: an untyped field made a list of it, a copy in a list replaced, a whole answer replaced, and a field the
// record lacks made.
func TestApplyRecordsAt(t *testing.T) {
	t.Parallel()

	doc := recordsDoc()
	missing, err := applyRecordsAt(doc, []recordAt{
		{"Author .libraryItems[]", "Item"},
		{"Author .latest", "Session"},
		{"GET /sessions 200 .sessions[]", "Session"},
		{"GET /session 200 .", "Session"},
	}, nil)
	if err != nil || len(missing) != 0 {
		t.Fatalf("applyRecordsAt = %q, %v", missing, err)
	}

	author := doc.Components.Schemas["Author"]
	if items := author.Properties["libraryItems"]; items.Type != "array" || items.Items.Ref != "#/components/schemas/Item" {
		t.Errorf("an untyped field: %+v, want a list of the record", items)
	}
	if latest := author.Properties["latest"]; latest == nil || latest.Ref != "#/components/schemas/Session" {
		t.Errorf("a field the record lacked: %+v, want it made, pointing at the record", latest)
	}
	sessions := doc.Paths["/sessions"]["get"].Responses["200"].Content["application/json"].Schema
	if ref := sessions.Properties["sessions"].Items.Ref; ref != "#/components/schemas/Session" {
		t.Errorf("a copy in a list: %q, want the record", ref)
	}
	if ref := doc.Paths["/session"]["get"].Responses["200"].Content["application/json"].Schema.Ref; ref != "#/components/schemas/Session" {
		t.Errorf("a whole answer: %q, want the record", ref)
	}
}

// A stored record is made from the copies at its places, every field any of them holds, and each place, a body's among them, then refers to it.
func TestApplyRecordsAtMakesStoredRecords(t *testing.T) {
	t.Parallel()

	doc := recordsDoc()
	doc.Components.Schemas["Item"].Properties["chapters"] = &schema{Type: "array", Items: &schema{Type: "object", Properties: map[string]*schema{"start": {Type: "integer"}, "id": {Type: "integer", written: true}}}}
	doc.Components.Schemas["Session"].Properties["chapters"] = &schema{Type: "array", Items: &schema{Type: "object", Properties: map[string]*schema{"start": {Type: "number"}, "title": {Type: "string"}}}}
	doc.Paths["/chapters"] = map[string]*operation{"post": {RequestBody: &body{Content: map[string]media{"application/json": {Schema: &schema{Type: "object", Properties: map[string]*schema{"chapters": {}}}}}}}}

	missing, err := applyRecordsAt(doc, []recordAt{
		{"Item .chapters[]", "Chapter"},
		{"Session .chapters[]", "Chapter"},
		{"POST /chapters body .chapters[]", "Chapter"},
	}, []string{"Chapter"})
	if err != nil || len(missing) != 0 {
		t.Fatalf("applyRecordsAt = %q, %v", missing, err)
	}

	chapter := doc.Components.Schemas["Chapter"]
	if chapter == nil || chapter.Properties["start"].Type != "number" || chapter.Properties["title"].Type != "string" {
		t.Fatalf("the record made: %+v, want start a number and title, from both copies", chapter)
	}
	if id := chapter.Properties["id"]; id.Type != "number" || !id.whole {
		t.Errorf("a whole number written out at one place: %+v, want a number seen whole, for the names of whole numbers to settle", id)
	}
	for _, s := range []*schema{
		doc.Components.Schemas["Item"].Properties["chapters"].Items,
		doc.Components.Schemas["Session"].Properties["chapters"].Items,
		doc.Paths["/chapters"]["post"].RequestBody.Content["application/json"].Schema.Properties["chapters"].Items,
	} {
		if s.Ref != "#/components/schemas/Chapter" {
			t.Errorf("a place: %+v, want the record", s)
		}
	}

	if _, err := applyRecordsAt(doc, []recordAt{{"Item .chapters[]", "Chapter"}}, []string{"Chapter"}); err == nil {
		t.Error("a place stated twice: no error, want one, since the record would refer to itself")
	}
}

// A place stated inside a body the scan saw nothing of makes the records on the way to it, and they stay open to what a client sends, as the
// untyped body was.
func TestApplyRecordsAtLeavesAnUnseenBodyOpen(t *testing.T) {
	t.Parallel()

	doc := recordsDoc()
	doc.Paths["/batch"] = map[string]*operation{"post": {RequestBody: &body{Content: map[string]media{"application/json": {Schema: &schema{Type: "array", Items: &schema{}}}}}}}
	if _, err := applyRecordsAt(doc, []recordAt{{"POST /batch body .[].payload.authors[]", "Author"}, {"POST /batch body .[].payload.series[]", "Item"}}, nil); err != nil {
		t.Fatalf("applyRecordsAt: %v", err)
	}

	sent := doc.Paths["/batch"]["post"].RequestBody.Content["application/json"].Schema
	a := newAnswerFill()
	a.merge(sent, inferAll([]any{decoded(t, `[{"id": "x", "payload": {"title": "T", "authors": [{"name": "A"}]}}]`)}), doc.Components.Schemas, "body", place{sent: true})

	item := sent.Items
	if item.Properties["id"] == nil || item.Properties["payload"].Properties["title"] == nil || item.Properties["payload"].Properties["series"] == nil || len(a.conflicts) != 0 {
		t.Errorf("the body: %s, conflicts %q; want what was sent filled in beside the places stated", describe(sent), a.conflicts)
	}
}

// A record made from places the scan saw nothing of stays open to what a client sends, as each place was.
func TestApplyRecordsAtKeepsAStoredRecordOpen(t *testing.T) {
	t.Parallel()

	doc := recordsDoc()
	doc.Paths["/one"] = map[string]*operation{"patch": {RequestBody: &body{Content: map[string]media{"application/json": {Schema: &schema{}}}}}}
	doc.Paths["/many"] = map[string]*operation{"post": {RequestBody: &body{Content: map[string]media{"application/json": {Schema: &schema{Type: "array", Items: &schema{}}}}}}}
	places := []recordAt{{"PATCH /one body .payload.authors[]", "Author"}, {"POST /many body .[].payload.authors[]", "Author"}, {"PATCH /one body .payload", "Payload"}, {"POST /many body .[].payload", "Payload"}}
	if _, err := applyRecordsAt(doc, places, []string{"Payload"}); err != nil {
		t.Fatalf("applyRecordsAt: %v", err)
	}

	a := newAnswerFill()
	a.merge(doc.Paths["/one"]["patch"].RequestBody.Content["application/json"].Schema, inferAll([]any{decoded(t, `{"payload": {"title": "T"}}`)}), doc.Components.Schemas, "body", place{sent: true})
	if doc.Components.Schemas["Payload"].Properties["title"] == nil || len(a.conflicts) != 0 {
		t.Errorf("the record: %s, conflicts %q; want what was sent filled in", describe(doc.Components.Schemas["Payload"]), a.conflicts)
	}
}

// A place whose record or operation the document does not have is reported, as is one inside a record the document names, where the answer
// is that record alone; one inside them that is not as stated is an error naming it.
func TestApplyRecordsAtRefuses(t *testing.T) {
	t.Parallel()

	doc := recordsDoc()
	doc.Paths["/named"] = map[string]*operation{"get": {Responses: map[string]response{"200": {Content: map[string]media{"application/json": {Schema: &schema{Ref: "#/components/schemas/Session"}}}}}}}
	missing, err := applyRecordsAt(doc, []recordAt{{"Author .libraryItems[]", "Missing"}, {"Missing .x", "Item"}, {"GET /missing 200 .x", "Item"}, {"GET /named 200 .extra", "Item"}}, nil)
	if err != nil || len(missing) != 4 {
		t.Errorf("places the document does not have = %q, %v; want the four reported", missing, err)
	}
	if doc.Components.Schemas["Session"].Properties["extra"] != nil {
		t.Error("a place inside a named record: want the record left as it was")
	}

	for _, r := range []recordAt{
		{"GET /sessions 201 .x", "Item"},
		{"Author .missing.x", "Item"},
		{"Author .name[]", "Item"},
		{"Author", "Item"},
	} {
		if _, err := applyRecordsAt(recordsDoc(), []recordAt{r}, nil); err == nil || !strings.Contains(err.Error(), r.at) {
			t.Errorf("%s -> %s: %v, want an error naming the place", r.at, r.record, err)
		}
	}
}
