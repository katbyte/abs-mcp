//go:build integration

// The Messy library: every defect the curation audits exist to find, seeded
// on purpose (the layout in scripts/abs-testenv.sh, the metadata in
// messyBooks). Each test here is one audit against it, asserting the finding
// it was seeded for and that the clean books beside it are left alone.
package acceptance

import (
	"slices"
	"strings"
	"testing"
)

// messy is the library every call here names.
var messy = map[string]any{"library": "Messy"}

// withMessy adds arguments to the library.
func withMessy(extra map[string]any) map[string]any {
	args := map[string]any{"library": "Messy"}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

// The scan saw everything the layout put there: the folder books, the
// three-file book, and the book that is one m4b file.
func TestMessyItems(t *testing.T) {
	if total := num(t, call(t, "library_items", withMessy(map[string]any{"limit": 1}))["total"], "total"); total != len(messyBooks) {
		t.Errorf("total = %d, want %d", total, len(messyBooks))
	}

	eric := call(t, "item_get", withMessy(map[string]any{"item": "Eric"}))
	if path, _ := eric["path"].(string); !strings.HasSuffix(path, "Eric.m4b") {
		t.Errorf("Eric path = %q, want the m4b file itself", path)
	}
	if tracks := num(t, eric["audio_tracks"], "audio_tracks"); tracks != 1 {
		t.Errorf("Eric audio_tracks = %d, want 1", tracks)
	}
	guards := call(t, "item_get", withMessy(map[string]any{"item": "Guards! Guards!"}))
	if tracks := num(t, guards["audio_tracks"], "audio_tracks"); tracks != 3 {
		t.Errorf("Guards! Guards! audio_tracks = %d, want 3", tracks)
	}
	// two books are titled Mort, so the title alone is ambiguous
	if msg := callErr(t, "item_get", withMessy(map[string]any{"item": "Mort"})); msg == "" {
		t.Error("a title two books share should be refused rather than guessed")
	}
}

// audit_series: a gap whose missing book is on the shelf unlinked, one
// series spelled two ways, numbers padded both ways, titles that are the
// series name, and the article names when asked.
func TestMessySeries(t *testing.T) {
	out := call(t, "audit_series", withMessy(map[string]any{"articles": true}))

	t.Run("gaps", func(t *testing.T) {
		var mars map[string]any
		for _, row := range rows(t, out["gaps"], "gaps") {
			if row["name"] == "Mars Trilogy" {
				mars = row
			}
			if row["name"] == "Discworld" || row["name"] == "Stormlight Archive" {
				t.Errorf("%v is complete but reported missing %v", row["name"], row["missing"])
			}
		}
		if mars == nil {
			t.Fatalf("Mars Trilogy not reported; gaps = %v", out["gaps"])
		}
		if missing := strs(t, mars["missing"], "missing"); !slices.Equal(missing, []string{"2"}) {
			t.Errorf("Mars Trilogy missing = %v, want [2]", missing)
		}
		unlinked := rows(t, mars["unlinked"], "unlinked")
		if len(unlinked) != 1 {
			t.Fatalf("unlinked = %v, want Green Mars", unlinked)
		}
		if path, _ := unlinked[0]["path"].(string); !strings.Contains(path, "Green Mars") {
			t.Errorf("unlinked path = %q, want the Green Mars folder", path)
		}
		if suggest, _ := unlinked[0]["suggest"].(string); !strings.Contains(suggest, "Mars Trilogy") {
			t.Errorf("unlinked suggest = %q, want a link into Mars Trilogy", suggest)
		}
	})

	t.Run("names", func(t *testing.T) {
		found := false
		for _, group := range rows(t, out["names"], "names") {
			var values []string
			for _, sp := range rows(t, group["spellings"], "spellings") {
				v, _ := sp["value"].(string)
				values = append(values, v)
			}
			if slices.Contains(values, "The Wheel of Time") && slices.Contains(values, "Wheel of Time") {
				found = true
			}
		}
		if !found {
			t.Errorf("The Wheel of Time and Wheel of Time were not grouped: %v", out["names"])
		}
	})

	t.Run("numbering", func(t *testing.T) {
		padding := map[string]string{}
		folders := map[string]string{}
		var unlinked []string
		for _, row := range rows(t, out["numbering"], "numbering") {
			title, _ := row["title"].(string)
			switch row["problem"] {
			case "padding":
				padding[title], _ = row["suggest"].(string)
			case "unlinked":
				unlinked = append(unlinked, title)
			case "folder_style":
				folders[title], _ = row["suggest"].(string)
			}
		}
		// the Wheel of Time folders spell the series two ways, two and two:
		// the two without its article are the ones renamed, to the spelling
		// that sorts first
		wantFolders := map[string]string{
			"The Dragon Reborn": "Robert Jordan/The Wheel of Time - 03 - The Dragon Reborn",
			"The Shadow Rising": "Robert Jordan/The Wheel of Time - 04 - The Shadow Rising",
		}
		for title, want := range wantFolders {
			if folders[title] != want {
				t.Errorf("folder_style for %s = %q, want %q", title, folders[title], want)
			}
		}
		if len(folders) != len(wantFolders) {
			t.Errorf("folder_style flagged %v, want only the Wheel of Time folders without the article", folders)
		}
		if padding["The Light Fantastic"] != "Discworld #02" || padding["Sourcery"] != "Discworld #05" {
			t.Errorf("padding = %v, want #2 and #5 restyled as two digits", padding)
		}
		if len(padding) != 2 {
			t.Errorf("padding flagged %v, want only the two unpadded books", padding)
		}
		if !slices.Contains(unlinked, "Green Mars") {
			t.Errorf("unlinked = %v, want Green Mars, whose folder carries the series", unlinked)
		}
	})

	t.Run("titles", func(t *testing.T) {
		problems := map[string]string{}
		suggests := map[string]string{}
		for _, row := range rows(t, out["titles"], "titles") {
			title, _ := row["title"].(string)
			problems[title], _ = row["problem"].(string)
			suggests[title], _ = row["suggest"].(string)
		}
		if problems["A Song of Ice and Fire"] != "series_as_title" || suggests["A Song of Ice and Fire"] != "A Game of Thrones" {
			t.Errorf("the title that is the series: problem %q, suggest %q", problems["A Song of Ice and Fire"], suggests["A Song of Ice and Fire"])
		}
		const clash = "A Clash of Kings: A Song of Ice and Fire, Book 2"
		if problems[clash] != "series_in_title" || suggests[clash] != "A Clash of Kings" {
			t.Errorf("the title carrying the series and a number: problem %q, suggest %q", problems[clash], suggests[clash])
		}
		if len(problems) != 2 {
			t.Errorf("titles flagged %v, want only the two", problems)
		}
	})

	t.Run("articles", func(t *testing.T) {
		var names []string
		for _, row := range rows(t, out["articles"], "articles") {
			name, _ := row["name"].(string)
			names = append(names, name)
		}
		if !slices.Contains(names, "The Wheel of Time") {
			t.Errorf("articles = %v, want The Wheel of Time", names)
		}
		if slices.Contains(names, "Discworld") {
			t.Errorf("articles = %v: Discworld has no article", names)
		}
	})
}

// audit_narrators sees the narrator spelled two ways on the Wheel of Time
// books (narrators are its field; audit_spelling covers the others).
func TestMessySpelling(t *testing.T) {
	out := call(t, "audit_narrators", messy)

	found := false
	for _, group := range rows(t, out["names"], "names") {
		var values []string
		for _, sp := range rows(t, group["spellings"], "spellings") {
			v, _ := sp["value"].(string)
			values = append(values, v)
		}
		if slices.Contains(values, "Michael Kramer") && slices.Contains(values, "Micheal Kramer") {
			found = true
		}
	}
	if !found {
		t.Errorf("Michael and Micheal Kramer were not grouped: %v", out["names"])
	}
	if msg := callErr(t, "audit_spelling", withMessy(map[string]any{"field": "narrators"})); !strings.Contains(msg, "audit_narrators") {
		t.Errorf("audit_spelling on narrators should point at audit_narrators: %s", msg)
	}
}

// audit_authors: the author spelled two ways, and the record that is a title.
func TestMessyAuthors(t *testing.T) {
	out := call(t, "audit_authors", messy)

	found := false
	for _, group := range rows(t, out["names"], "names") {
		var values []string
		for _, sp := range rows(t, group["spellings"], "spellings") {
			v, _ := sp["value"].(string)
			values = append(values, v)
		}
		if slices.Contains(values, "Brandon Sanderson") && slices.Contains(values, "Sanderson, Brandon") {
			found = true
		}
	}
	if !found {
		t.Errorf("Brandon Sanderson and Sanderson, Brandon were not grouped: %v", out["names"])
	}

	asTitle := false
	for _, row := range rows(t, out["items"], "items") {
		if row["author"] == "Warbreaker" && row["title"] == "Warbreaker" {
			asTitle = true
		}
	}
	if !asTitle {
		t.Errorf("the author named after its book was not reported: %v", out["items"])
	}
}

// audit_missing description: the four ways a description says nothing.
func TestMessyDescriptions(t *testing.T) {
	out := call(t, "audit_missing", withMessy(map[string]any{"field": "description"}))

	details := map[string]string{}
	for _, row := range rows(t, out["findings"], "findings") {
		title, _ := row["title"].(string)
		details[title], _ = row["detail"].(string)
	}
	for title, want := range map[string]string{
		"The Martian": "credit line", "Artemis": "only a url", "Project Hail Mary": "stub", "The Egg": "no description",
	} {
		if !strings.Contains(details[title], want) {
			t.Errorf("%s: detail %q, want %q", title, details[title], want)
		}
	}
	if len(details) != 4 {
		t.Errorf("findings = %v, want only the four Weir books", details)
	}
}

// audit_genres: a placeholder, a placeholder glued to a value, a compound
// value, a tag repeating the genre, and a marker tag.
func TestMessyGenres(t *testing.T) {
	out := call(t, "audit_genres", messy)

	values := func(section string) []string {
		var vs []string
		for _, row := range rows(t, out[section], section) {
			v, _ := row["value"].(string)
			vs = append(vs, v)
		}
		return vs
	}
	if got := values("placeholders"); !slices.Contains(got, "Audiobook") || !slices.Contains(got, "Audiobook - Fantasy") {
		t.Errorf("placeholders = %v, want Audiobook and Audiobook - Fantasy", got)
	}
	if got := values("compound"); !slices.Equal(got, []string{"Science Fiction & Fantasy, Fantasy"}) {
		t.Errorf("compound = %v", got)
	}
	if got := values("redundant"); !slices.Equal(got, []string{"fantasy"}) {
		t.Errorf("redundant = %v, want the fantasy tag on a Fantasy book", got)
	}
	if got := strs(t, out["markers"], "markers"); !slices.Equal(got, []string{"lang:en"}) {
		t.Errorf("markers = %v", got)
	}
	// Fantasy and Science Fiction are on enough books to be genres
	if got := values("narrow"); slices.Contains(got, "Fantasy") || slices.Contains(got, "Science Fiction") {
		t.Errorf("narrow = %v: the real genres were flagged", got)
	}
}

// audit_path: the folder that names another book, and with files, the book
// whose tracks are named after another.
func TestMessyPath(t *testing.T) {
	titles := func(out map[string]any) []string {
		var ts []string
		for _, row := range rows(t, out["findings"], "findings") {
			title, _ := row["title"].(string)
			ts = append(ts, title)
		}
		return ts
	}

	folders := titles(call(t, "audit_path", messy))
	if !slices.Contains(folders, "Small Gods") {
		t.Errorf("findings = %v, want Small Gods, whose folder says Pyramids", folders)
	}
	if slices.Contains(folders, "Guards! Guards!") {
		t.Errorf("findings = %v: the folder agrees, only the files are wrong", folders)
	}

	files := titles(call(t, "audit_path", withMessy(map[string]any{"files": true})))
	if !slices.Contains(files, "Guards! Guards!") {
		t.Errorf("files=true findings = %v, want Guards! Guards!, whose files say Men at Arms", files)
	}
}

// audit_duplicates: Mort is on the shelf twice.
func TestMessyDuplicates(t *testing.T) {
	out := call(t, "audit_duplicates", messy)

	groups := rows(t, out["groups"], "groups")
	if len(groups) != 1 {
		t.Fatalf("groups = %v, want the one pair", groups)
	}
	items := rows(t, groups[0]["items"], "items")
	if len(items) != 2 || items[0]["title"] != "Mort" || items[1]["title"] != "Mort" {
		t.Errorf("group = %v, want Mort twice", items)
	}
}

// audit_covers: the one cover wears the ribbon, and the rest are missing.
func TestMessyCovers(t *testing.T) {
	out := call(t, "audit_covers", withMessy(map[string]any{"banner": true, "limit": 100}))

	if checked := num(t, out["covers_checked"], "covers_checked"); checked != 1 {
		t.Errorf("covers_checked = %d, want the one cover", checked)
	}
	findings := rows(t, out["findings"], "findings")
	problems := map[string]string{}
	for _, row := range findings {
		title, _ := row["title"].(string)
		problems[title], _ = row["problem"].(string)
	}
	if problems["Moving Pictures"] != "banner" {
		t.Errorf("Moving Pictures is %q, want banner", problems["Moving Pictures"])
	}
	if problems["Eric"] != "missing" {
		t.Errorf("Eric is %q, want missing", problems["Eric"])
	}
	if len(findings) != len(messyBooks) {
		t.Errorf("%d findings, want one per book, the ribbon plus %d missing: %v", len(findings), len(messyBooks)-1, problems)
	}
}

// audit_matched: the one matched book is a one-second file against a real
// recording, so the store disagrees with it on duration.
func TestMessyMatched(t *testing.T) {
	requireProviders(t)

	out := call(t, "audit_matched", withMessy(map[string]any{"providers": []any{"audible"}}))
	if scanned := num(t, out["items_scanned"], "items_scanned"); scanned != 1 {
		t.Errorf("items_scanned = %d, want the one book with an asin", scanned)
	}
	findings := rows(t, out["findings"], "findings")
	if len(findings) != 1 || !strings.HasPrefix(findings[0]["title"].(string), "Foundation") {
		t.Fatalf("findings = %v, want Foundation", findings)
	}
	if problems := strs(t, findings[0]["problems"], "problems"); !slices.Contains(problems, "duration_off") {
		t.Errorf("problems = %v, want duration_off", problems)
	}
}

// audit_all on the messy library runs every audit and counts what each found.
func TestMessyAuditAll(t *testing.T) {
	out := call(t, "audit_all", withMessy(map[string]any{"deep": true}))

	counts := map[string]int{}
	for _, row := range rows(t, out["audits"], "audits") {
		name, _ := row["audit"].(string)
		counts[name] = num(t, row["found"], "found")
	}
	for _, name := range []string{"audit_series", "audit_narrators", "audit_authors", "audit_genres", "audit_path", "audit_duplicates", "audit_covers", "audit_matched", "audit_missing", "audit_unmatched"} {
		if counts[name] == 0 {
			t.Errorf("%s found nothing in the messy library: %v", name, counts)
		}
	}
}
