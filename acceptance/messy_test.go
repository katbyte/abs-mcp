//go:build integration

// The Messy library: every defect the curation audits exist to find, seeded
// on purpose (the layout in scripts/abs-testenv.sh, the metadata in
// messyBooks). Each test here is one audit against it, asserting the finding
// it was seeded for and that the clean books beside it are left alone.
package acceptance

import (
	"maps"
	"slices"
	"strings"
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"
)

// messy is the library every call here names.
var messy = map[string]any{"library": "Messy"}

// withMessy adds arguments to the library.
func withMessy(extra map[string]any) map[string]any {
	args := map[string]any{"library": "Messy"}
	maps.Copy(args, extra)
	return args
}

// The scan saw everything the layout put there: the folder books, the
// three-file book, and the book that is one m4b file.
func TestMessyItems(t *testing.T) {
	if total := acc.Num(t, suite.Call(t, "library_items", withMessy(map[string]any{"limit": 1}))["total"], "total"); total != len(messyBooks) {
		t.Errorf("total = %d, want %d", total, len(messyBooks))
	}

	eric := suite.Call(t, "item_get", withMessy(map[string]any{"item": "Eric"}))
	if path := acc.Str(eric["path"]); !strings.HasSuffix(path, "Eric.m4b") {
		t.Errorf("Eric path = %q, want the m4b file itself", path)
	}
	if tracks := acc.Num(t, eric["audio_tracks"], "audio_tracks"); tracks != 1 {
		t.Errorf("Eric audio_tracks = %d, want 1", tracks)
	}
	guards := suite.Call(t, "item_get", withMessy(map[string]any{"item": "Guards! Guards!"}))
	if tracks := acc.Num(t, guards["audio_tracks"], "audio_tracks"); tracks != 3 {
		t.Errorf("Guards! Guards! audio_tracks = %d, want 3", tracks)
	}
	// two books are titled Mort, so the title alone is ambiguous
	if msg := suite.CallErr(t, "item_get", withMessy(map[string]any{"item": "Mort"})); msg == "" {
		t.Error("a title two books share should be refused rather than guessed")
	}
}

// audit_series: a gap whose missing book is on the shelf unlinked, one
// series spelled two ways, numbers padded both ways, titles that are the
// series name, and the article names when asked.
func TestMessySeries(t *testing.T) {
	out := suite.Call(t, "audit_series", withMessy(map[string]any{"articles": true}))

	t.Run("gaps", func(t *testing.T) {
		var mars map[string]any
		for _, row := range acc.Rows(t, out["gaps"], "gaps") {
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
		if missing := acc.Strs(t, mars["missing"], "missing"); !slices.Equal(missing, []string{"2"}) {
			t.Errorf("Mars Trilogy missing = %v, want [2]", missing)
		}
		unlinked := acc.Rows(t, mars["unlinked"], "unlinked")
		if len(unlinked) != 1 {
			t.Fatalf("unlinked = %v, want Green Mars", unlinked)
		}
		if path := acc.Str(unlinked[0]["path"]); !strings.Contains(path, "Green Mars") {
			t.Errorf("unlinked path = %q, want the Green Mars folder", path)
		}
		if suggest := acc.Str(unlinked[0]["suggest"]); !strings.Contains(suggest, "Mars Trilogy") {
			t.Errorf("unlinked suggest = %q, want a link into Mars Trilogy", suggest)
		}
	})

	t.Run("names", func(t *testing.T) {
		found := false
		for _, group := range acc.Rows(t, out["names"], "names") {
			var values []string
			for _, sp := range acc.Rows(t, group["spellings"], "spellings") {
				v := acc.Str(sp["value"])
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
		for _, row := range acc.Rows(t, out["numbering"], "numbering") {
			title := acc.Str(row["title"])
			switch row["problem"] {
			case "padding":
				padding[title] = acc.Str(row["suggest"])
			case "unlinked":
				unlinked = append(unlinked, title)
			case "folder_style":
				folders[title] = acc.Str(row["suggest"])
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
		for _, row := range acc.Rows(t, out["titles"], "titles") {
			title := acc.Str(row["title"])
			problems[title] = acc.Str(row["problem"])
			suggests[title] = acc.Str(row["suggest"])
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
		names := valuesIn(t, out["articles"], "articles", "name")
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
	out := suite.Call(t, "audit_narrators", messy)

	found := false
	for _, group := range acc.Rows(t, out["names"], "names") {
		var values []string
		for _, sp := range acc.Rows(t, group["spellings"], "spellings") {
			v := acc.Str(sp["value"])
			values = append(values, v)
		}
		if slices.Contains(values, "Michael Kramer") && slices.Contains(values, "Micheal Kramer") {
			found = true
		}
	}
	if !found {
		t.Errorf("Michael and Micheal Kramer were not grouped: %v", out["names"])
	}
	if msg := suite.CallErr(t, "audit_spelling", withMessy(map[string]any{"field": "narrators"})); !strings.Contains(msg, "audit_narrators") {
		t.Errorf("audit_spelling on narrators should point at audit_narrators: %s", msg)
	}
}

// audit_authors: the author spelled two ways, and the record that is a title.
func TestMessyAuthors(t *testing.T) {
	out := suite.Call(t, "audit_authors", messy)

	found := false
	for _, group := range acc.Rows(t, out["names"], "names") {
		var values []string
		for _, sp := range acc.Rows(t, group["spellings"], "spellings") {
			v := acc.Str(sp["value"])
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
	for _, row := range acc.Rows(t, out["items"], "items") {
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
	out := suite.Call(t, "audit_missing", withMessy(map[string]any{"field": "description"}))

	details := map[string]string{}
	for _, row := range acc.Rows(t, out["findings"], "findings") {
		title := acc.Str(row["title"])
		details[title] = acc.Str(row["detail"])
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
	out := suite.Call(t, "audit_genres", messy)

	values := func(section string) []string {
		return valuesIn(t, out[section], section, "value")
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
	if got := acc.Strs(t, out["markers"], "markers"); !slices.Equal(got, []string{"lang:en"}) {
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
		return valuesIn(t, out["findings"], "findings", "title")
	}

	folders := titles(suite.Call(t, "audit_path", messy))
	if !slices.Contains(folders, "Small Gods") {
		t.Errorf("findings = %v, want Small Gods, whose folder says Pyramids", folders)
	}
	if slices.Contains(folders, "Guards! Guards!") {
		t.Errorf("findings = %v: the folder agrees, only the files are wrong", folders)
	}

	files := titles(suite.Call(t, "audit_path", withMessy(map[string]any{"files": true})))
	if !slices.Contains(files, "Guards! Guards!") {
		t.Errorf("files=true findings = %v, want Guards! Guards!, whose files say Men at Arms", files)
	}
}

// audit_duplicates: Mort is on the shelf twice.
func TestMessyDuplicates(t *testing.T) {
	out := suite.Call(t, "audit_duplicates", messy)

	groups := acc.Rows(t, out["groups"], "groups")
	if len(groups) != 1 {
		t.Fatalf("groups = %v, want the one pair", groups)
	}
	items := acc.Rows(t, groups[0]["items"], "items")
	if len(items) != 2 || items[0]["title"] != "Mort" || items[1]["title"] != "Mort" {
		t.Errorf("group = %v, want Mort twice", items)
	}
}

// audit_covers: the one cover wears the ribbon, and the rest are missing.
func TestMessyCovers(t *testing.T) {
	out := suite.Call(t, "audit_covers", withMessy(map[string]any{"banner": true, "limit": 100}))

	if checked := acc.Num(t, out["covers_checked"], "covers_checked"); checked != 1 {
		t.Errorf("covers_checked = %d, want the one cover", checked)
	}
	findings := acc.Rows(t, out["findings"], "findings")
	problems := map[string]string{}
	for _, row := range findings {
		title := acc.Str(row["title"])
		problems[title] = acc.Str(row["problem"])
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

	out := suite.Call(t, "audit_matched", withMessy(map[string]any{"providers": []any{"audible"}}))
	if scanned := acc.Num(t, out["items_scanned"], "items_scanned"); scanned != 1 {
		t.Errorf("items_scanned = %d, want the one book with an asin", scanned)
	}
	findings := acc.Rows(t, out["findings"], "findings")
	if len(findings) != 1 || !strings.HasPrefix(acc.Str(findings[0]["title"]), "Foundation") {
		t.Fatalf("findings = %v, want Foundation", findings)
	}
	if problems := acc.Strs(t, findings[0]["problems"], "problems"); !slices.Contains(problems, "duration_off") {
		t.Errorf("problems = %v, want duration_off", problems)
	}
}

// audit_whitespace: the double space seeded in The Egg's subtitle, and the
// note in its folder with a space before the extension, each with the
// spaces made visible and the name put right; nothing else in Messy.
func TestMessyWhitespace(t *testing.T) {
	out := suite.Call(t, "audit_whitespace", messy)

	got := map[string]map[string]any{}
	for _, row := range acc.Rows(t, out["findings"], "findings") {
		got[acc.Str(row["where"])+"|"+acc.Str(row["problem"])] = row
	}
	want := map[string][2]string{
		"subtitle|double_space":       {"A␣␣Short Story", "A Short Story"},
		"file|space_before_extension": {"notes␣.txt", "notes.txt"},
	}
	if n := acc.Num(t, out["total_findings"], "total_findings"); n != len(want) || len(got) != len(want) {
		t.Errorf("%d findings = %v, want only %v", n, got, want)
	}
	for key, w := range want {
		row := got[key]
		if row == nil {
			t.Errorf("%s not reported: %v", key, got)
			continue
		}
		if row["text"] != w[0] || row["suggest"] != w[1] || row["title"] != "The Egg" {
			t.Errorf("%s = %v, want %q put right as %q on The Egg", key, row, w[0], w[1])
		}
	}
	if read := acc.Num(t, out["files_read"], "files_read"); read != len(messyBooks) {
		t.Errorf("files_read = %d, want every book, %d", read, len(messyBooks))
	}
}
