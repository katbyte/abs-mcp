package main

import (
	"slices"
	"testing"
)

// A value stated is written where the document leaves the place untyped, a null the scan saw kept, and a field the record lacks made; a place
// already typed is left as it is and reported, and one whose record or operation the document lacks is reported missing.
func TestApplyValues(t *testing.T) {
	t.Parallel()

	doc := recordsDoc()
	doc.Components.Schemas["Item"].Properties["error"] = &schema{Nullable: true}
	doc.Components.Schemas["Item"].Properties["sizes"] = &schema{}

	missing, typed, err := applyValues(doc, []valueAt{
		{"Item .error", "string"},
		{"Item .sizes", "[]integer"},
		{"Item .note", "string?"},
		{"Item .id", "integer"},
		{"GET /sessions 200 .sessions[].device", "open"},
		{"Nothing .id", "string"},
		{"GET /nowhere 200 .id", "string"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(missing, []string{"Nothing .id", "GET /nowhere 200 .id"}) {
		t.Errorf("missing = %q, want the record and the operation the document lacks", missing)
	}
	if !slices.Equal(typed, []string{"Item .id"}) {
		t.Errorf("typed = %q, want the field the document already types", typed)
	}

	item := doc.Components.Schemas["Item"].Properties
	if e := item["error"]; e.Type != "string" || !e.Nullable {
		t.Errorf("error = %+v, want text that may be null, as the scan saw it", e)
	}
	if s := item["sizes"]; s.Type != "array" || s.Items == nil || s.Items.Type != "integer" {
		t.Errorf("sizes = %+v, want a list of whole numbers", s)
	}
	if n := item["note"]; n == nil || n.Type != "string" || !n.Nullable {
		t.Errorf("note = %+v, want it made, as text that may be null", n)
	}
	if id := item["id"]; id.Type != "string" {
		t.Errorf("id = %+v, want it left as the document has it", id)
	}
	device := doc.Paths["/sessions"]["get"].Responses["200"].Content["application/json"].Schema.Properties["sessions"].Items.Properties["device"]
	if device == nil || device.Type != "object" || !device.AdditionalProperties {
		t.Errorf("device = %+v, want a record of any keys", device)
	}
}

// A type written short is refused when it names none.
func TestValueSchemaRefuses(t *testing.T) {
	t.Parallel()

	for _, v := range []string{"", "text", "[]", "string??"} {
		if _, err := valueSchema(v); err == nil {
			t.Errorf("%q was taken as a type", v)
		}
	}
	if _, _, err := applyValues(recordsDoc(), []valueAt{{"Item .id", "text"}}); err == nil {
		t.Error("a statement with no type was applied")
	}
}
