package main

import (
	"fmt"
	"strings"
)

// goTypeAt is a place the server writes in a form the document cannot describe, and the type written by hand in sdk/abs that reads it.
type goTypeAt struct {
	// at is a place as recordsAt names one: a record, or an operation and status, then a path into it
	at string
	// goType is the type as the package writes it, $item standing for a list's own element, as pandorest names it
	goType string
}

// goTypesAt are the places held as a type of the package's own.
var goTypesAt = []goTypeAt{
	// MiscController.getLoggerData: logs.slice(-5000) || '', the day's lines, or empty text before the first
	{"GET /api/logger-data 200 .currentDailyLogs", "ListOrEmpty[$item]"},
	// FantLab.cleanBookData: publishedYear: work_year, the number FantLab sends, where every other provider writes text
	{"GET /api/search/books 200 .[].publishedYear", "TextOrNumber"},
}

// goType is a type of the generated package's own, which the importer holds a value as in place of what its schema describes.
type goType struct {
	Name string `json:"name"`
}

// applyGoTypes marks each place with its type, and returns the places whose operation the document does not have, for the report. A place
// inside them that is not there is an error, so the statement is mended when the source changes.
func applyGoTypes(doc *document, all []goTypeAt) (missing []string, err error) {
	for _, g := range all {
		root, path, err := placeRoot(doc, g.at)
		if err != nil {
			return nil, err
		}
		if root == nil {
			missing = append(missing, g.at)
			continue
		}

		s, err := schemaAt(*root, path)
		if err != nil {
			return nil, fmt.Errorf("type at %s: %w", g.at, err)
		}
		s.GoType = &goType{Name: g.goType}
	}

	return missing, nil
}

// schemaAt is the schema at the path under s, following fields by name and a list's items by [], without making anything.
func schemaAt(s *schema, path string) (*schema, error) {
	if path == "" {
		return s, nil
	}

	for step := range strings.SplitSeq(path, ".") {
		name, list := strings.CutSuffix(step, "[]")
		name, keyed := strings.CutSuffix(name, "{}")
		if name != "" {
			next := s.Properties[name]
			if next == nil {
				return nil, fmt.Errorf("the record has no %s", name)
			}
			s = next
		}
		if keyed {
			if s.Values == nil {
				return nil, fmt.Errorf("%s is not a map", step)
			}
			s = s.Values
		}
		if list {
			if s.Items == nil {
				return nil, fmt.Errorf("%s is not a list", step)
			}
			s = s.Items
		}
	}

	return s, nil
}
