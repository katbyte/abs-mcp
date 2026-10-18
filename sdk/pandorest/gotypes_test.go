package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A place stated is marked with its type, in an answer and in a record, and the document writes the mark where the importer reads it.
func TestApplyGoTypes(t *testing.T) {
	t.Parallel()

	doc := recordsDoc()
	doc.Components.Schemas["Author"].Properties["libraryItems"] = &schema{Type: "array", Items: &schema{Type: "object"}}
	missing, err := applyGoTypes(doc, []goTypeAt{
		{"GET /sessions 200 .sessions", "ListOrEmpty[$item]"},
		{"Author .libraryItems[]", "Item"},
	})
	if err != nil || len(missing) != 0 {
		t.Fatalf("applyGoTypes = %q, %v", missing, err)
	}

	sessions := doc.Paths["/sessions"]["get"].Responses["200"].Content["application/json"].Schema.Properties["sessions"]
	if sessions.GoType == nil || sessions.GoType.Name != "ListOrEmpty[$item]" {
		t.Errorf("the answer's field: %+v, want it held as the type stated", sessions.GoType)
	}
	if item := doc.Components.Schemas["Author"].Properties["libraryItems"].Items; item.GoType == nil || item.GoType.Name != "Item" {
		t.Errorf("a list's element: %+v, want it held as the type stated", item.GoType)
	}

	written, err := json.Marshal(sessions)
	if err != nil || !strings.Contains(string(written), `"x-go-type":{"name":"ListOrEmpty[$item]"}`) {
		t.Errorf("written as %s, %v; want the type under x-go-type", written, err)
	}
}

// A place whose operation the document does not have is reported; one inside it that is not there is an error naming it.
func TestApplyGoTypesRefuses(t *testing.T) {
	t.Parallel()

	missing, err := applyGoTypes(recordsDoc(), []goTypeAt{{"GET /missing 200 .x", "T"}})
	if err != nil || len(missing) != 1 {
		t.Errorf("a place the document does not have = %q, %v; want it reported", missing, err)
	}

	for _, at := range []string{"GET /sessions 200 .missing", "Author .name[]", "Author"} {
		if _, err := applyGoTypes(recordsDoc(), []goTypeAt{{at, "T"}}); err == nil || !strings.Contains(err.Error(), at) {
			t.Errorf("%s: %v, want an error naming the place", at, err)
		}
	}
}
