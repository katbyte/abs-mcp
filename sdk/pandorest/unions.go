package main

import (
	"fmt"
	"slices"
	"strings"
)

// union is an answer that is a list of records of several kinds, told apart by one of their fields, which the scan sees merged into one: the
// kinds differ in what one list field holds, and the field that tells them apart takes a value the source does not write out.
type union struct {
	// name is the record the union is written as
	name         string
	method, path string
	// field tells the kinds apart, and list is the field whose items differ between them
	field, list string
	members     []unionMember
}

// unionMember is one kind: the record it is written as, the values of the field that name it, and the record its list holds.
type unionMember struct {
	name   string
	kinds  []string
	record string
}

// unions are stated here from the source, as the scan cannot tell the kinds apart, and the document says each as a oneOf named by its field,
// so a client asks for the record a value says it is.
var unions = []union{{
	// LibraryItem.getPersonalizedShelves: book, episode and podcast shelves hold library items, a podcast library's recently added shelf typed
	// by the library's media type; series shelves hold series with their books, and author shelves authors with a count of their books
	name: "Shelf", method: "get", path: "/api/libraries/{id}/personalized", field: "type", list: "entities",
	members: []unionMember{
		{name: "ItemShelf", kinds: []string{"book", "episode", "podcast"}, record: "LibraryItemMinified"},
		{name: "SeriesShelf", kinds: []string{"series"}, record: "Series"},
		{name: "AuthorShelf", kinds: []string{"authors"}, record: "Author"},
	},
}}

// discriminator is the field the records of a oneOf name themselves by, and the record each of its values is.
type discriminator struct {
	PropertyName string            `json:"propertyName"`
	Mapping      map[string]string `json:"mapping,omitempty"`
}

// applyUnions writes each union into the document as a record of its name, one of a record per kind: the scanned record with the field that
// names it and the list it holds. The answer's items are the union. It returns the unions whose operation the document does not have, for the
// report; an answer no longer of the shape stated is an error, so the statement is mended when the source changes.
func applyUnions(doc *document, all []union) (missing []string, err error) {
	for _, u := range all {
		op := doc.Paths[u.path][u.method]
		if op == nil {
			missing = append(missing, strings.ToUpper(u.method)+" "+u.path)
			continue
		}
		m, ok := op.Responses["200"].Content["application/json"]
		if !ok || m.Schema == nil || m.Schema.Type != "array" || m.Schema.Items == nil || m.Schema.Items.Properties[u.field] == nil || m.Schema.Items.Properties[u.list] == nil {
			return nil, fmt.Errorf("union %s %s: the answer is no longer a list of records with %s and %s", u.method, u.path, u.field, u.list)
		}

		// the field takes every kind there is, so it is one type of constants; which kind is which record is the mapping's to say
		var kinds []string
		for _, mem := range u.members {
			kinds = append(kinds, mem.kinds...)
		}
		slices.Sort(kinds)

		oneOf := &schema{Discriminator: &discriminator{PropertyName: u.field, Mapping: map[string]string{}}}
		for _, mem := range u.members {
			if doc.Components.Schemas[mem.record] == nil {
				return nil, fmt.Errorf("union %s %s: %s holds %s, which the document does not have", u.method, u.path, mem.name, mem.record)
			}
			if doc.Components.Schemas[mem.name] != nil || mem.name == u.name {
				return nil, fmt.Errorf("union %s %s: the document already has a record named %s", u.method, u.path, mem.name)
			}

			s := clone(m.Schema.Items)
			s.Properties[u.field] = &schema{Type: "string", Enum: kinds}
			s.Properties[u.list] = &schema{Type: "array", Items: &schema{Ref: "#/components/schemas/" + mem.record}}
			doc.Components.Schemas[mem.name] = s

			oneOf.OneOf = append(oneOf.OneOf, &schema{Ref: "#/components/schemas/" + mem.name})
			for _, k := range mem.kinds {
				oneOf.Discriminator.Mapping[k] = "#/components/schemas/" + mem.name
			}
		}
		if doc.Components.Schemas[u.name] != nil {
			return nil, fmt.Errorf("union %s %s: the document already has a record named %s", u.method, u.path, u.name)
		}
		doc.Components.Schemas[u.name] = oneOf
		m.Schema.Items = &schema{Ref: "#/components/schemas/" + u.name}
	}

	return missing, nil
}
