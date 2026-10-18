package main

import (
	"maps"
	"slices"
	"strings"
)

// untyped is whether a schema says nothing of what it holds.
func untyped(s *schema) bool {
	return s.Type == "" && s.Ref == "" && len(s.Properties) == 0 && s.Items == nil && len(s.OneOf) == 0
}

// clone is a copy of a schema deep enough to change without touching the one it was copied from.
func clone(s *schema) *schema {
	if s == nil {
		return nil
	}

	out := *s
	out.Items = clone(s.Items)
	out.Values = clone(s.Values)
	out.Enum = slices.Clone(s.Enum)
	out.OneOf = nil
	for _, o := range s.OneOf {
		out.OneOf = append(out.OneOf, clone(o))
	}
	if s.Properties != nil {
		out.Properties = make(map[string]*schema, len(s.Properties))
		for n, p := range s.Properties {
			out.Properties[n] = clone(p)
		}
	}

	return &out
}

// fingerprint tells two schemas apart, so a record is filled once from each shape the server answers it in: a field sent null in one answer
// is another shape from the same field sent with a value in another.
func fingerprint(s *schema) string {
	if s == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(s.Type + s.Ref)
	if s.Nullable {
		sb.WriteString("?")
	}
	for _, n := range slices.Sorted(maps.Keys(s.Properties)) {
		sb.WriteString("," + n + ":" + fingerprint(s.Properties[n]))
	}
	if s.Items != nil {
		sb.WriteString("[" + fingerprint(s.Items) + "]")
	}
	if s.Values != nil {
		sb.WriteString("{" + fingerprint(s.Values) + "}")
	}

	return sb.String()
}

// write reports what the client filled, and with -v the requests it makes that no operation of the document has.
