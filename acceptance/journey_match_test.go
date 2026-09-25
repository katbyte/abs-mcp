//go:build integration

// Journeys through matching: a book matched the way a collector decides one,
// field by field, and an asin that lands on the wrong book and is taken off
// again. What the match tools report is what they asked the server for, so
// every step reads the book back and holds it against the provider's record.
package acceptance

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// foundationASIN is the asin the Messy Foundation carries: the recording
// Audible's own search returns for it, so its record is on a cassette.
const foundationASIN = "B003D8W5VS"

// storeTag is the provider tag a match from the US store records.
const storeTag = "zz-provider:audible"

// matchFields are the item_get fields a match can write, compared to tell
// what a match changed and to check that a put back left nothing behind.
var matchFields = []string{"title", "subtitle", "author", "narrator", "series", "year", "publisher", "genres", "tags", "language", "asin", "isbn", "description", "abridged", "explicit", "no_cover"}

// bookNow reads the fields a match can write, and the author records.
func bookNow(t *testing.T, id string) map[string]any {
	t.Helper()

	got := call(t, "item_get", map[string]any{"item": id})
	out := map[string]any{"authors": got["authors"]}
	for _, k := range matchFields {
		out[k] = got[k]
	}
	return out
}

// bookChanges lists the fields two readings of a book disagree on, all of
// matchFields unless some are named.
func bookChanges(before, after map[string]any, fields ...string) []string {
	if len(fields) == 0 {
		fields = matchFields
	}
	var out []string
	for _, k := range fields {
		if fmt.Sprint(before[k]) != fmt.Sprint(after[k]) {
			out = append(out, fmt.Sprintf("%s: %v -> %v", k, before[k], after[k]))
		}
	}
	return out
}

// listOrNone reads a list field that item_get leaves out when it is empty.
func listOrNone(t *testing.T, v any, field string) []string {
	t.Helper()

	if v == nil {
		return nil
	}
	return strs(t, v, field)
}

// putBackBook writes a book back to a reading taken before a journey matched
// over it: every field a match can write, the empty ones cleared, the cover
// taken off when it had none. A match fills what was empty and leaves a
// series, tags and an asin behind, and any of those left over changes what
// every later test sees, so the book is read again and must equal the reading.
func putBackBook(t *testing.T, id string, before map[string]any) {
	t.Helper()

	args := map[string]any{"item": id, "title": before["title"], "abridged": before["abridged"] == true, "explicit": before["explicit"] == true}
	var clear []any
	for _, k := range []string{"subtitle", "year", "publisher", "description", "isbn", "asin", "language"} {
		if s := text(before[k]); s != "" {
			args[k] = s
		} else {
			clear = append(clear, k)
		}
	}
	lists := map[string][]string{
		"series": listOrNone(t, before["series"], "series"), "genres": listOrNone(t, before["genres"], "genres"),
		"tags": listOrNone(t, before["tags"], "tags"),
	}
	if narrator := text(before["narrator"]); narrator != "" {
		lists["narrators"] = strings.Split(narrator, ", ")
	}
	for _, k := range []string{"series", "genres", "tags", "narrators"} {
		if len(lists[k]) > 0 {
			args[k] = toAny(lists[k])
		} else {
			clear = append(clear, k)
		}
	}
	var authors []any
	for _, a := range rows(t, before["authors"], "authors") {
		authors = append(authors, a["name"])
	}
	args["authors"] = authors
	args["clear"] = clear
	call(t, "item_edit", args)
	if before["no_cover"] == true && bookNow(t, id)["no_cover"] != true {
		call(t, "item_cover_edit", map[string]any{"item": id, "remove": true})
	}
	if changed := bookChanges(before, bookNow(t, id)); len(changed) > 0 {
		t.Errorf("the book was not put back:\n  %s", strings.Join(changed, "\n  "))
	}
}

// decisionsByField indexes a smart match's decisions by the field decided.
func decisionsByField(t *testing.T, v any) map[string]map[string]any {
	t.Helper()

	out := map[string]map[string]any{}
	for _, d := range rows(t, v, "fields") {
		out[text(d["field"])] = d
	}
	return out
}

// decidedValue is a field of a reading as a smart decision spells it: lists
// joined with commas, the people fields by their display names.
func decidedValue(t *testing.T, book map[string]any, field string) string {
	t.Helper()

	switch field {
	case "authors":
		return text(book["author"])
	case "narrators":
		return text(book["narrator"])
	case "series", "genres":
		return strings.Join(listOrNone(t, book[field], field), ", ")
	}
	return text(book[field])
}

// A book matched the way a collector matches one: scored against the store
// first, the smart match previewed to see each field's fate, applied, read
// back field by field against the store's record, applied again to see that
// nothing moves the second time, matched once more over file-tag junk so the
// rules that write have something to write, then overridden with the curated
// fields kept (two of them, then every one), re-tagged with the store, and
// finally matched to an asin no store has. It catches a smart rule that
// writes what it said it would keep or keeps what it said it would write, a
// kept field that does not survive an override, a provider tag written over
// the tags a match had just filled, beside another store's, or moved about in
// a list that already had it, a second match that reports a change it did
// not make, and a match that found nothing leaving a tag behind as if it had.
func TestJourneyMatchDecidedFieldByField(t *testing.T) {
	requireProviders(t)

	id := messyID(t, "Isaac Asimov/Foundation")
	before := bookNow(t, id)
	if before["asin"] != foundationASIN || before["tags"] != nil || before["no_cover"] != true {
		t.Fatalf("the Messy Foundation is not as seeded: %v", before)
	}
	t.Cleanup(func() {
		putBackBook(t, id, before)
		// the series the match filled, and the one kept over it, go with their
		// last book, so the next test finds neither in Messy to trip over
		for _, name := range []string{"Foundation", "Zzyzx Kept Saga"} {
			if msg := callErr(t, "series_get", map[string]any{"library": "Messy", "series": name}); !strings.Contains(msg, "no series named") {
				t.Errorf("%s outlived the put back: %s", name, msg)
			}
		}
	})

	// the batch scores the book against the store: its own recording is
	// among the candidates, the same book read by the same reader, and only
	// the length of a one-second file says it is another recording
	batch := call(t, "item_match_batch", map[string]any{
		"library": "Messy", "filter": "authors:Isaac Asimov", "providers": []any{"audible"},
		"candidates": 10, "tolerance": 0.05, "limit": 5,
	})
	if total := num(t, batch["total"], "total"); total != 1 {
		t.Fatalf("total = %d, want the one Asimov book in Messy", total)
	}
	scoredRows := rows(t, batch["rows"], "rows")
	if len(scoredRows) != 1 || scoredRows[0]["id"] != id || scoredRows[0]["provider"] != "audible" {
		t.Fatalf("rows = %v, want Foundation scored at audible", scoredRows)
	}
	var candidates []map[string]any
	if best, ok := scoredRows[0]["best"].(map[string]any); ok {
		candidates = append(candidates, best)
	}
	if scoredRows[0]["others"] != nil {
		candidates = append(candidates, rows(t, scoredRows[0]["others"], "others")...)
	}
	if len(candidates) < 2 {
		t.Errorf("candidates = %d, want more than the best when ten are asked for", len(candidates))
	}
	own := slices.IndexFunc(candidates, func(c map[string]any) bool { return c["asin"] == foundationASIN })
	if own < 0 {
		t.Fatalf("the store's own recording is not among the candidates: %v", candidates)
	}
	if c := candidates[own]; c["confidence"] != "edition" || !strings.Contains(text(c["reason"]), "narrator same") || !strings.Contains(text(c["reason"]), "title same") {
		t.Errorf("the book's own recording scored %v (%v), want an edition: the title and reader agree, the length does not", c["confidence"], c["reason"])
	}

	// a misspelt store and a tolerance that makes every edition exact are
	// refused by name, before anything is asked of a provider
	if msg := callErr(t, "item_match_batch", withMessy(map[string]any{"filter": "authors:Isaac Asimov", "providers": []any{"audible.cq"}})); !strings.Contains(msg, `no provider "audible.cq"`) {
		t.Errorf("a misspelt store: %s", msg)
	}
	if msg := callErr(t, "item_match_batch", withMessy(map[string]any{"filter": "authors:Isaac Asimov", "tolerance": 0.5})); !strings.Contains(msg, "tolerance 0.5") {
		t.Errorf("a tolerance past a tenth: %s", msg)
	}
	if msg := callErr(t, "item_match_apply", map[string]any{"item": id, "provider": "audibel", "asin": foundationASIN, "smart": true, "preview": true}); !strings.Contains(msg, `no provider "audibel"`) {
		t.Errorf("a misspelt store on the apply: %s", msg)
	}

	// a lookup takes part of a title; a write needs the whole of it, and
	// says which book the part was nearest
	look := call(t, "item_match", map[string]any{"library": "Messy", "item": "Foundation", "provider": "audible", "title": "Foundation", "author": "Isaac Asimov", "limit": 3})
	if look["item"] != "Foundation (Unabridged)" || len(rows(t, look["candidates"], "candidates")) != 3 {
		t.Errorf("item_match by part of the title, limit 3: item %v, %d candidates", look["item"], len(rows(t, look["candidates"], "candidates")))
	}
	if msg := callErr(t, "item_match_apply", map[string]any{"library": "Messy", "item": "Foundation", "provider": "audible", "asin": foundationASIN, "smart": true, "preview": true}); !strings.Contains(msg, "Foundation (Unabridged)") || !strings.Contains(msg, "whole title") {
		t.Errorf("a write by part of a title: %s", msg)
	}

	// the preview: every field the store would write differently, and what
	// the rules do about it, with nothing changed
	smart := map[string]any{"library": "Messy", "item": "Foundation (Unabridged)", "provider": "audible", "asin": foundationASIN, "smart": true}
	preview := call(t, "item_match_apply", withArgs(smart, map[string]any{"preview": true}))
	decided := decisionsByField(t, preview["fields"])
	want := map[string]string{
		"title":     "written", // "(Unabridged)" is an importer's suffix
		"series":    "filled",
		"publisher": "filled",
		"year":      "filled",
		"genres":    "kept", // a real genre is the collector's
	}
	for field, action := range want {
		if d := decided[field]; d == nil || d["action"] != action {
			t.Errorf("%s: decided %v, want %s", field, d, action)
		}
	}
	for field, d := range decided {
		if _, ok := want[field]; !ok {
			t.Errorf("an unexpected decision on %s: %v", field, d)
		}
	}
	if changed := bookChanges(before, bookNow(t, id)); len(changed) > 0 {
		t.Errorf("the preview changed the book: %v", changed)
	}

	// applied for real: the decisions are the preview's, and the book holds
	// what each one said
	applied := call(t, "item_match_apply", smart)
	if updated, _ := applied["updated"].(bool); !updated {
		t.Errorf("updated = %v, want the match to have changed the book: %v", applied["updated"], applied)
	}
	if fmt.Sprint(applied["fields"]) != fmt.Sprint(preview["fields"]) {
		t.Errorf("the match decided\n  %v\nthe preview said\n  %v", applied["fields"], preview["fields"])
	}
	after := bookNow(t, id)
	for field, d := range decided {
		switch got := decidedValue(t, after, field); d["action"] {
		case "filled", "written":
			if got != text(d["provider"]) {
				t.Errorf("%s is %q, want the store's %q (%s)", field, got, d["provider"], d["action"])
			}
		case "kept":
			if was := decidedValue(t, before, field); got != was {
				t.Errorf("%s is %q, want it kept as %q", field, got, was)
			}
		}
	}
	if changed := bookChanges(before, after, "author", "narrator", "language", "description", "asin"); len(changed) > 0 {
		t.Errorf("fields no rule touched changed: %v", changed)
	}
	// the match filled the empty tag list from the store, and the provider
	// tag went onto that list rather than onto the empty one from before
	tags := listOrNone(t, after["tags"], "tags")
	if countOf(tags, storeTag) != 1 || len(tags) < 2 {
		t.Errorf("tags = %v, want the store's tags with %s once beside them", tags, storeTag)
	}
	if after["no_cover"] == true {
		t.Error("the match fetched no cover for a book that had none")
	}

	// the same match again changes nothing and says so; the preview and a
	// batch with rows that cannot be matched are counted by what happened
	again := call(t, "item_match_apply_batch", map[string]any{"matches": []any{map[string]any{"item": id, "asin": foundationASIN}}, "provider": "audible", "smart": true})
	if a, u, f := num(t, again["applied"], "applied"), num(t, again["unchanged"], "unchanged"), num(t, again["failed"], "failed"); a != 0 || u != 1 || f != 0 {
		t.Errorf("a second smart match: applied %d, unchanged %d, failed %d, want 0, 1, 0: %v", a, u, f, again)
	}
	for _, d := range rows(t, rows(t, again["results"], "results")[0]["fields"], "fields") {
		if d["action"] == "written" || d["action"] == "filled" {
			t.Errorf("the second match decided to write %v", d)
		}
	}
	if changed := bookChanges(after, bookNow(t, id)); len(changed) > 0 {
		t.Errorf("the second match changed the book: %v", changed)
	}
	pv := call(t, "item_match_apply_batch", map[string]any{"matches": []any{map[string]any{"item": id, "asin": foundationASIN}}, "provider": "audible", "smart": true, "preview": true})
	if p, a, u := num(t, pv["previewed"], "previewed"), num(t, pv["applied"], "applied"), num(t, pv["unchanged"], "unchanged"); p != 1 || a != 0 || u != 0 {
		t.Errorf("a batch preview: previewed %d, applied %d, unchanged %d, want 1, 0, 0", p, a, u)
	}
	mixed := call(t, "item_match_apply_batch", map[string]any{"provider": "audible", "matches": []any{
		map[string]any{"item": id, "asin": foundationASIN},
		map[string]any{"item": "Zzyzx Nowhere On The Shelf", "asin": foundationASIN},
		map[string]any{"item": id},
	}})
	if a, u, f := num(t, mixed["applied"], "applied"), num(t, mixed["unchanged"], "unchanged"), num(t, mixed["failed"], "failed"); a != 0 || u != 1 || f != 2 {
		t.Errorf("a batch of one unchanged book and two that cannot be matched: applied %d, unchanged %d, failed %d", a, u, f)
	}
	if results := rows(t, mixed["results"], "results"); len(results) == 3 {
		if msg := text(results[1]["error"]); !strings.Contains(msg, "no item titled") {
			t.Errorf("a title nothing has: %q", msg)
		}
		if msg := text(results[2]["error"]); msg != "no asin or isbn" {
			t.Errorf("a row with no asin: %q", msg)
		}
	}

	// the book as file tags leave one: a track number on the title, a company
	// for the reader, a placeholder genre, a timestamp for the year, a credit
	// line for a description, a clipped language, and a series number that
	// disagrees with the store's. Each rule writes, or holds for review
	smart = map[string]any{"item": id, "provider": "audible", "asin": foundationASIN, "smart": true}
	call(t, "item_edit", map[string]any{
		"item": id, "title": "01 Foundation", "narrators": []any{"Random House Audio"}, "genres": []any{"Audiobook"},
		"year": "2010-04-20", "description": "Read by Scott Brick", "language": "Eng", "publisher": "Zzyzx Press", "series": []any{"Foundation #1"},
	})
	fromFiles := bookNow(t, id)
	overTags := call(t, "item_match_apply", smart)
	decided = decisionsByField(t, overTags["fields"])
	for field, action := range map[string]string{
		"title": "written", "narrators": "written", "genres": "written", "year": "written",
		"description": "written", "language": "written", "publisher": "written", "series": "review",
	} {
		if d := decided[field]; d == nil || d["action"] != action {
			t.Errorf("%s over file tags: decided %v, want %s", field, d, action)
		}
	}
	fixed := bookNow(t, id)
	for field, d := range decided {
		got := decidedValue(t, fixed, field)
		switch {
		case field == "description":
			if got == text(fromFiles["description"]) || !strings.Contains(got, "Foundation") {
				t.Errorf("description = %q, want the store's in place of the credit line", got)
			}
		case d["action"] == "written" && got != text(d["provider"]):
			t.Errorf("%s = %q, want the store's %q", field, got, d["provider"])
		case d["action"] == "review" && got != decidedValue(t, fromFiles, field):
			t.Errorf("%s = %q, want it left for review as %q", field, got, decidedValue(t, fromFiles, field))
		}
	}
	if updated, _ := overTags["updated"].(bool); !updated {
		t.Errorf("updated = %v after writing seven fields", overTags["updated"])
	}

	// the collector's own reader, genre and description, then an override
	// that keeps the first two and takes the store's description
	call(t, "item_edit", map[string]any{
		"item": id, "narrators": []any{"Zzyzx Reader"}, "genres": []any{"Zzyzx Genre"},
		"description": "Zzyzx: the collector's own description of the book, long enough to be one rather than a stub, for the override to replace.",
	})
	curated := bookNow(t, id)
	override := map[string]any{"item": id, "provider": "audible", "asin": foundationASIN}
	if msg := callErr(t, "item_match_apply", withArgs(override, map[string]any{"keep": []any{"narrators"}})); !strings.Contains(msg, "keep only means something with override_details") {
		t.Errorf("keep without override_details: %s", msg)
	}
	if msg := callErr(t, "item_match_apply", withArgs(override, map[string]any{"override_details": true, "keep": []any{"narrator", "bogus"}})); !strings.Contains(msg, `unknown field "bogus"`) {
		t.Errorf("keep naming no field: %s", msg)
	}
	if msg := callErr(t, "item_match_apply", withArgs(override, map[string]any{"override_details": true, "smart": true})); !strings.Contains(msg, "pick one") {
		t.Errorf("smart with override_details: %s", msg)
	}
	over := call(t, "item_match_apply", withArgs(override, map[string]any{"override_details": true, "keep": []any{"narrator", "Genres"}}))
	if kept := listOrNone(t, over["kept"], "kept"); !slices.Equal(kept, []string{"narrators", "genres"}) {
		t.Errorf("kept = %v, want [narrators genres]", kept)
	}
	overridden := bookNow(t, id)
	if overridden["narrator"] != "Zzyzx Reader" || fmt.Sprint(overridden["genres"]) != "[Zzyzx Genre]" {
		t.Errorf("the kept fields did not survive the override: narrator %v, genres %v", overridden["narrator"], overridden["genres"])
	}
	if desc := text(overridden["description"]); desc == text(curated["description"]) || !strings.Contains(desc, "Foundation") {
		t.Errorf("description = %q, want the store's in place of the one that was there", desc)
	}
	if tags := listOrNone(t, overridden["tags"], "tags"); countOf(tags, storeTag) != 1 {
		t.Errorf("tags after the override = %v, want %s once", tags, storeTag)
	}
	// everything curated and everything kept: the override then changes none
	// of it, the empty subtitle included
	call(t, "item_edit", map[string]any{
		"item": id, "title": "Zzyzx Kept Title", "series": []any{"Zzyzx Kept Saga #7"}, "publisher": "Zzyzx Press",
		"year": "1999", "language": "Zzyzx", "add_tags": []any{"zzyzx-kept"},
		"description": "Zzyzx: a description the collector wrote, long enough to be one rather than a stub, which an override keeping it must leave where it is.",
	})
	mine := bookNow(t, id)
	keepAll := []string{"title", "subtitle", "authors", "narrators", "series", "genres", "tags", "publisher", "year", "language", "description"}
	all := call(t, "item_match_apply", withArgs(override, map[string]any{"override_details": true, "keep": toAny(keepAll)}))
	if kept := listOrNone(t, all["kept"], "kept"); !slices.Equal(kept, keepAll) {
		t.Errorf("kept = %v, want every field", kept)
	}
	if changed := bookChanges(mine, bookNow(t, id), "title", "subtitle", "author", "narrator", "series", "genres", "tags", "publisher", "year", "language", "description"); len(changed) > 0 {
		t.Errorf("an override keeping every field changed:\n  %s", strings.Join(changed, "\n  "))
	}

	// the store recorded on a book matched before the tag existed, once, and
	// in place of another store's
	call(t, "item_edit", map[string]any{"item": id, "remove_tags": []any{storeTag}})
	untagged := listOrNone(t, bookNow(t, id)["tags"], "tags")
	tagged := call(t, "item_match_tag", map[string]any{"library": "Messy", "providers": []any{"audible"}})
	if c, n := num(t, tagged["checked"], "checked"), num(t, tagged["tagged"], "tagged"); c != 1 || n != 1 {
		t.Errorf("item_match_tag checked %d and tagged %d, want the one matched book: %v", c, n, tagged)
	}
	if tagRows := rows(t, tagged["rows"], "rows"); len(tagRows) != 1 || tagRows[0]["id"] != id || tagRows[0]["provider"] != "audible" {
		t.Errorf("rows = %v, want Foundation found at audible", tagRows)
	}
	tags = listOrNone(t, bookNow(t, id)["tags"], "tags")
	if countOf(tags, storeTag) != 1 || !slices.Equal(slices.DeleteFunc(slices.Clone(tags), func(s string) bool { return s == storeTag }), untagged) {
		t.Errorf("tags = %v, want %v with %s once", tags, untagged, storeTag)
	}
	if again := call(t, "item_match_tag", map[string]any{"library": "Messy", "providers": []any{"audible"}}); num(t, again["already_tagged"], "already_tagged") != 1 || num(t, again["tagged"], "tagged") != 0 {
		t.Errorf("a second item_match_tag: %v, want the book counted as already tagged", again)
	}
	call(t, "item_edit", map[string]any{"item": id, "add_tags": []any{"zz-provider:audible.ca"}, "remove_tags": []any{storeTag}})
	retagged := call(t, "item_match_tag", map[string]any{"library": "Messy", "providers": []any{"audible"}, "overwrite": true})
	if n := num(t, retagged["tagged"], "tagged"); n != 1 {
		t.Errorf("tagged = %d with overwrite, want 1", n)
	}
	if tags := listOrNone(t, bookNow(t, id)["tags"], "tags"); countOf(tags, storeTag) != 1 || slices.Contains(tags, "zz-provider:audible.ca") {
		t.Errorf("tags = %v, want %s alone in place of the Canadian store's", tags, storeTag)
	}
	if msg := callErr(t, "item_match_tag", map[string]any{"library": "Messy", "providers": []any{"audible.cq"}}); !strings.Contains(msg, `no provider "audible.cq"`) {
		t.Errorf("a misspelt store: %s", msg)
	}
	if msg := callErr(t, "item_match_tag", map[string]any{"library": "Messy", "providers": []any{"google"}}); !strings.Contains(msg, "cannot look up an asin") {
		t.Errorf("a store that cannot look an asin up: %s", msg)
	}

	// an asin no store has, on a book no store has either: the server finds
	// nothing, and nothing is written, not even the store. A Discworld book
	// carries the made-up title, so no author is left without a book on the way
	nowhere := messyID(t, "Terry Pratchett/Discworld - 02 - The Light Fantastic")
	nowhereBefore := bookNow(t, nowhere)
	t.Cleanup(func() {
		putBackBook(t, nowhere, nowhereBefore)
		if msg := callErr(t, "author_get", map[string]any{"library": "Messy", "author": "Qxvzq Jzkfh"}); !strings.Contains(msg, "no author named") {
			t.Errorf("the made-up author outlived the put back: %s", msg)
		}
	})
	call(t, "item_edit", map[string]any{"item": nowhere, "title": "Qxvzq Wtrpl Jzkfh", "authors": []any{"Qxvzq Jzkfh"}})
	unmatchable := bookNow(t, nowhere)
	nothing := call(t, "item_match_apply_batch", map[string]any{"matches": []any{map[string]any{"item": nowhere, "asin": "B000000000"}}, "provider": "audible"})
	if a, u, f := num(t, nothing["applied"], "applied"), num(t, nothing["unchanged"], "unchanged"), num(t, nothing["failed"], "failed"); a != 0 || u != 1 || f != 0 {
		t.Errorf("a match that found nothing: applied %d, unchanged %d, failed %d, want 0, 1, 0", a, u, f)
	}
	if res := rows(t, nothing["results"], "results")[0]; res["updated"] == true || !strings.Contains(text(res["warning"]), "No audible match found") || strings.Contains(text(res["warning"]), "asin") {
		t.Errorf("the row = %v, want no update and only the server's warning", res)
	}
	single := call(t, "item_match_apply", map[string]any{"item": nowhere, "provider": "audible", "asin": "B000000000"})
	if single["updated"] == true || !strings.Contains(text(single["warning"]), "No audible match found") || strings.Contains(text(single["warning"]), "asin") {
		t.Errorf("item_match_apply = %v, want no update and only the server's warning", single)
	}
	if changed := bookChanges(unmatchable, bookNow(t, nowhere)); len(changed) > 0 {
		t.Errorf("a match that found nothing changed the book: %v", changed)
	}
}

// withArgs is a copy of base with extra laid over it.
func withArgs(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// The unmatched books worked through a window at a time, the way a session
// does it: score a window, apply the row that is right, and ask for the
// next. Under a missing: filter the applied book leaves the listing and the
// rest move up, so asking for next_offset would skip the book that moved
// into this window; item_match_batch says to ask for the same offset again,
// and this checks that doing so reaches every book, the one next_offset
// would have missed included.
func TestJourneyMatchBatchAskAgainAfterApplying(t *testing.T) {
	requireProviders(t)

	// the order the batch pages in: the unmatched books by title
	var order []string
	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Non-Fiction", "filter": "missing:asin", "sort": "title"})["items"], "items") {
		order = append(order, text(it["title"]))
	}
	if len(order) != 3 {
		t.Fatalf("Non-Fiction has %d unmatched books, want all 3: %v", len(order), order)
	}
	batchArgs := map[string]any{"library": "Non-Fiction", "providers": []any{"audible"}, "limit": 2}

	first := call(t, "item_match_batch", batchArgs)
	if first["filter"] != "missing:asin" || num(t, first["total"], "total") != 3 || num(t, first["offset"], "offset") != 0 || num(t, first["next_offset"], "next_offset") != 2 {
		t.Fatalf("offset 0 = filter %v, total %v, offset %v, next_offset %v, want missing:asin, 3, 0, 2", first["filter"], first["total"], first["offset"], first["next_offset"])
	}
	if paging := text(first["paging"]); !strings.Contains(paging, "offset 0 again") {
		t.Errorf("paging = %q, want the advice to ask for offset 0 again after applying", paging)
	}
	window := rows(t, first["rows"], "rows")
	if got := titlesIn(t, first["rows"], "rows"); !slices.Equal(got, order[:2]) {
		t.Fatalf("offset 0 = %v, want %v", got, order[:2])
	}

	// the row for the book to apply, and the book as it was
	i := slices.IndexFunc(window, func(r map[string]any) bool { return r["title"] == "A Brief History of Vice" })
	if i < 0 {
		t.Fatalf("A Brief History of Vice is not in the first window: %v", order)
	}
	row := window[i]
	best, _ := row["best"].(map[string]any)
	asin := text(best["asin"])
	if asin == "" {
		t.Fatalf("no candidate to apply: %v", row)
	}
	vice := text(row["id"])
	before := bookNow(t, vice)
	t.Cleanup(func() { putBackBook(t, vice, before) })

	applied := call(t, "item_match_apply_batch", map[string]any{"matches": []any{map[string]any{"item": vice, "asin": asin, "provider": row["provider"]}}})
	if a, u, f := num(t, applied["applied"], "applied"), num(t, applied["unchanged"], "unchanged"), num(t, applied["failed"], "failed"); a != 1 || u != 0 || f != 0 {
		t.Errorf("applied %d, unchanged %d, failed %d, want 1, 0, 0: %v", a, u, f, applied)
	}
	after := bookNow(t, vice)
	if after["asin"] != asin {
		t.Errorf("asin = %v, want %s", after["asin"], asin)
	}
	// the book's own tags stay, and the store is recorded after them once
	if tags, own := listOrNone(t, after["tags"], "tags"), listOrNone(t, before["tags"], "tags"); !slices.Equal(tags, append(slices.Clone(own), storeTag)) {
		t.Errorf("tags = %v, want %v then %s", tags, own, storeTag)
	}

	// the same offset again holds the book that moved up into the window;
	// next_offset from before now starts past the end, and holds nothing
	rest := slices.DeleteFunc(slices.Clone(order), func(s string) bool { return s == "A Brief History of Vice" })
	again := call(t, "item_match_batch", batchArgs)
	if got := titlesIn(t, again["rows"], "rows"); num(t, again["total"], "total") != 2 || !slices.Equal(got, rest) {
		t.Errorf("offset 0 again = %v of %v, want %v", got, again["total"], rest)
	}
	if again["next_offset"] != nil || again["paging"] != nil {
		t.Errorf("the last window says there is more: next_offset %v, paging %v", again["next_offset"], again["paging"])
	}
	skipped := call(t, "item_match_batch", withArgs(batchArgs, map[string]any{"offset": num(t, first["next_offset"], "next_offset")}))
	if got := rows(t, skipped["rows"], "rows"); len(got) != 0 {
		t.Errorf("offset 2 = %v, want nothing: the book it held has moved up", got)
	}
	// asking again at the same offset after applying reached every book:
	// the two first seen and the one that moved up
	seen := slices.Concat(titlesIn(t, first["rows"], "rows"), titlesIn(t, again["rows"], "rows"))
	for _, title := range order {
		if !slices.Contains(seen, title) {
			t.Errorf("%s was never in a window: saw %v", title, seen)
		}
	}
}

// One asin on two books, the way a slip of the paste puts it there: the
// audits that look a matched book up and the one that finds copies both say
// so, and clearing it puts every answer back. It catches an audit that
// compares only the length (every one-second fixture is off in length), and
// a duplicate grouping that misses a pair sharing an asin under different
// titles.
func TestJourneyOneASINOnTwoBooks(t *testing.T) {
	requireProviders(t)

	colour := messyID(t, "Terry Pratchett/Discworld - 01 - The Colour of Magic")
	foundation := messyID(t, "Isaac Asimov/Foundation")
	t.Cleanup(func() { call(t, "item_edit", map[string]any{"item": colour, "clear": []any{"asin"}}) })

	// problems per book, and how many books were looked up
	matched := func(t *testing.T, extra map[string]any) (map[string][]string, map[string]any) {
		t.Helper()
		out := call(t, "audit_matched", withMessy(withArgs(map[string]any{"providers": []any{"audible"}}, extra)))
		problems := map[string][]string{}
		for _, f := range rows(t, out["findings"], "findings") {
			problems[text(f["id"])] = strs(t, f["problems"], "problems")
		}
		return problems, out
	}
	asinGroup := func(t *testing.T, library string) []string {
		t.Helper()
		args := map[string]any{}
		if library != "" {
			args["library"] = library
		}
		for _, g := range rows(t, call(t, "audit_duplicates", args)["groups"], "groups") {
			if strings.Contains(text(g["key"]), "asin:"+foundationASIN) {
				ids := valuesIn(t, g["items"], "items", "id")
				slices.Sort(ids)
				return ids
			}
		}
		return nil
	}
	unmatched := func(t *testing.T) []string {
		t.Helper()
		return titlesIn(t, call(t, "audit_unmatched", withMessy(map[string]any{"limit": 100}))["findings"], "findings")
	}

	problems, out := matched(t, nil)
	if num(t, out["items_scanned"], "items_scanned") != 1 || !slices.Equal(problems[foundation], []string{"duration_off"}) || len(problems) != 1 {
		t.Fatalf("before: %v, want Foundation alone, off only in length", out)
	}
	if !slices.Contains(unmatched(t), "The Colour of Magic") {
		t.Fatal("The Colour of Magic is not listed as unmatched before it has an asin")
	}

	call(t, "item_edit", map[string]any{"item": colour, "asin": foundationASIN})

	problems, out = matched(t, nil)
	if n := num(t, out["items_scanned"], "items_scanned"); n != 2 {
		t.Errorf("items_scanned = %d, want both books with the asin", n)
	}
	if got := problems[colour]; !slices.Contains(got, "title_differs") || !slices.Contains(got, "narrator_differs") {
		t.Errorf("The Colour of Magic: %v, want title_differs and narrator_differs: the asin is another book read by someone else", got)
	}
	if got := problems[foundation]; !slices.Equal(got, []string{"duration_off"}) {
		t.Errorf("Foundation: %v, want still only duration_off", got)
	}
	counts, _ := out["counts"].(map[string]any)
	if num(t, counts["title_differs"], "title_differs") != 1 || num(t, counts["narrator_differs"], "narrator_differs") != 1 || num(t, counts["duration_off"], "duration_off") != 2 {
		t.Errorf("counts = %v, want one title and one narrator off, two lengths", counts)
	}
	for _, f := range rows(t, out["findings"], "findings") {
		if f["id"] != colour {
			continue
		}
		prov, _ := f["provider"].(map[string]any)
		if f["found_in"] != "audible" || prov["title"] != "Foundation" || prov["narrator"] != "Scott Brick" {
			t.Errorf("the finding names %v in %v, want Foundation read by Scott Brick at audible", prov, f["found_in"])
		}
	}
	// with fields, the difference is spelled out: both titles
	_, withFields := matched(t, map[string]any{"fields": true})
	for _, f := range rows(t, withFields["findings"], "findings") {
		if f["id"] != colour {
			continue
		}
		var title map[string]any
		for _, d := range rows(t, f["fields"], "fields") {
			if d["field"] == "title" {
				title = d
			}
		}
		if title == nil || title["local"] != "The Colour of Magic" || title["provider"] != "Foundation" {
			t.Errorf("the title difference = %v, want The Colour of Magic against Foundation", title)
		}
	}

	// the two share an asin, so they are one work to the duplicates audit,
	// in the library and across the server
	want := []string{colour, foundation}
	slices.Sort(want)
	if got := asinGroup(t, "Messy"); !slices.Equal(got, want) {
		t.Errorf("the asin group in Messy = %v, want the two books", got)
	}
	if got := asinGroup(t, ""); !slices.Equal(got, want) {
		t.Errorf("the asin group across the server = %v, want the two books", got)
	}
	if slices.Contains(unmatched(t), "The Colour of Magic") {
		t.Error("a book with an asin is still listed as unmatched")
	}

	// put right
	call(t, "item_edit", map[string]any{"item": colour, "clear": []any{"asin"}})
	problems, out = matched(t, nil)
	if num(t, out["items_scanned"], "items_scanned") != 1 || len(problems) != 1 || !slices.Equal(problems[foundation], []string{"duration_off"}) {
		t.Errorf("after clearing: %v, want Foundation alone again", out)
	}
	if got := asinGroup(t, "Messy"); got != nil {
		t.Errorf("the asin group outlived the clear: %v", got)
	}
	if !slices.Contains(unmatched(t), "The Colour of Magic") {
		t.Error("The Colour of Magic is not listed as unmatched once its asin is gone")
	}
}

// Copies and editions: the duplicates audit joins a matched copy with the
// unmatched copy beside it, joins two books that share an isbn however it is
// hyphenated, and never joins two books whose asins differ, since those are
// two recordings of one work that a collector keeps on purpose. It catches a
// grouping by a single key (the asin, else the title), which splits the copy
// a scan brought in from the one already matched.
func TestJourneyDuplicatesJoinCopiesNotEditions(t *testing.T) {
	inSeries := messyID(t, "Terry Pratchett/Discworld - 04 - Mort")
	loose := messyID(t, "Terry Pratchett/Mort")
	red := messyID(t, "Kim Stanley Robinson/Mars Trilogy - 01 - Red Mars")
	blue := messyID(t, "Kim Stanley Robinson/Mars Trilogy - 03 - Blue Mars")
	t.Cleanup(func() {
		for _, id := range []string{inSeries, loose} {
			call(t, "item_edit", map[string]any{"item": id, "clear": []any{"asin"}})
		}
		for _, id := range []string{red, blue} {
			call(t, "item_edit", map[string]any{"item": id, "clear": []any{"isbn"}})
		}
	})

	// the group holding a book, with what its copies share
	groupOf := func(t *testing.T, id string) (key string, ids []string) {
		t.Helper()
		for _, g := range rows(t, call(t, "audit_duplicates", messy)["groups"], "groups") {
			members := valuesIn(t, g["items"], "items", "id")
			if slices.Contains(members, id) {
				slices.Sort(members)
				return text(g["key"]), members
			}
		}
		return "", nil
	}
	morts := []string{inSeries, loose}
	slices.Sort(morts)

	if key, ids := groupOf(t, inSeries); !slices.Equal(ids, morts) || key != "title:mort|terry pratchett" {
		t.Fatalf("the two Morts: %q %v, want grouped by title and author", key, ids)
	}
	// one matched, one not: still one work
	call(t, "item_edit", map[string]any{"item": inSeries, "asin": "B0ZZYZX001"})
	if key, ids := groupOf(t, inSeries); !slices.Equal(ids, morts) {
		t.Errorf("a matched copy and an unmatched copy: %q %v, want them together", key, ids)
	}
	// two asins: two recordings, not a duplicate
	call(t, "item_edit", map[string]any{"item": loose, "asin": "B0ZZYZX002"})
	if key, ids := groupOf(t, inSeries); ids != nil {
		t.Errorf("two books with different asins were grouped: %q %v", key, ids)
	}
	// one asin on both: grouped by it as well as by the title
	call(t, "item_edit", map[string]any{"item": loose, "asin": "B0ZZYZX001"})
	if key, ids := groupOf(t, inSeries); !slices.Equal(ids, morts) || !strings.Contains(key, "asin:B0ZZYZX001") || !strings.Contains(key, "title:mort") {
		t.Errorf("one asin on both: %q %v, want grouped by the asin and the title", key, ids)
	}

	// an isbn two different titles share, hyphenated on one of them
	call(t, "item_edit", map[string]any{"item": red, "isbn": "978-0-553-56073-8"})
	call(t, "item_edit", map[string]any{"item": blue, "isbn": "9780553560738"})
	mars := []string{red, blue}
	slices.Sort(mars)
	if key, ids := groupOf(t, red); !slices.Equal(ids, mars) || key != "isbn:9780553560738" {
		t.Errorf("a shared isbn: %q %v, want Red and Blue Mars grouped by it", key, ids)
	}
	call(t, "item_edit", map[string]any{"item": blue, "clear": []any{"isbn"}})
	if key, ids := groupOf(t, red); ids != nil {
		t.Errorf("the isbn group outlived the clear: %q %v", key, ids)
	}
}
