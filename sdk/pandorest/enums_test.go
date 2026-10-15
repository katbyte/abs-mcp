package main

import (
	"slices"
	"strings"
	"testing"
)

var testEnumNames = []enumName{{field: "type", value: "series", name: "ShelfType"}, {field: "sort", value: "title", name: "Sort"}}

// enumsDoc is a document with a set of values in a record, the same set seen with fewer values in an answer, and one on a query parameter.
func enumsDoc() *document {
	return &document{
		Paths: map[string]map[string]*operation{"/shelves": {"get": {
			Parameters: []parameter{{Name: "sort", In: "query", Schema: &schema{Type: "string", Enum: []string{"added", "title"}}}, {Name: "q", In: "query", Schema: &schema{Type: "string"}}},
			Responses: map[string]response{"200": {Content: map[string]media{"application/json": {Schema: &schema{Type: "array", Items: &schema{Type: "object", Properties: map[string]*schema{
				"type": {Type: "string", Enum: []string{"book", "series"}, Nullable: true},
			}}}}}}},
		}}},
		Components: components{Schemas: map[string]*schema{
			"Shelf": {Type: "object", Properties: map[string]*schema{"type": {Type: "string", Enum: []string{"authors", "series"}}, "name": {Type: "string"}}},
		}},
	}
}

// Each set becomes a record named by the table, holding every value any place takes, and every place points there.
func TestNameEnums(t *testing.T) {
	t.Parallel()

	doc := enumsDoc()
	if err := nameEnums(doc, testEnumNames); err != nil {
		t.Fatal(err)
	}

	if set := doc.Components.Schemas["ShelfType"]; set == nil || set.Type != "string" || !slices.Equal(set.Enum, []string{"authors", "book", "series"}) {
		t.Errorf("ShelfType: %+v, want every value of both places", set)
	}
	if typ := doc.Components.Schemas["Shelf"].Properties["type"]; typ.Ref != "#/components/schemas/ShelfType" || typ.Enum != nil {
		t.Errorf("the record's type: %+v, want it pointed at the set", typ)
	}
	op := doc.Paths["/shelves"]["get"]
	if typ := op.Responses["200"].Content["application/json"].Schema.Items.Properties["type"]; typ.Ref != "#/components/schemas/ShelfType" || !typ.Nullable {
		t.Errorf("the answer's type: %+v, want it pointed at the set and still nullable", typ)
	}
	if sort, q := op.Parameters[0].Schema, op.Parameters[1].Schema; sort.Ref != "#/components/schemas/Sort" || q.Ref != "" || q.Type != "string" {
		t.Errorf("parameters: sort %+v, q %+v; want sort pointed at its set and q left as text", sort, q)
	}
}

// A set the table does not name stops the scan, naming where it is, as does a name a record already has.
func TestNameEnumsRefuses(t *testing.T) {
	t.Parallel()

	if err := nameEnums(enumsDoc(), testEnumNames[:1]); err == nil || !strings.Contains(err.Error(), `GET /shelves.sort ["added" "title"]`) {
		t.Errorf("an unnamed set = %v, want it named in the error", err)
	}

	// a field of the table's name whose set does not hold the table's value is another set, and unnamed
	if err := nameEnums(enumsDoc(), []enumName{{field: "type", value: "podcast", name: "ShelfType"}, testEnumNames[1]}); err == nil || !strings.Contains(err.Error(), "Shelf.type") {
		t.Errorf("a set without the table's value = %v, want it unnamed", err)
	}

	if err := nameEnums(enumsDoc(), []enumName{{field: "type", value: "series", name: "Shelf"}, testEnumNames[1]}); err == nil || !strings.Contains(err.Error(), "named Shelf has the name of a record") {
		t.Errorf("a name a record has = %v, want an error", err)
	}
}
