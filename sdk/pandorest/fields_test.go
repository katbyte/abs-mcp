package main

import (
	"slices"
	"strings"
	"testing"
)

// The fields stated are marked required on the body, once each and in order, and the document writes them where the importer reads them.
func TestApplyRequired(t *testing.T) {
	t.Parallel()

	doc := recordsDoc()
	doc.Paths["/share"] = map[string]*operation{"post": {RequestBody: &body{Content: map[string]media{"application/json": {Schema: &schema{Type: "object", Properties: map[string]*schema{"slug": {Type: "string"}, "expiresAt": {Type: "integer"}, "note": {Type: "string"}}}}}}}}

	missing, err := applyRequired(doc, []requiredAt{{"POST /share body .", []string{"slug", "expiresAt"}}, {"POST /share body .", []string{"slug"}}})
	if err != nil || len(missing) != 0 {
		t.Fatalf("applyRequired = %q, %v", missing, err)
	}
	if got := doc.Paths["/share"]["post"].RequestBody.Content["application/json"].Schema.Required; !slices.Equal(got, []string{"expiresAt", "slug"}) {
		t.Errorf("required = %q, want expiresAt and slug", got)
	}
}

// A place whose operation the document does not have is reported; a field the body does not have is an error naming the place.
func TestApplyRequiredRefuses(t *testing.T) {
	t.Parallel()

	missing, err := applyRequired(recordsDoc(), []requiredAt{{"POST /missing body .", []string{"x"}}})
	if err != nil || len(missing) != 1 {
		t.Errorf("a place the document does not have = %q, %v; want it reported", missing, err)
	}

	if _, err := applyRequired(recordsDoc(), []requiredAt{{"GET /session 200 .", []string{"missing"}}}); err == nil || !strings.Contains(err.Error(), "GET /session 200 .") {
		t.Errorf("a field the record does not have: %v, want an error naming the place", err)
	}
}

// A field kept as given is marked nullable, for the importer to hold it by pointer; a field the record does not have is an error.
func TestApplyKeptAsGiven(t *testing.T) {
	t.Parallel()

	doc := recordsDoc()
	doc.Components.Schemas["Session"].Properties["time"] = &schema{Type: "number"}
	if missing, err := applyNullable(doc, []requiredAt{{"Session .", []string{"time"}}, {"Missing .", []string{"x"}}}); err != nil || len(missing) != 1 {
		t.Fatalf("applyNullable = %q, %v; want the missing record reported", missing, err)
	}
	if !doc.Components.Schemas["Session"].Properties["time"].Nullable {
		t.Error("the field kept as given: want it nullable")
	}

	if _, err := applyNullable(recordsDoc(), []requiredAt{{"Session .", []string{"missing"}}}); err == nil || !strings.Contains(err.Error(), "Session .") {
		t.Errorf("a field the record does not have: %v, want an error naming the place", err)
	}
}

// A stated query parameter is added to its route once, beside those the scan found, and a route the document does not have is reported.
func TestApplyQueries(t *testing.T) {
	t.Parallel()

	doc := &document{Paths: map[string]map[string]*operation{"/merge": {"post": {Parameters: []parameter{{Name: "codec", In: "query", Schema: &schema{Type: "string"}}}}}}}
	missing := applyQueries(doc, []queryAt{{"POST /merge", []string{"bitrate", "codec"}}, {"GET /missing", []string{"x"}}})
	if len(missing) != 1 || missing[0] != "GET /missing" {
		t.Errorf("missing = %q, want the route the document does not have", missing)
	}
	params := doc.Paths["/merge"]["post"].Parameters
	names := make([]string, 0, len(params))
	for _, p := range params {
		names = append(names, p.Name)
	}
	if !slices.Equal(names, []string{"codec", "bitrate"}) {
		t.Errorf("parameters = %q, want codec as it was and bitrate added", names)
	}
}

// A route stated as text answers text at 200 whatever the scan found there, and leaves its other answers be; a route the document does not
// have, or one with no 200, is reported.
func TestApplyText(t *testing.T) {
	t.Parallel()

	answer := map[string]media{"application/json": {Schema: &schema{Type: "object"}}}
	doc := &document{Paths: map[string]map[string]*operation{
		"/password": {"patch": {Responses: map[string]response{"200": {Description: "JSON", Content: answer}, "400": {Description: "refused"}}}},
		"/empty":    {"patch": {Responses: map[string]response{"204": {Description: "nothing"}}}},
	}}
	if missing := applyText(doc, []string{"PATCH /password", "GET /missing", "PATCH /empty"}); !slices.Equal(missing, []string{"GET /missing", "PATCH /empty"}) {
		t.Errorf("missing = %q, want the route the document lacks and the one with no 200", missing)
	}

	op := doc.Paths["/password"]["patch"]
	if text, ok := op.Responses["200"].Content["text/plain"]; !ok || len(op.Responses["200"].Content) != 1 || text.Schema.Type != "string" {
		t.Errorf("200 = %+v, want text alone", op.Responses["200"])
	}
	if op.Responses["400"].Description != "refused" {
		t.Errorf("400 = %+v, want it as it was", op.Responses["400"])
	}
}

// A ranged route gets a Range header once and a 206 carrying what its 200 does; a route the document does not have, or one with no 200, is
// reported.
func TestApplyRanges(t *testing.T) {
	t.Parallel()

	file := map[string]media{"application/octet-stream": {Schema: &schema{Type: "string", Format: "binary"}}}
	doc := &document{Paths: map[string]map[string]*operation{
		"/file":  {"get": {Responses: map[string]response{"200": {Description: "a file", Content: file}}}},
		"/empty": {"get": {Responses: map[string]response{"204": {Description: "nothing"}}}},
	}}
	all := []string{"GET /file", "GET /missing", "GET /empty"}
	if missing := applyRanges(doc, all); !slices.Equal(missing, []string{"GET /missing", "GET /empty"}) {
		t.Errorf("missing = %q, want the route the document lacks and the one with no file", missing)
	}
	applyRanges(doc, all)

	op := doc.Paths["/file"]["get"]
	if len(op.Parameters) != 1 || op.Parameters[0].Name != "Range" || op.Parameters[0].In != "header" {
		t.Errorf("parameters = %+v, want one Range header however many times it is applied", op.Parameters)
	}
	if part, ok := op.Responses["206"]; !ok || part.Content["application/octet-stream"].Schema != file["application/octet-stream"].Schema {
		t.Errorf("206 = %+v, want the 200's file", part)
	}
}
