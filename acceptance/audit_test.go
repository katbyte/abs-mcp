//go:build integration

// The audits: the tools whose output is judgement of our own rather than a
// projection of an API response. They sweep a real library and the assertions
// are about what they conclude from it.
package acceptance

import (
	"slices"
	"strings"
	"testing"
)

// auditTools is every audit the server registers. TestEveryAuditRuns calls all
// of them, so a tool cannot be added without acquiring coverage here.
var auditTools = []string{
	"audit_unmatched",
	"audit_issues",
	"audit_no_audio",
	"audit_path",
	"audit_chapters",
	"audit_podcast_stale_feed",
	"audit_podcast_no_episodes",
}

// missingFields are the values audit_missing accepts. They are one tool rather
// than ten because they are one query with a variable.
var missingFields = []string{
	"cover", "description", "narrator", "series", "author",
	"genres", "year", "publisher", "language", "chapters",
}

// Every audit must answer against a real library with a well-formed worklist.
// This is the plumbing check: the filter encoding the server accepts, the
// projection decoding, the name resolution. What each one concludes is
// asserted below.
func TestEveryAuditRuns(t *testing.T) {
	registered := map[string]bool{}
	for _, name := range toolNames(t) {
		registered[name] = true
	}

	for _, name := range auditTools {
		if !registered[name] {
			t.Errorf("%s is in the test list but not registered", name)
			continue
		}
		out := call(t, name, map[string]any{"library": "Fiction"})
		if _, ok := out["total_findings"]; !ok {
			t.Errorf("%s returned no total_findings: %v", name, out)
		}
		rows(t, out["findings"], name+".findings")
	}

	// every field audit_missing accepts must answer too
	for _, field := range missingFields {
		out := call(t, "audit_missing", map[string]any{"library": "Fiction", "field": field})
		if _, ok := out["total_findings"]; !ok {
			t.Errorf("audit_missing %s returned no total_findings: %v", field, out)
		}
		rows(t, out["findings"], "audit_missing "+field)
	}
	if msg := callErr(t, "audit_missing", map[string]any{"library": "Fiction", "field": "nope"}); msg == "" {
		t.Error("audit_missing should refuse an unknown field")
	}

	// and nothing registered as an audit may be missing from the list above
	for name := range registered {
		if !strings.HasPrefix(name, "audit_") {
			continue
		}
		switch name {
		case "audit_all", "audit_missing", "audit_duplicates", "audit_series",
			"audit_spelling", "audit_unembedded",
			"audit_covers", "audit_authors", "audit_narrators", "audit_matched", "audit_genres":
			continue // asserted individually below
		}
		if !slices.Contains(auditTools, name) {
			t.Errorf("%s is registered but has no coverage; add it to auditTools", name)
		}
	}
}

// The fixtures have no covers at all, so this is the audit with a known exact
// answer - which also proves the server-side filter path.
func TestAuditMissingCover(t *testing.T) {
	out := call(t, "audit_missing", map[string]any{"library": "Fiction", "field": "cover"})

	if found := num(t, out["total_findings"], "total_findings"); found != 7 {
		t.Errorf("total_findings = %d, want all 7 fiction items", found)
	}
}

// Nothing was ever matched to a provider, so every book trips this.
func TestAuditUnmatched(t *testing.T) {
	out := call(t, "audit_unmatched", map[string]any{"library": "Non-Fiction"})

	if found := num(t, out["total_findings"], "total_findings"); found != 3 {
		t.Errorf("total_findings = %d, want 3", found)
	}
	for _, f := range rows(t, out["findings"], "findings") {
		if detail, _ := f["detail"].(string); detail == "" {
			t.Errorf("finding has no detail saying why: %v", f)
		}
	}
}

// The sweep path rather than a native filter: non-fiction carries no series.
func TestAuditMissingSeries(t *testing.T) {
	out := call(t, "audit_missing", map[string]any{"library": "Non-Fiction", "field": "series"})

	if found := num(t, out["total_findings"], "total_findings"); found != 3 {
		t.Errorf("total_findings = %d, want 3", found)
	}
	// and fiction, which is all in series, must come back clean
	out = call(t, "audit_missing", map[string]any{"library": "Fiction", "field": "series"})
	if found := num(t, out["total_findings"], "total_findings"); found != 0 {
		t.Errorf("Fiction total_findings = %d, want 0 - every book is in a series", found)
	}
}

// The fixtures are one-second files, so nothing is long enough to want
// chapters. A clean result is the assertion.
func TestAuditMissingChapters(t *testing.T) {
	out := call(t, "audit_missing", map[string]any{"library": "Fiction", "field": "chapters"})

	if found := num(t, out["total_findings"], "total_findings"); found != 0 {
		t.Errorf("total_findings = %d, want 0 - the fixtures are too short to need chapters", found)
	}
}

// The folders are laid out <author>/<title>, so the path heuristic must clear
// every one of them.
func TestAuditPath(t *testing.T) {
	out := call(t, "audit_path", map[string]any{"library": "Fiction"})

	if found := num(t, out["total_findings"], "total_findings"); found != 0 {
		t.Errorf("total_findings = %d, want 0: %v", found, out["findings"])
	}
}

// The podcast audits must read the podcast library and leave the books alone.
func TestAuditPodcasts(t *testing.T) {
	out := call(t, "audit_podcast_no_episodes", map[string]any{"library": "Podcasts"})
	if found := num(t, out["total_findings"], "total_findings"); found != 0 {
		t.Errorf("audit_podcast_no_episodes = %d, want 0 - both shows have episodes", found)
	}

	// a book library has no podcasts to flag
	out = call(t, "audit_podcast_stale_feed", map[string]any{"library": "Fiction"})
	if found := num(t, out["total_findings"], "total_findings"); found != 0 {
		t.Errorf("audit_podcast_stale_feed on books = %d, want 0", found)
	}
}

// audit_all must run every per-item audit in one sweep and agree with what the
// individual audits say.
func TestAuditAll(t *testing.T) {
	all := call(t, "audit_all", map[string]any{"library": "Fiction"})

	if scanned := num(t, all["items_scanned"], "items_scanned"); scanned != 7 {
		t.Errorf("items_scanned = %d, want 7", scanned)
	}

	counts := map[string]int{}
	fields := map[string]int{}
	for _, row := range rows(t, all["audits"], "audits") {
		name, _ := row["audit"].(string)
		if field, ok := row["field"].(string); ok && field != "" {
			fields[field] = num(t, row["found"], "found")
			continue
		}
		counts[name] = num(t, row["found"], "found")
	}
	clean := strs(t, all["clean"], "clean")

	// every audit appears exactly once, found, clean or not applicable (the
	// podcast audits, in a library of books); the ones that fetch something
	// per item are named as skipped instead
	var notApplicable []string
	if all["not_applicable"] != nil {
		notApplicable = strs(t, all["not_applicable"], "not_applicable")
	}
	if !slices.Equal(notApplicable, []string{"audit_podcast_stale_feed", "audit_podcast_no_episodes"}) {
		t.Errorf("not_applicable = %v, want the two podcast audits", notApplicable)
	}
	crossItem := []string{"audit_duplicates", "audit_spelling", "audit_authors", "audit_narrators", "audit_series", "audit_genres"}
	for _, name := range slices.Concat(auditTools, crossItem) {
		_, reported := counts[name]
		lists := 0
		for _, in := range []bool{reported, slices.Contains(clean, name), slices.Contains(notApplicable, name)} {
			if in {
				lists++
			}
		}
		if lists != 1 {
			t.Errorf("%s is in %d of found, clean and not applicable, want 1", name, lists)
		}
	}
	perItem := []string{"audit_covers", "audit_unembedded", "audit_matched"}
	if skipped := strs(t, all["skipped"], "skipped"); !slices.Equal(skipped, perItem) {
		t.Errorf("skipped = %v, want %v", skipped, perItem)
	}
	for _, name := range perItem {
		if _, reported := counts[name]; reported || slices.Contains(clean, name) {
			t.Errorf("%s ran without deep", name)
		}
	}

	// with deep the covers and the embedded tags run as well, and agree
	// with the tools themselves. Fiction is on the server's default
	// provider, google, which cannot look an asin up, and no --providers is
	// set: audit_matched refuses it alone, and deep skips it saying so
	deep := call(t, "audit_all", map[string]any{"library": "Fiction", "deep": true})
	if skipped := strs(t, deep["skipped"], "skipped"); !slices.Equal(skipped, []string{"audit_matched"}) {
		t.Errorf("deep skipped %v, want audit_matched alone", skipped)
	}
	const onGoogle = `library "Fiction" is on the google provider, which cannot look up an asin`
	for _, row := range rows(t, deep["not_run"], "not_run") {
		if row["audit"] == "audit_matched" && !strings.Contains(text(row["reason"]), onGoogle) {
			t.Errorf("audit_matched not run because %q, want %q", row["reason"], onGoogle)
		}
	}
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"audit_matched", map[string]any{"library": "Fiction"}},
		{"audit_covers", map[string]any{"library": "Fiction", "store": true}},
		{"item_match_tag", map[string]any{"library": "Fiction"}},
		{"item_cover_upgrade", map[string]any{"library": "Fiction", "items": []any{"Foundation"}}},
	} {
		if msg := callErr(t, c.tool, c.args); !strings.Contains(msg, onGoogle) || !strings.Contains(msg, "--providers") {
			t.Errorf("%s on Fiction with no providers: %q, want the library, its provider and the fix", c.tool, msg)
		}
	}
	deepCounts := map[string]int{}
	for _, row := range rows(t, deep["audits"], "audits") {
		if name, _ := row["audit"].(string); slices.Contains(perItem, name) {
			deepCounts[name] = num(t, row["found"], "found")
		}
	}
	for _, name := range perItem[:2] {
		one := call(t, name, map[string]any{"library": "Fiction"})
		if got := num(t, one["total_findings"], "total_findings"); got != deepCounts[name] {
			t.Errorf("%s: audit_all deep says %d, the audit itself says %d", name, deepCounts[name], got)
		}
	}

	// and the counts match what the individual audit says
	for name, want := range counts {
		one := call(t, name, map[string]any{"library": "Fiction"})
		if got := num(t, one["total_findings"], "total_findings"); got != want {
			t.Errorf("%s: audit_all says %d, the audit itself says %d", name, want, got)
		}
	}
	for field, want := range fields {
		one := call(t, "audit_missing", map[string]any{"library": "Fiction", "field": field})
		if got := num(t, one["total_findings"], "total_findings"); got != want {
			t.Errorf("audit_missing %s: audit_all says %d, the audit itself says %d", field, want, got)
		}
	}
}

// audit_series must find both shapes of hole, invent none in a complete
// series, and skip the podcast library without erroring.
func TestAuditSeriesGaps(t *testing.T) {
	out := call(t, "audit_series", nil)

	got := map[string][]string{}
	for _, row := range rows(t, out["gaps"], "gaps") {
		name, _ := row["name"].(string)
		got[name] = strs(t, row["missing"], "missing")
	}

	if missing, ok := got["The Expanse"]; !ok {
		t.Errorf("The Expanse not reported; got %v", got)
	} else if !slices.Equal(missing, []string{"2"}) {
		t.Errorf("The Expanse missing = %v, want [2]", missing)
	}

	// a run of gaps, not just a single one
	if missing, ok := got["Otherland"]; !ok {
		t.Errorf("Otherland not reported; got %v", got)
	} else if !slices.Equal(missing, []string{"2", "3"}) {
		t.Errorf("Otherland missing = %v, want [2 3]", missing)
	}

	if missing, reported := got["Foundation"]; reported {
		t.Errorf("Foundation is complete (1-3) but was reported missing %v", missing)
	}
}

// Nothing in the clean libraries is a duplicate of anything else; the Messy
// library's pair is asserted in messy_test.go.
func TestAuditDuplicates(t *testing.T) {
	out := call(t, "audit_duplicates", map[string]any{"library": "Fiction"})

	if groups := rows(t, out["groups"], "groups"); len(groups) != 0 {
		t.Errorf("audit_duplicates found %d groups in a clean library: %v", len(groups), groups)
	}
}

// audit_spelling groups spellings of the same value. The clean fixtures are
// consistent, so a clean result is the assertion - and the language alias
// table must not invent a group out of a single spelling. The Messy library
// has the inconsistent ones.
func TestAuditSpelling(t *testing.T) {
	out := call(t, "audit_spelling", map[string]any{"library": "Fiction"})

	if groups := rows(t, out["groups"], "groups"); len(groups) != 0 {
		t.Errorf("audit_spelling found %d groups in a consistent library: %v", len(groups), groups)
	}
	// every fixture is English, which the alias table recognizes
	if odd, present := out["unrecognized_languages"]; present {
		t.Errorf("unrecognized_languages = %v, want none", odd)
	}

	// a bad field is an error naming the valid ones
	if msg := callErr(t, "audit_spelling", map[string]any{"field": "nope"}); msg == "" {
		t.Error("an unknown field should be refused")
	}
}

func TestAuditAuthors(t *testing.T) {
	out := call(t, "audit_authors", map[string]any{"library": "Fiction"})

	// no author was ever matched, so all three lack an asin and a photo
	counts, ok := out["counts"].(map[string]any)
	if !ok {
		t.Fatalf("counts = %v, want an object", out["counts"])
	}
	if got := num(t, counts["no_photo"], "counts.no_photo"); got != 3 {
		t.Errorf("counts.no_photo = %d, want 3", got)
	}
	if got := num(t, counts["unmatched"], "counts.unmatched"); got != 3 {
		t.Errorf("counts.unmatched = %d, want 3", got)
	}
	records := rows(t, out["records"], "records")
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	// most-published first within the same problems
	if books := num(t, records[0]["books"], "books"); books != 3 {
		t.Errorf("first record has %d books, want Asimov's 3 (sorted most first)", books)
	}
	if got := num(t, out["total_findings"], "total_findings"); got != 3 {
		t.Errorf("total_findings = %d, want the three records and nothing else", got)
	}
}

func TestAuditNarrators(t *testing.T) {
	out := call(t, "audit_narrators", map[string]any{"library": "Fiction"})
	if _, ok := out["roles"]; !ok {
		t.Error("no roles section")
	}
	if _, ok := out["names"]; !ok {
		t.Error("no names section")
	}
}

// The non-fiction covers are one of each shape: square, a jacket scan, and
// one too small to keep. The audit reads the files themselves, so the sizes it
// reports are the ones the seed script wrote.
func TestAuditCovers(t *testing.T) {
	out := call(t, "audit_covers", map[string]any{"library": "Non-Fiction"})

	if checked := num(t, out["covers_checked"], "covers_checked"); checked != 3 {
		t.Errorf("covers_checked = %d, want 3", checked)
	}
	if found := num(t, out["total_findings"], "total_findings"); found != 2 {
		t.Errorf("total_findings = %d, want the jacket and the small one", found)
	}
	problems := map[string]string{}
	for _, row := range rows(t, out["findings"], "findings") {
		title, _ := row["title"].(string)
		problem, _ := row["problem"].(string)
		problems[title] = problem
		if num(t, row["width"], "width") == 0 {
			t.Errorf("%s: no width measured", title)
		}
	}
	if problems["A Brief History of Vice"] != "ratio" {
		t.Errorf("the 400x600 jacket is %q, want ratio", problems["A Brief History of Vice"])
	}
	if problems["War Is a Racket"] != "small" {
		t.Errorf("the 200x200 cover is %q, want small", problems["War Is a Racket"])
	}
	if _, flagged := problems["The Arms of Krupp"]; flagged {
		t.Errorf("the 600x600 cover was flagged: %v", problems)
	}

	// Fiction has no covers at all, which is a finding per book
	out = call(t, "audit_covers", map[string]any{"library": "Fiction"})
	if found := num(t, out["total_findings"], "total_findings"); found != 7 {
		t.Errorf("Fiction total_findings = %d, want 7 missing covers", found)
	}
}

// The fixtures are silent files ffmpeg wrote with no tags, so every book is
// unembedded until item_embed_metadata runs. The embed waits for the
// background task and rescans, so the audit is clear as soon as it returns.
func TestAuditUnembedded(t *testing.T) {
	const item = "The Arms of Krupp"

	out := call(t, "audit_unembedded", map[string]any{"library": "Non-Fiction"})
	if scanned := num(t, out["items_scanned"], "items_scanned"); scanned != 3 {
		t.Errorf("items_scanned = %d, want 3", scanned)
	}
	if found := num(t, out["total_findings"], "total_findings"); found != 3 {
		t.Fatalf("total_findings = %d, want all 3 untagged fixtures: %v", found, out["findings"])
	}
	for _, f := range rows(t, out["findings"], "findings") {
		if detail, _ := f["detail"].(string); !strings.Contains(detail, "no tags") {
			t.Errorf("finding should say the files carry no tags: %v", f)
		}
	}

	// a podcast library has nothing to embed
	if pods := call(t, "audit_unembedded", map[string]any{"library": "Podcasts"}); num(t, pods["items_scanned"], "items_scanned") != 0 {
		t.Errorf("podcasts were scanned: %v", pods)
	}

	keepAudioFiles(t, item)
	if embedded, _ := call(t, "item_embed_metadata", map[string]any{"item": item})["embedded"].(bool); !embedded {
		t.Fatalf("item_embed_metadata did not embed %s", item)
	}
	after := call(t, "audit_unembedded", map[string]any{"library": "Non-Fiction"})
	if titles := titlesIn(t, after["findings"], "findings"); slices.Contains(titles, item) || len(titles) != 2 {
		t.Errorf("findings after the embed = %v, want the 2 other fixtures", titles)
	}
}
