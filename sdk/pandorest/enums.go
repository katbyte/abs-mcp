package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// enumName names the fixed set of values a field or parameter takes, found by its name and one of its values, so a set of another field of
// the same name is not given it.
type enumName struct {
	field, value, name string
}

// enumNames name every fixed set the scan finds; one the table does not name stops the scan, so each type of constants is named on purpose.
var enumNames = []enumName{
	{field: "id", value: "continue-listening", name: "ShelfID"},
	{field: "labelStringKey", value: "LabelContinueListening", name: "ShelfLabelKey"},
	{field: "type", value: "series", name: "ShelfType"},
	{field: "name", value: "onBackupCompleted", name: "NotificationEvent"},
	{field: "descriptionKey", value: "NotificationOnBackupCompletedDescription", name: "NotificationEventDescriptionKey"},
	{field: "stringKey", value: "MessageInvalidAsin", name: "ChapterSearchErrorKey"},
	{field: "sort", value: "timeListening", name: "SessionSort"},
	{field: "mediaItemType", value: "podcastEpisode", name: "MediaItemType"},
}

// nameEnums writes each fixed set of values in the document as a record of its own, named by the table, and points every place that takes
// it there, so the generated package holds it as a type with its values as constants. A set met in several places is one record holding every
// value any of them takes.
func nameEnums(doc *document, names []enumName) error {
	sets := map[string]*schema{}
	var unnamed []string

	name := func(field string, s *schema, where string) {
		if len(s.Enum) == 0 {
			return
		}

		i := slices.IndexFunc(names, func(n enumName) bool { return n.field == field && slices.Contains(s.Enum, n.value) })
		if i < 0 {
			unnamed = append(unnamed, fmt.Sprintf("%s.%s %q", where, field, s.Enum))
			return
		}

		n := names[i].name
		set := sets[n]
		if set == nil {
			set = &schema{Type: "string"}
			sets[n] = set
		}
		for _, v := range s.Enum {
			if !slices.Contains(set.Enum, v) {
				set.Enum = append(set.Enum, v)
			}
		}
		*s = schema{Ref: "#/components/schemas/" + n, Nullable: s.Nullable}
	}

	var walk func(s *schema, where string)
	walk = func(s *schema, where string) {
		if s == nil {
			return
		}
		for _, f := range slices.Sorted(maps.Keys(s.Properties)) {
			name(f, s.Properties[f], where)
			walk(s.Properties[f], where+"."+f)
		}
		walk(s.Items, where+"[]")
		walk(s.Values, where+"{}")
		for _, o := range s.OneOf {
			walk(o, where)
		}
	}

	for _, n := range slices.Sorted(maps.Keys(doc.Components.Schemas)) {
		walk(doc.Components.Schemas[n], n)
	}
	for _, path := range slices.Sorted(maps.Keys(doc.Paths)) {
		for _, method := range slices.Sorted(maps.Keys(doc.Paths[path])) {
			op, where := doc.Paths[path][method], strings.ToUpper(method)+" "+path
			for i := range op.Parameters {
				name(op.Parameters[i].Name, op.Parameters[i].Schema, where)
			}
			if op.RequestBody != nil {
				for _, m := range op.RequestBody.Content {
					walk(m.Schema, where+" body")
				}
			}
			for _, code := range slices.Sorted(maps.Keys(op.Responses)) {
				for _, m := range op.Responses[code].Content {
					walk(m.Schema, where+" "+code)
				}
			}
		}
	}

	if len(unnamed) > 0 {
		return fmt.Errorf("fixed sets of values with no name in enumNames, which each needs: %s", strings.Join(unnamed, "; "))
	}
	for n, set := range sets {
		if doc.Components.Schemas[n] != nil {
			return fmt.Errorf("the set of values named %s has the name of a record the document already has", n)
		}
		slices.Sort(set.Enum)
		doc.Components.Schemas[n] = set
	}

	return nil
}
