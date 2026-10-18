package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/abs-mcp/sdk/pandorest/answers"
)

// answerFill types the document from what a real server was sent and answered: it fills what the source leaves untyped, and corrects what the
// answers prove wrong, such as a whole number that came back with a fraction, a value that came back null, or a field the document did not have.
type answerFill struct {
	files      int
	filled     int
	ops        map[string]bool
	corrected  []string
	conflicts  []string
	unmatched  []string
	visitedRef map[string]bool
}

func newAnswerFill() *answerFill {
	return &answerFill{ops: map[string]bool{}, visitedRef: map[string]bool{}}
}

// fill merges every file's samples into its operation's request body and answers.
func (a *answerFill) fill(doc *document, files []*answers.File) {
	a.files = len(files)

	for _, f := range files {
		key := f.Method + " " + f.Path
		op := doc.Paths[f.Path][strings.ToLower(f.Method)]
		if op == nil {
			a.unmatched = append(a.unmatched, key+" ("+f.Operation+")")
			continue
		}

		before := a.filled + len(a.corrected)
		if len(f.Sent) > 0 && op.RequestBody != nil {
			if m, ok := op.RequestBody.Content["application/json"]; ok {
				a.merge(m.Schema, inferAll(f.Sent), doc.Components.Schemas, key+" body", place{sent: true})
			}
		}

		for _, code := range slices.Sorted(maps.Keys(f.Answers)) {
			m, ok := op.Responses[code].Content["application/json"]
			if !ok || m.Schema == nil {
				a.conflicts = append(a.conflicts, fmt.Sprintf("%s: the server answered %s with JSON, which the document does not say", key, code))
				continue
			}
			a.merge(m.Schema, inferAll(f.Answers[code]), doc.Components.Schemas, key+" answer", place{})
			for _, v := range f.Answers[code] {
				a.throughUnions(m.Schema, v, doc.Components.Schemas, key+" answer")
			}
		}

		if a.filled+len(a.corrected) > before {
			a.ops[key] = true
		}
	}
}

// infer is the schema of one decoded value, "integer" for a whole number, and untyped but nullable for null.
func infer(v any) *schema {
	switch v := v.(type) {
	case nil:
		return &schema{Nullable: true}
	case bool:
		return &schema{Type: "boolean"}
	case json.Number:
		if answers.Whole(v) {
			return &schema{Type: "integer"}
		}
		return &schema{Type: "number"}
	case string:
		return &schema{Type: "string"}
	case []any:
		// an empty list is a list of something no answer has shown
		return &schema{Type: "array", Items: cmp.Or(inferAll(v), &schema{})}
	case map[string]any:
		// a map keyed by data, such as listening time by day, holds what every key holds
		if answers.Keyed(v) {
			out := &schema{Type: "object"}
			for _, k := range slices.Sorted(maps.Keys(v)) {
				out.Values = unite(out.Values, infer(v[k]))
			}
			return out
		}
		s := &schema{Type: "object", Properties: map[string]*schema{}}
		for k, x := range v {
			s.Properties[k] = infer(x)
		}
		return s
	}

	return &schema{}
}

// inferAll is the schema every one of the values fits, nil for none.
func inferAll(values []any) *schema {
	var out *schema
	for _, v := range values {
		out = unite(out, infer(v))
	}

	return out
}

// unite is the schema two samples of one place both fit: a field either holds is kept, a whole number and a fraction are a number, a null makes
// it nullable, two kinds that do not go together leave it untyped, and a record referred to stays referred to.
func unite(a, b *schema) *schema {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case a.Ref != "":
		return clone(a)
	case b.Ref != "":
		return clone(b)
	}

	nullable := a.Nullable || b.Nullable
	var out *schema
	switch {
	case a.Type == "":
		out = clone(b)
	case b.Type == "":
		out = clone(a)
	case a.Type == "object" && b.Type == "object":
		// a record a stated place made where the scan saw nothing of it stays one the answers may fill
		out = &schema{Type: "object", AdditionalProperties: a.AdditionalProperties || b.AdditionalProperties, Properties: map[string]*schema{}, unseen: a.unseen || b.unseen}
		for _, n := range slices.Sorted(maps.Keys(a.Properties)) {
			out.Properties[n] = unite(a.Properties[n], b.Properties[n])
		}
		for _, n := range slices.Sorted(maps.Keys(b.Properties)) {
			if _, ok := out.Properties[n]; !ok {
				out.Properties[n] = clone(b.Properties[n])
			}
		}
		out.Values = unite(clone(a.Values), clone(b.Values))
	case a.Type == "array" && b.Type == "array":
		out = &schema{Type: "array", Items: unite(a.Items, b.Items)}
	case a.Type == b.Type:
		out = clone(a)
	case a.Type == "number" && b.Type == "integer", a.Type == "integer" && b.Type == "number":
		out = &schema{Type: "number"}
	default:
		out = &schema{}
	}
	out.Nullable = nullable

	return out
}

// loosen makes every whole number of a schema read from answers a number marked as seen whole, since a count in a small test library does not
// prove a value is never a fraction, and marks every record as sampled, since a test library does not send every field there is. The
// hand-written client may settle both.
func loosen(s *schema) *schema {
	if s == nil {
		return nil
	}
	switch s.Type {
	case "integer":
		s.Type, s.whole = "number", true
	case "object":
		s.sampled = true
	}
	loosen(s.Items)
	loosen(s.Values)
	for _, p := range s.Properties {
		loosen(p)
	}

	return s
}

// place is where in the document a merge stands: in what a client sent, which only shows what one client chose to send, or in what the server
// answered. A record answered in place of another kind is no worry here: a value of several kinds is a stated union, which the merge leaves be.
type place struct {
	sent bool
}

// merge types the untyped places of to from what was sent or answered, and, in an answer, corrects to where the server sent what it says cannot
// be. What an answer shows that may not be a mistake, and what a client sent beyond the document, is listed for a person to judge.
func (a *answerFill) merge(to, from *schema, components map[string]*schema, where string, at place) {
	if to == nil || from == nil {
		return
	}

	if to.Ref != "" {
		name := strings.TrimPrefix(to.Ref, "#/components/schemas/")
		key := name + "\x00" + fingerprint(from) + strconv.FormatBool(from.Nullable) + strconv.FormatBool(at.sent)
		if a.visitedRef[key] {
			return
		}
		a.visitedRef[key] = true
		if from.Nullable && !to.Nullable && !at.sent {
			to.Nullable = true
			a.corrected = append(a.corrected, where+": the server sent null")
		}
		a.merge(components[name], from, components, name, place{sent: at.sent})
		return
	}
	// a value of several kinds is stated, and each kind's record is filled where it is used on its own: the answers here are of every kind at
	// once
	if len(to.OneOf) > 0 {
		return
	}

	if from.Nullable && !to.Nullable && !at.sent {
		to.Nullable = true
		a.corrected = append(a.corrected, where+": the server sent null")
	}
	if from.Type == "" {
		return
	}

	if untyped(to) {
		nullable := to.Nullable
		*to = *loosen(clone(from))
		to.Nullable = to.Nullable || nullable
		a.filled++
		return
	}

	switch {
	case to.Type == "object" && from.Type == "object" && to.Values != nil:
		// a map's keys are data, so what each holds fills what every key holds, whether or not the answer's keys looked like data
		a.merge(to.Values, from.Values, components, where+"{}", at)
		for _, n := range slices.Sorted(maps.Keys(from.Properties)) {
			a.merge(to.Values, from.Properties[n], components, where+"{}", at)
		}

	case to.Type == "object" && from.Type == "object" && from.Values != nil:
		// a map the source writes as {} without filling it where the scan can see
		if len(to.Properties) == 0 {
			to.Values = loosen(clone(from.Values))
			a.filled++
		}

	case to.Type == "object" && from.Type == "object":
		// a field of a record the source leaves open, or of a body it hands on whole, is filled in, and one the source's record lacks is a
		// correction, except where it is only what a client chose to send
		open := to.AdditionalProperties || len(to.Properties) == 0 || to.unseen
		for _, n := range slices.Sorted(maps.Keys(from.Properties)) {
			if p, ok := to.Properties[n]; ok {
				a.merge(p, from.Properties[n], components, where+"."+n, place{sent: at.sent})
				continue
			}

			if at.sent && !open {
				a.conflicts = append(a.conflicts, where+"."+n+": the client sends it, and the source does not show the server reading it")
				continue
			}

			if to.Properties == nil {
				to.Properties = map[string]*schema{}
			}
			to.Properties[n] = loosen(clone(from.Properties[n]))
			if open {
				a.filled++
			} else {
				a.corrected = append(a.corrected, where+"."+n+": the server sends it, and the document did not have it")
			}
		}

	case to.Type == "array" && from.Type == "array":
		a.merge(to.Items, from.Items, components, where+"[]", place{sent: at.sent})

	case to.Type == "integer" && from.Type == "number" && !at.sent:
		to.Type = "number"
		a.corrected = append(a.corrected, where+": the server sent a fraction")

	case to.Type == "number" && from.Type == "integer":
		// a number that came back whole is still a number

	case to.Type == "number" && from.Type == "number":
		// and one seen whole elsewhere that came back with a fraction is not a whole number, whatever its name
		to.whole = false

	case to.Type != from.Type:
		who := "the server sent"
		if at.sent {
			who = "the client sends"
		}
		a.conflicts = append(a.conflicts, fmt.Sprintf("%s: the source says %s, %s %s", where, to.Type, who, from.Type))
	}
}

// throughUnions merges each record an answer holds where the document has a union into the member the record's field names, which the merge
// of every answer at once cannot do, as it sees the kinds together. A kind the union has no member for is listed.
func (a *answerFill) throughUnions(to *schema, v any, components map[string]*schema, where string) {
	if to == nil || v == nil {
		return
	}
	if to.Ref != "" {
		where = strings.TrimPrefix(to.Ref, "#/components/schemas/")
		to = components[where]
		if to == nil {
			return
		}
	}

	switch v := v.(type) {
	case map[string]any:
		if len(to.OneOf) > 0 && to.Discriminator != nil {
			kind, isText := v[to.Discriminator.PropertyName].(string)
			ref, ok := to.Discriminator.Mapping[kind]
			if !isText || !ok {
				a.conflicts = append(a.conflicts, fmt.Sprintf("%s: the server sent a %s of %q, which the union has no record for", where, to.Discriminator.PropertyName, kind))
				return
			}
			member := &schema{Ref: ref}
			a.merge(member, infer(v), components, where, place{})
			a.throughUnions(member, v, components, where)
			return
		}
		for _, n := range slices.Sorted(maps.Keys(to.Properties)) {
			a.throughUnions(to.Properties[n], v[n], components, where+"."+n)
		}

	case []any:
		for _, item := range v {
			a.throughUnions(to.Items, item, components, where+"[]")
		}
	}
}

// write reports what the answers filled and corrected, and with -v each correction and each place they disagree with the source.
func (a *answerFill) write(w io.Writer, dir string, verbose bool) {
	_, _ = fmt.Fprintf(w, "  answers from %s   %d files: filled %d values in %d operations, corrected %d; %d differ from the source and are left as it says; %d match no operation\n", dir, a.files, a.filled, len(a.ops), len(a.corrected), len(a.conflicts), len(a.unmatched))

	if verbose {
		for _, c := range a.corrected {
			_, _ = fmt.Fprintln(w, "  corrected:", c)
		}
		for _, c := range a.conflicts {
			_, _ = fmt.Fprintln(w, "  differs:", c)
		}
		for _, u := range a.unmatched {
			_, _ = fmt.Fprintln(w, "  no operation for", u)
		}
	}
}
