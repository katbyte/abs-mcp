package main

import (
	"slices"
	"strings"
	"testing"
)

// shelvesDoc is a document whose answer is a list of shelves, each with a type and entities the scan typed as one record.
func shelvesDoc() *document {
	shelf := &schema{Type: "object", Properties: map[string]*schema{
		"id":       {Type: "string"},
		"type":     {Type: "string", Nullable: true},
		"entities": {Type: "array", Items: &schema{Ref: "#/components/schemas/Item"}},
	}}
	answer := &schema{Type: "array", Items: shelf}

	return &document{
		Paths: map[string]map[string]*operation{"/shelves": {"get": {Responses: map[string]response{"200": {Content: map[string]media{"application/json": {Schema: answer}}}}}}},
		Components: components{Schemas: map[string]*schema{
			"Item":   {Type: "object", Properties: map[string]*schema{"id": {Type: "string"}}},
			"Series": {Type: "object", Properties: map[string]*schema{"name": {Type: "string"}}},
		}},
	}
}

var shelves = union{name: "Shelf", method: "get", path: "/shelves", field: "type", list: "entities", members: []unionMember{
	{name: "ItemShelf", kinds: []string{"book", "podcast"}, record: "Item"},
	{name: "SeriesShelf", kinds: []string{"series"}, record: "Series"},
}}

// A union is written as a record per kind, holding its own record, and a oneOf told apart by the field, every value naming its kind.
func TestApplyUnions(t *testing.T) {
	t.Parallel()

	doc := shelvesDoc()
	missing, err := applyUnions(doc, []union{shelves})
	if err != nil || len(missing) != 0 {
		t.Fatalf("applyUnions = %q, %v", missing, err)
	}

	if ref := doc.Paths["/shelves"]["get"].Responses["200"].Content["application/json"].Schema.Items.Ref; ref != "#/components/schemas/Shelf" {
		t.Errorf("the answer's items: %q, want the union", ref)
	}
	items := doc.Components.Schemas["Shelf"]
	if items == nil || len(items.OneOf) != 2 || items.OneOf[0].Ref != "#/components/schemas/ItemShelf" || items.OneOf[1].Ref != "#/components/schemas/SeriesShelf" {
		t.Errorf("oneOf: %+v, want the item shelf then the series shelf", items.OneOf)
	}
	want := map[string]string{"book": "#/components/schemas/ItemShelf", "podcast": "#/components/schemas/ItemShelf", "series": "#/components/schemas/SeriesShelf"}
	if d := items.Discriminator; d == nil || d.PropertyName != "type" || len(d.Mapping) != len(want) || d.Mapping["podcast"] != want["podcast"] || d.Mapping["series"] != want["series"] {
		t.Errorf("discriminator: %+v, want type naming every kind", d)
	}

	series := doc.Components.Schemas["SeriesShelf"]
	if series == nil || series.Properties["entities"].Items.Ref != "#/components/schemas/Series" || !slices.Equal(series.Properties["type"].Enum, []string{"book", "podcast", "series"}) || series.Properties["id"] == nil {
		t.Errorf("series shelf: %+v, want the shelf's fields, a type of every kind, and series", series)
	}
	if item := doc.Components.Schemas["ItemShelf"]; item.Properties["entities"].Items.Ref != "#/components/schemas/Item" || item.Properties["type"].Nullable {
		t.Errorf("item shelf: %+v, want items and a type that is always there", item)
	}
}

// A union stated for an operation the document does not have is reported; one whose answer has changed shape is an error, so it is mended.
func TestApplyUnionsRefuses(t *testing.T) {
	t.Parallel()

	missing, err := applyUnions(shelvesDoc(), []union{{method: "get", path: "/elsewhere"}})
	if err != nil || !slices.Equal(missing, []string{"GET /elsewhere"}) {
		t.Errorf("no such operation = %q, %v; want it reported", missing, err)
	}

	for name, change := range map[string]func(*document, *union){
		"the answer has no such field": func(_ *document, u *union) { u.field = "kind" },
		"the record held is not there": func(_ *document, u *union) { u.members = []unionMember{{name: "AuthorShelf", record: "Author"}} },
		"the name is a record already": func(_ *document, u *union) { u.members = []unionMember{{name: "Series", record: "Item"}} },
		"the union's name is a record": func(_ *document, u *union) { u.name = "Item" },
		"the answer is not a list": func(d *document, _ *union) {
			d.Paths["/shelves"]["get"].Responses["200"].Content["application/json"].Schema.Type = "object"
		},
	} {
		doc, u := shelvesDoc(), shelves
		change(doc, &u)
		if _, err := applyUnions(doc, []union{u}); err == nil || !strings.Contains(err.Error(), "union get /shelves") {
			t.Errorf("%s: %v, want an error naming the union", name, err)
		}
	}
}
