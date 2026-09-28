package tools

import (
	"slices"
	"testing"
)

// Every author problem in one place: the item whose author is its title, the
// records with no asin, no photo or no books, the biography that belongs to
// someone else, and two records that are one name spelled two ways.
func TestAuditAuthors(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(
		item("i1", "Dune", `"authorName":"Frank Herbert"`, ""),
		item("i2", "Neuromancer", `"authorName":"Neuromancer"`, ""),
		item("i3", "The Silent War", `"authorName":"C Z Dunn"`, ""),
	))
	f.json("GET /api/libraries/"+libID+"/authors", `{"results":[
		{"id":"a1","name":"Frank Herbert","asin":"B1","imagePath":"/a.jpg","numBooks":1,"description":"Frank Herbert was an American science fiction author."},
		{"id":"a2","name":"Sarah Diemer","asin":"B2","imagePath":"/b.jpg","numBooks":1,"description":"Sarah Miller began writing her first novel at the age of ten."},
		{"id":"a3","name":"Cat Hellisen","asin":"B3","imagePath":"/c.jpg","numBooks":1,"description":"I like mucking about with words. I live near a sea."},
		{"id":"a4","name":"Shayna Small","asin":"B4","imagePath":"/d.jpg","numBooks":0,"description":"Shayna Small is a narrator."},
		{"id":"a5","name":"Christian Dunn","asin":"B5","imagePath":"/e.jpg","numBooks":1},
		{"id":"a6","name":"C Z Dunn","asin":"B6","numBooks":1},
		{"id":"a7","name":"Neuromancer","numBooks":1},
		{"id":"a8","name":"Chris Wraight","asin":"B8","numBooks":8}
	],"total":8}`)
	call := toolCaller(t, f)

	out, err := call("audit_authors", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := num(t, out["items_scanned"]); got != 3 {
		t.Errorf("items_scanned = %d", got)
	}
	if got := num(t, out["authors_scanned"]); got != 8 {
		t.Errorf("authors_scanned = %d", got)
	}
	counts, ok := out["counts"].(map[string]any)
	if !ok {
		t.Fatalf("counts = %v, want an object", out["counts"])
	}
	for k, want := range map[string]int{"author_as_title": 1, "unmatched": 1, "no_photo": 3, "no_books": 1, "bio_mismatch": 1, "names": 1} {
		if got := num(t, counts[k]); got != want {
			t.Errorf("counts.%s = %d, want %d", k, got, want)
		}
	}
	items := list(t, out["items"])
	if len(items) != 1 || str(t, items[0]["title"]) != "Neuromancer" {
		t.Errorf("items = %v, want the book whose author is its title", items)
	}

	records := list(t, out["records"])
	// worst first: the wrong biography, the empty record, the unmatched one,
	// then photos only, most books first
	wantOrder := []string{"Sarah Diemer", "Shayna Small", "Neuromancer", "Chris Wraight", "C Z Dunn"}
	gotOrder := make([]string, 0, len(records))
	for _, r := range records {
		gotOrder = append(gotOrder, str(t, r["name"]))
	}
	if !slices.Equal(gotOrder, wantOrder) {
		t.Errorf("records = %v, want %v", gotOrder, wantOrder)
	}
	probs := func(name string) []string {
		for _, r := range records {
			if str(t, r["name"]) != name {
				continue
			}
			raw, ok := r["problems"].([]any)
			if !ok {
				t.Fatalf("%s problems = %v, want a list", name, r["problems"])
			}
			ps := make([]string, 0, len(raw))
			for _, p := range raw {
				ps = append(ps, str(t, p))
			}
			return ps
		}
		return nil
	}
	if got := probs("Sarah Diemer"); !slices.Equal(got, []string{"bio_mismatch"}) {
		t.Errorf("Sarah Diemer problems = %v", got)
	}
	if got := probs("Neuromancer"); !slices.Equal(got, []string{"unmatched", "no_photo"}) {
		t.Errorf("Neuromancer problems = %v", got)
	}
	if got := probs("Cat Hellisen"); got != nil {
		t.Errorf("a first-person biography that names nobody was flagged: %v", got)
	}
	if got := probs("Frank Herbert"); got != nil {
		t.Errorf("a biography that names its author was flagged: %v", got)
	}

	names := list(t, out["names"])
	if len(names) != 1 || str(t, names[0]["kind"]) != "contains" || str(t, names[0]["keep"]) != "Christian Dunn" {
		t.Errorf("names = %v, want C Z Dunn contained in Christian Dunn", names)
	}

	if _, err := call("audit_spelling", map[string]any{"field": "authors"}); err == nil {
		t.Error("audit_spelling took authors, which audit_authors owns")
	}
}

func TestBioNamesSomeoneElse(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, bio string
		want      bool
	}{
		{"Sarah Diemer", "Sarah Miller began writing her first novel at the age of ten.", true},
		{"Reba Buhr", "Reba Bale writes contemporary lesbian romance.", true},
		{"Frank Herbert", "Frank Herbert was an American author.", false},
		{"Anne Holt", "Anne Holt is Norway's bestselling crime writer.", false},
		{"L.L. Raand", "Radclyffe has written many novels and, writing as L.L. Raand, a paranormal series.", false},
		{"Cat Hellisen", "I like mucking about with words.", false},
		{"Patrick Lencioni", "New York Times bestselling author Patrick Lencioni founded The Table Group.", false},
		{"Jane Doe", "New York Times bestselling author John Smith founded a firm.", true},
		{"Fuse", "", false},
		{"Martin Luther King Jr.", "Martin Luther King was a minister.", false},
		{"Alan Dean Foster", "Alan Dean Foster's work to date includes hard science fiction.", false},
		{"Carsen Taite", "Carsen Taite’s goal as an author is to spin tales.", false},
		{"Jo Nesbø", "Jo Nesbo is one of the world's bestselling crime writers.", false},
		{"Amanda Kay", "SHATTERING HEARTS IN THE NAME OF HAPPILY EVER AFTER I am a romance author.", false},
		{"Rachel Botchan", "Rachel Bateman grew up with an abundance of sisters.", true},
		{"Tyler Art", "Tyler Grant is the author of dozens of comedy stories.", true},
	} {
		if got := bioNamesSomeoneElse(tc.name, tc.bio); got != tc.want {
			t.Errorf("bioNamesSomeoneElse(%q, %q) = %v, want %v", tc.name, tc.bio, got, tc.want)
		}
	}
}
