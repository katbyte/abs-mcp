//go:build integration

// Journey 2: every audit a tool can fix, fixed the way a session fixes it.
// Each loop audits, applies the fix the finding points at (its suggest, its
// keep, the tool its description names), audits again to see the finding
// gone, then puts the defect back and audits a third time to see it return,
// so the fixtures the rest of the suite asserts on are left as they were.
package acceptance

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// writeJPEG writes a square test image of n pixels.
func writeJPEG(t *testing.T, name string, n int) {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 120, 255})
		}
	}
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// suggestCall turns a finding's suggest string ("metadata_rename
// field=genres from=\"Audiobook - Fantasy\" to=Fantasy") into the tool and
// arguments it names.
func suggestCall(t *testing.T, suggest string) (string, map[string]any) {
	t.Helper()

	tool, rest, ok := strings.Cut(strings.TrimSpace(suggest), " ")
	if !ok {
		t.Fatalf("suggest %q names no arguments", suggest)
	}
	args := map[string]any{}
	for _, m := range regexp.MustCompile(`(\w+)=("(?:[^"\\]|\\.)*"|\S+)`).FindAllStringSubmatch(rest, -1) {
		value := m[2]
		if strings.HasPrefix(value, `"`) {
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				t.Fatalf("suggest %q: %v", suggest, err)
			}
			value = unquoted
		}
		switch value {
		case "true":
			args[m[1]] = true
		case "false":
			args[m[1]] = false
		default:
			args[m[1]] = value
		}
	}

	return tool, args
}

// titlesIn lists the title of every row of a findings section.
func titlesIn(t *testing.T, v any, field string) []string {
	t.Helper()

	if v == nil {
		return nil
	}
	var out []string
	for _, row := range rows(t, v, field) {
		title, _ := row["title"].(string)
		out = append(out, title)
	}
	return out
}

// valuesIn lists a key of every row of a findings section.
func valuesIn(t *testing.T, v any, field, key string) []string {
	t.Helper()

	if v == nil {
		return nil
	}
	var out []string
	for _, row := range rows(t, v, field) {
		s, _ := row[key].(string)
		out = append(out, s)
	}
	return out
}

// spellingsIn lists, per group of a names section, the spellings it holds.
func spellingsIn(t *testing.T, v any) [][]string {
	t.Helper()

	if v == nil {
		return nil
	}
	var out [][]string
	for _, group := range rows(t, v, "names") {
		out = append(out, valuesIn(t, group["spellings"], "spellings", "value"))
	}
	return out
}

// hasGroup reports whether a names section has a group holding both values.
func hasGroup(groups [][]string, a, b string) bool {
	return slices.ContainsFunc(groups, func(g []string) bool { return slices.Contains(g, a) && slices.Contains(g, b) })
}

// loop is one audit-fix-audit-restore journey.
type loop struct {
	audit   func(t *testing.T) bool // is the defect reported?
	fix     func(t *testing.T)
	putBack func(t *testing.T)
}

func (l loop) run(t *testing.T) {
	t.Helper()

	if !l.audit(t) {
		t.Fatal("the defect is not reported before the fix")
	}
	restored := false
	t.Cleanup(func() {
		if !restored {
			l.putBack(t)
		}
	})
	l.fix(t)
	if l.audit(t) {
		t.Error("the finding is still reported after the fix it points at")
	}
	l.putBack(t)
	restored = true
	if !l.audit(t) {
		t.Error("putting the defect back did not bring the finding back")
	}
}

// messyID is the id of the Messy book at a folder: two of them share a title,
// so a title is not enough.
func messyID(t *testing.T, relPath string) string {
	t.Helper()

	for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy", "limit": 100})["items"], "items") {
		if it["path"] == relPath {
			id, _ := it["id"].(string)
			return id
		}
	}
	t.Fatalf("no messy book at %s", relPath)
	return ""
}

func TestJourneyEveryFixableAudit(t *testing.T) {
	if !ready {
		t.Skip("ABS_SERVER and ABS_TOKEN are not set")
	}

	messyAudit := func(tool string, extra map[string]any) map[string]any {
		return call(t, tool, withMessy(extra))
	}

	t.Run("a stub description, filled", func(t *testing.T) {
		loop{
			audit: func(t *testing.T) bool {
				return slices.Contains(titlesIn(t, messyAudit("audit_missing", map[string]any{"field": "description"})["findings"], "findings"), "The Martian")
			},
			fix: func(t *testing.T) {
				call(t, "item_edit", map[string]any{"library": "Messy", "item": "The Martian", "description": filler})
			},
			putBack: func(t *testing.T) {
				call(t, "item_edit", map[string]any{"library": "Messy", "item": "The Martian", "description": "Read by R. C. Bray"})
			},
		}.run(t)
	})

	t.Run("a missing year, set", func(t *testing.T) {
		missing := func(t *testing.T) bool {
			return slices.Contains(titlesIn(t, messyAudit("audit_missing", map[string]any{"field": "year"})["findings"], "findings"), "Reaper Man")
		}
		loop{
			audit: missing,
			fix: func(t *testing.T) {
				call(t, "item_edit", map[string]any{"library": "Messy", "item": "Reaper Man", "year": "1991"})
			},
			putBack: func(t *testing.T) {
				call(t, "item_edit", map[string]any{"library": "Messy", "item": "Reaper Man", "clear": []any{"year"}})
			},
		}.run(t)
	})

	t.Run("a gap whose book is on the shelf, linked", func(t *testing.T) {
		var greenMars, suggest string
		audit := func(t *testing.T) bool {
			for _, gap := range rows(t, messyAudit("audit_series", nil)["gaps"], "gaps") {
				if gap["name"] != "Mars Trilogy" {
					continue
				}
				if gap["unlinked"] == nil {
					return true
				}
				unlinked := rows(t, gap["unlinked"], "unlinked")
				greenMars, _ = unlinked[0]["id"].(string)
				suggest, _ = unlinked[0]["suggest"].(string)
				return true
			}
			return false
		}
		loop{
			audit: audit,
			fix: func(t *testing.T) {
				if greenMars == "" || suggest == "" {
					t.Fatal("the gap names no unlinked book to link")
				}
				call(t, "item_edit", map[string]any{"item": greenMars, "add_series": []any{suggest}})
			},
			putBack: func(t *testing.T) {
				call(t, "item_edit", map[string]any{"library": "Messy", "item": "Green Mars", "remove_series": []any{"Mars Trilogy"}})
			},
		}.run(t)
		if series := call(t, "item_get", map[string]any{"library": "Messy", "item": "Green Mars"})["series"]; series != nil {
			t.Errorf("Green Mars series after the put back = %v, want none", series)
		}
	})

	t.Run("one series spelled two ways, merged", func(t *testing.T) {
		books := map[string]string{} // title -> id of the two filed under the other spelling
		for _, b := range messyBooks {
			if len(b.Series) > 0 && strings.HasPrefix(b.Series[0], "Wheel of Time #") {
				books[b.Title] = itemID(t, "Messy", b.Title)
			}
		}
		var keep, other string
		loop{
			audit: func(t *testing.T) bool {
				for _, group := range rows(t, messyAudit("audit_series", nil)["names"], "names") {
					spellings := valuesIn(t, group["spellings"], "spellings", "value")
					if slices.Contains(spellings, "The Wheel of Time") && slices.Contains(spellings, "Wheel of Time") {
						keep, _ = group["keep"].(string)
						other = spellings[0]
						if other == keep {
							other = spellings[1]
						}
						return true
					}
				}
				return false
			},
			fix: func(t *testing.T) {
				out := call(t, "series_merge", map[string]any{"library": "Messy", "from": other, "into": keep})
				if moved := num(t, out["moved"], "moved"); moved != 2 {
					t.Errorf("moved = %d, want 2", moved)
				}
				got := call(t, "series_get", map[string]any{"library": "Messy", "series": keep})
				if n := len(rows(t, got["books"], "books")); n != 4 {
					t.Errorf("%s holds %d books after the merge, want 4", keep, n)
				}
			},
			putBack: func(t *testing.T) {
				for title, id := range books {
					for _, b := range messyBooks {
						if b.Title == title {
							call(t, "item_edit", map[string]any{"item": id, "series": toAny(b.Series)})
						}
					}
				}
			},
		}.run(t)
	})

	t.Run("an unpadded number, padded", func(t *testing.T) {
		var suggest string
		loop{
			audit: func(t *testing.T) bool {
				for _, row := range rows(t, messyAudit("audit_series", nil)["numbering"], "numbering") {
					if row["title"] == "The Light Fantastic" && row["problem"] == "padding" {
						suggest, _ = row["suggest"].(string)
						return true
					}
				}
				return false
			},
			fix: func(t *testing.T) {
				call(t, "item_edit", map[string]any{"library": "Messy", "item": "The Light Fantastic", "add_series": []any{suggest}})
				if series := strs(t, call(t, "item_get", map[string]any{"library": "Messy", "item": "The Light Fantastic"})["series"], "series"); !slices.Equal(series, []string{"Discworld #02"}) {
					t.Errorf("series = %v, want [Discworld #02]", series)
				}
			},
			putBack: func(t *testing.T) {
				call(t, "item_edit", map[string]any{"library": "Messy", "item": "The Light Fantastic", "add_series": []any{"Discworld #2"}})
			},
		}.run(t)
	})

	t.Run("a title that is the series name, retitled", func(t *testing.T) {
		id := messyID(t, "George R. R. Martin/A Song of Ice and Fire - 01 - A Game of Thrones")
		var suggest string
		loop{
			audit: func(t *testing.T) bool {
				for _, row := range rows(t, messyAudit("audit_series", nil)["titles"], "titles") {
					if row["id"] == id {
						suggest, _ = row["suggest"].(string)
						return true
					}
				}
				return false
			},
			fix: func(t *testing.T) {
				call(t, "item_edit", map[string]any{"item": id, "title": suggest})
			},
			putBack: func(t *testing.T) {
				call(t, "item_edit", map[string]any{"item": id, "title": "A Song of Ice and Fire"})
			},
		}.run(t)
	})

	t.Run("a folder naming another book, retitled", func(t *testing.T) {
		id := messyID(t, "Terry Pratchett/Discworld - 07 - Pyramids")
		loop{
			audit: func(t *testing.T) bool {
				return slices.Contains(titlesIn(t, messyAudit("audit_path", nil)["findings"], "findings"), "Small Gods")
			},
			fix: func(t *testing.T) {
				call(t, "item_edit", map[string]any{"item": id, "title": "Pyramids"})
				if got := titlesIn(t, messyAudit("audit_path", nil)["findings"], "findings"); slices.Contains(got, "Pyramids") {
					t.Errorf("the corrected title is still a finding: %v", got)
				}
			},
			putBack: func(t *testing.T) {
				call(t, "item_edit", map[string]any{"item": id, "title": "Small Gods"})
			},
		}.run(t)
	})

	t.Run("a narrator spelled two ways, renamed onto the one to keep", func(t *testing.T) {
		var keep, other string
		loop{
			audit: func(t *testing.T) bool {
				for _, group := range rows(t, messyAudit("audit_narrators", nil)["names"], "names") {
					spellings := valuesIn(t, group["spellings"], "spellings", "value")
					if slices.Contains(spellings, "Michael Kramer") && slices.Contains(spellings, "Micheal Kramer") {
						keep, _ = group["keep"].(string)
						other = spellings[0]
						if other == keep {
							other = spellings[1]
						}
						return true
					}
				}
				return false
			},
			fix: func(t *testing.T) {
				out := call(t, "metadata_rename", map[string]any{"field": "narrators", "library": "Messy", "from": other, "to": keep})
				if n := num(t, out["items_updated"], "items_updated"); n != 2 {
					t.Errorf("items_updated = %d, want the 2 books spelled %s", n, other)
				}
			},
			putBack: func(t *testing.T) {
				for _, b := range messyBooks {
					if slices.Contains(b.Narrators, "Micheal Kramer") {
						call(t, "item_edit", map[string]any{"item": messyID(t, b.Path), "narrators": toAny(b.Narrators)})
					}
				}
			},
		}.run(t)
	})

	t.Run("an author spelled two ways, merged by rename", func(t *testing.T) {
		loop{
			audit: func(t *testing.T) bool {
				return hasGroup(spellingsIn(t, messyAudit("audit_authors", nil)["names"]), "Brandon Sanderson", "Sanderson, Brandon")
			},
			fix: func(t *testing.T) {
				out := call(t, "author_edit", map[string]any{"library": "Messy", "author": "Sanderson, Brandon", "name": "Brandon Sanderson"})
				if merged, _ := out["merged"].(bool); !merged {
					t.Errorf("merged = %v, want the rename to merge into the existing record", out["merged"])
				}
				// the row after a merge counts the books the record now has
				author, _ := out["author"].(map[string]any)
				if books := num(t, author["books"], "books"); books != 3 {
					t.Errorf("books = %d after the merge, want 3", books)
				}
			},
			putBack: func(t *testing.T) {
				call(t, "item_edit", map[string]any{"library": "Messy", "item": "Oathbringer", "authors": []any{"Sanderson, Brandon"}})
			},
		}.run(t)
	})

	t.Run("an author field holding the title, corrected", func(t *testing.T) {
		loop{
			audit: func(t *testing.T) bool {
				return slices.Contains(titlesIn(t, messyAudit("audit_authors", nil)["items"], "items"), "Warbreaker")
			},
			fix: func(t *testing.T) {
				call(t, "item_edit", map[string]any{"library": "Messy", "item": "Warbreaker", "authors": []any{"Brandon Sanderson"}})
			},
			putBack: func(t *testing.T) {
				call(t, "item_edit", map[string]any{"library": "Messy", "item": "Warbreaker", "authors": []any{"Warbreaker"}})
			},
		}.run(t)
	})

	// the genre findings each carry the metadata_rename call that fixes them
	genreLoop := func(section, value string, putBack func(t *testing.T)) func(t *testing.T) {
		return func(t *testing.T) {
			var finding map[string]any
			loop{
				audit: func(t *testing.T) bool {
					out := messyAudit("audit_genres", nil)
					if out[section] == nil {
						return false
					}
					for _, row := range rows(t, out[section], section) {
						if row["value"] == value {
							finding = row
							return true
						}
					}
					return false
				},
				fix: func(t *testing.T) {
					switch suggest := finding["suggest"].(type) {
					case string:
						tool, args := suggestCall(t, suggest)
						// a suggested remove only previews until confirmed
						if args["remove"] == true {
							args["confirm"] = true
						}
						call(t, tool, args)
					case map[string]any:
						field, _ := finding["field"].(string)
						call(t, "metadata_rename", map[string]any{"field": field, "from": value, "split": suggest})
					default:
						t.Fatalf("%s %q carries no suggest: %v", section, value, finding)
					}
				},
				putBack: putBack,
			}.run(t)
		}
	}
	t.Run("a placeholder genre, removed", genreLoop("placeholders", "Audiobook", func(t *testing.T) {
		call(t, "item_edit", map[string]any{"library": "Messy", "item": "The Colour of Magic", "genres": []any{"Audiobook"}})
	}))
	t.Run("a placeholder glued to a genre, renamed", genreLoop("placeholders", "Audiobook - Fantasy", func(t *testing.T) {
		call(t, "item_edit", map[string]any{"library": "Messy", "item": "The Light Fantastic", "genres": []any{"Audiobook - Fantasy"}})
	}))
	t.Run("a compound genre, split", func(t *testing.T) {
		mort := messyID(t, "Terry Pratchett/Discworld - 04 - Mort")
		genreLoop("compound", "Science Fiction & Fantasy, Fantasy", func(t *testing.T) {
			call(t, "item_edit", map[string]any{"library": "Messy", "item": "Equal Rites", "genres": []any{"Science Fiction & Fantasy, Fantasy"}, "clear": []any{"tags"}})
		})(t)
		// the split touched only the book carrying the compound: Fantasy is
		// still a genre on the books that had it
		if genres := strs(t, call(t, "item_get", map[string]any{"item": mort})["genres"], "genres"); !slices.Equal(genres, []string{"Fantasy"}) {
			t.Errorf("Mort's genres = %v, want [Fantasy]", genres)
		}
	})
	t.Run("a tag repeating the genre, removed", func(t *testing.T) {
		mort := messyID(t, "Terry Pratchett/Discworld - 04 - Mort")
		genreLoop("redundant", "fantasy", func(t *testing.T) {
			call(t, "item_edit", map[string]any{"item": mort, "tags": []any{"fantasy"}})
		})(t)
	})

	t.Run("a language spelled two ways, renamed", func(t *testing.T) {
		// not seeded: made here, and the fix is what restores it
		t.Cleanup(func() {
			call(t, "item_edit", map[string]any{"library": "Fiction", "item": "Leviathan Wakes", "language": "English"})
		})
		call(t, "item_edit", map[string]any{"library": "Fiction", "item": "Leviathan Wakes", "language": "eng"})
		spelled := func(t *testing.T) bool {
			for _, group := range rows(t, call(t, "audit_spelling", map[string]any{"library": "Fiction", "field": "languages"})["groups"], "groups") {
				if slices.Contains(valuesIn(t, group["spellings"], "spellings", "value"), "eng") {
					return true
				}
			}
			return false
		}
		if !spelled(t) {
			t.Fatal("eng beside English is not reported")
		}
		out := call(t, "metadata_rename", map[string]any{"field": "languages", "library": "Fiction", "from": "eng", "to": "English"})
		if items := strs(t, out["items"], "items"); !slices.Equal(items, []string{"Leviathan Wakes"}) {
			t.Errorf("items = %v, want [Leviathan Wakes]", items)
		}
		if spelled(t) {
			t.Error("the spelling is still reported after the rename")
		}
	})

	t.Run("a duplicate, deleted and scanned back", func(t *testing.T) {
		var outside string
		audit := func(t *testing.T) bool {
			for _, group := range rows(t, messyAudit("audit_duplicates", nil)["groups"], "groups") {
				for _, it := range rows(t, group["items"], "items") {
					if it["path"] == "Terry Pratchett/Mort" {
						outside, _ = it["id"].(string)
						return true
					}
				}
			}
			return false
		}
		loop{
			audit: audit,
			fix: func(t *testing.T) {
				// pick the copy outside the series and delete its record; the
				// folder stays on disk
				call(t, "item_delete", map[string]any{"confirm": true, "item": outside})
				if err := waitForItems("Messy", len(messyBooks)-1); err != nil {
					t.Fatal(err)
				}
			},
			putBack: func(t *testing.T) {
				call(t, "library_scan", map[string]any{"library": "Messy"})
				if err := waitForItems("Messy", len(messyBooks)); err != nil {
					t.Fatal(err)
				}
				waitIdle(t)
				for _, b := range messyBooks {
					if b.Path != "Terry Pratchett/Mort" {
						continue
					}
					var id string
					for _, it := range rows(t, call(t, "library_items", map[string]any{"library": "Messy", "limit": 100})["items"], "items") {
						if it["path"] == b.Path {
							id, _ = it["id"].(string)
						}
					}
					if id == "" {
						t.Fatal("the scan did not bring the folder back")
					}
					call(t, "item_edit", map[string]any{
						"item": id, "title": b.Title, "authors": []any{b.Author}, "narrators": toAny(b.Narrators),
						"genres": toAny(b.Genres), "description": b.Description, "language": "English", "clear": []any{"series"},
					})
				}
			},
		}.run(t)
	})

	t.Run("a ribboned cover, replaced", func(t *testing.T) {
		requireProviders(t)

		covers := strs(t, call(t, "item_cover_search", map[string]any{
			"item": "Foundation and Empire", "provider": "audible", "title": "Foundation and Empire", "author": "Isaac Asimov",
		})["covers"], "covers")
		if len(covers) == 0 {
			t.Skip("no recorded cover to replace it with")
		}
		folder, _ := call(t, "item_get", map[string]any{"library": "Messy", "item": "Moving Pictures"})["full_path"].(string)
		loop{
			audit: func(t *testing.T) bool {
				for _, row := range rows(t, messyAudit("audit_covers", map[string]any{"banner": true, "limit": 100})["findings"], "findings") {
					if row["title"] == "Moving Pictures" && row["problem"] == "banner" {
						return true
					}
				}
				return false
			},
			fix: func(t *testing.T) {
				call(t, "item_cover_edit", map[string]any{"library": "Messy", "item": "Moving Pictures", "url": covers[0]})
			},
			putBack: func(t *testing.T) {
				call(t, "item_cover_edit", map[string]any{"library": "Messy", "item": "Moving Pictures", "file": path.Join(folder, "cover.jpg")})
			},
		}.run(t)
	})

	t.Run("a cover too small, replaced", func(t *testing.T) {
		data := dataDir()
		if data == "" {
			t.Skip("ABS_TEST_DATA is not set")
		}
		// the provider proxy replays artwork as a two-pixel placeholder, which
		// is as small as covers come, so the bigger cover is one written into
		// the book's own folder
		const book = "War Is a Racket"
		it := call(t, "item_get", map[string]any{"library": "Non-Fiction", "item": book})
		folder, _ := it["full_path"].(string)
		rel, _ := it["path"].(string)
		big := filepath.Join(data, "nonfiction", rel, "zzyzx-bigger.jpg")
		writeJPEG(t, big, 600)
		t.Cleanup(func() {
			if err := os.Remove(big); err != nil {
				t.Errorf("removing %s: %v", big, err)
			}
		})
		loop{
			audit: func(t *testing.T) bool {
				for _, row := range rows(t, call(t, "audit_covers", map[string]any{"library": "Non-Fiction"})["findings"], "findings") {
					if row["title"] == book && row["problem"] == "small" {
						return true
					}
				}
				return false
			},
			fix: func(t *testing.T) {
				call(t, "item_cover_edit", map[string]any{"library": "Non-Fiction", "item": book, "file": path.Join(folder, "zzyzx-bigger.jpg")})
			},
			putBack: func(t *testing.T) {
				call(t, "item_cover_edit", map[string]any{"library": "Non-Fiction", "item": book, "file": path.Join(folder, "cover.jpg")})
			},
		}.run(t)
	})

	t.Run("a missing cover, set", func(t *testing.T) {
		requireProviders(t)

		covers := strs(t, call(t, "item_cover_search", map[string]any{
			"item": "Foundation and Empire", "provider": "audible", "title": "Foundation and Empire", "author": "Isaac Asimov",
		})["covers"], "covers")
		if len(covers) == 0 {
			t.Skip("no recorded cover")
		}
		loop{
			audit: func(t *testing.T) bool {
				return slices.Contains(titlesIn(t, call(t, "audit_missing", map[string]any{"library": "Fiction", "field": "cover"})["findings"], "findings"), "Foundation and Empire")
			},
			fix: func(t *testing.T) {
				call(t, "item_cover_edit", map[string]any{"library": "Fiction", "item": "Foundation and Empire", "url": covers[0]})
			},
			putBack: func(t *testing.T) {
				call(t, "item_cover_edit", map[string]any{"library": "Fiction", "item": "Foundation and Empire", "remove": true})
			},
		}.run(t)
	})

	t.Run("an unmatched author, matched", func(t *testing.T) {
		requireProviders(t)

		loop{
			audit: func(t *testing.T) bool {
				for _, row := range rows(t, call(t, "audit_authors", map[string]any{"library": "Fiction"})["records"], "records") {
					if row["name"] == "Isaac Asimov" {
						return slices.Contains(strs(t, row["problems"], "problems"), "unmatched")
					}
				}
				return false
			},
			fix: func(t *testing.T) {
				cand, _ := call(t, "author_match", map[string]any{"library": "Fiction", "author": "Isaac Asimov", "query": "Isaac Asimov"})["candidate"].(map[string]any)
				asin, _ := cand["asin"].(string)
				if asin == "" {
					t.Fatal("author_match found no one")
				}
				call(t, "author_match_apply", map[string]any{"library": "Fiction", "author": "Isaac Asimov", "asin": asin, "region": "us"})
			},
			putBack: func(t *testing.T) {
				call(t, "author_edit", map[string]any{"library": "Fiction", "author": "Isaac Asimov", "clear": []any{"description", "asin", "image"}})
			},
		}.run(t)
	})

	t.Run("an unmatched book, matched", func(t *testing.T) {
		requireProviders(t)

		const book = "War Is a Racket"
		before := call(t, "item_get", map[string]any{"library": "Non-Fiction", "item": book})
		loop{
			audit: func(t *testing.T) bool {
				return slices.Contains(titlesIn(t, call(t, "audit_unmatched", map[string]any{"library": "Non-Fiction"})["findings"], "findings"), book)
			},
			fix: func(t *testing.T) {
				candidates := rows(t, call(t, "item_match", map[string]any{
					"library": "Non-Fiction", "item": book, "provider": "audible", "title": book, "author": "Smedley D. Butler",
				})["candidates"], "candidates")
				if len(candidates) == 0 {
					t.Fatal("item_match found no candidate")
				}
				asin, _ := candidates[0]["asin"].(string)
				out := call(t, "item_match_apply", map[string]any{"library": "Non-Fiction", "item": book, "provider": "audible", "asin": asin})
				if updated, _ := out["updated"].(bool); !updated {
					t.Errorf("item_match_apply reported no update: %v", out)
				}
			},
			putBack: func(t *testing.T) {
				call(t, "item_edit", map[string]any{
					"library": "Non-Fiction", "item": book, "clear": []any{"asin", "isbn", "description", "subtitle", "series"},
					"tags": toAny(strs(t, before["tags"], "tags")), "genres": toAny(strs(t, before["genres"], "genres")),
					"publisher": before["publisher"], "year": before["year"], "narrators": []any{before["narrator"]},
					"authors": []any{before["author"]}, "title": book,
				})
				// the cover.jpg in the folder is still the book's
				folder, _ := before["full_path"].(string)
				call(t, "item_cover_edit", map[string]any{"library": "Non-Fiction", "item": book, "file": path.Join(folder, "cover.jpg")})
			},
		}.run(t)
		after := call(t, "item_get", map[string]any{"library": "Non-Fiction", "item": book})
		for _, key := range []string{"title", "author", "narrator", "publisher", "year", "language"} {
			if after[key] != before[key] {
				t.Errorf("%s after the put back = %v, want %v", key, after[key], before[key])
			}
		}
	})

	waitIdle(t)
}
