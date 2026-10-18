package main

import "testing"

// A number the answers marked whole is written whole under a name of the list, in a record or an answer, and left a number otherwise.
func TestSettleWhole(t *testing.T) {
	t.Parallel()

	marked := func() *schema { return &schema{Type: "number", whole: true} }
	doc := &document{
		Paths: map[string]map[string]*operation{"/x": {"get": {Responses: map[string]response{"200": {Content: map[string]media{"application/json": {Schema: &schema{Type: "array", Items: &schema{Type: "object", Properties: map[string]*schema{"size": marked()}}}}}}}}}},
		Components: components{Schemas: map[string]*schema{"Track": {Type: "object", Properties: map[string]*schema{
			"index":    marked(),
			"duration": marked(),
			"total":    {Type: "number"},
		}}}},
	}

	if n := settleWhole(doc, []string{"index", "size", "total"}); n != 2 {
		t.Errorf("settled %d, want the index and the size", n)
	}
	track := doc.Components.Schemas["Track"].Properties
	if track["index"].Type != "integer" || track["duration"].Type != "number" || track["total"].Type != "number" {
		t.Errorf("index %s, duration %s, total %s; want a whole index, and the rest left numbers", track["index"].Type, track["duration"].Type, track["total"].Type)
	}
	if size := doc.Paths["/x"]["get"].Responses["200"].Content["application/json"].Schema.Items.Properties["size"]; size.Type != "integer" {
		t.Errorf("size in an answer: %s, want whole", size.Type)
	}
}
