package tools

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit_authors is everything wrong with authors, in three parts, the way
// audit_narrators covers narrators.
//
// Items: a book whose author field holds its title, the import mistake that
// also creates a bogus author record.
//
// Records: authors never matched to Audible (no asin), with no photo, with no
// books (what a wrong match parked under a new name, or a narrator who was
// once credited as an author, leaves behind), and whose biography reads as
// someone else's. The last one is how a name lookup that tolerates a few
// letters shows up after the fact: Sarah Diemer's record opened "Sarah Miller
// began writing", Reba Buhr's "Reba Bale writes", and nothing said so.
//
// Names: the spelling detectors over the author records themselves, since a
// variant spelling of an author is a second record ("C Z Dunn" beside
// "Christian Dunn"), merged by renaming one onto the other.

type authorItemFinding struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Author string `json:"author"`
}

type authorRecordFinding struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Books       int      `json:"books"`
	ASIN        string   `json:"asin,omitempty"`
	Problems    []string `json:"problems"              jsonschema:"unmatched: no asin, author_match is the fix; no_photo: author_match if unmatched, else a photo from elsewhere with author_image_set (Audible holds none for many); no_books: nothing links to this record, author_delete it; bio_mismatch: the description opens with another person's name and never mentions this author's surname, a wrong match to undo with author_edit clear"`
	Description string   `json:"description,omitempty" jsonschema:"bio_mismatch only: how the description opens"`
}

type authorCounts struct {
	AuthorAsTitle int `json:"author_as_title"`
	Unmatched     int `json:"unmatched"`
	NoPhoto       int `json:"no_photo"`
	NoBooks       int `json:"no_books"`
	BioMismatch   int `json:"bio_mismatch"`
	Names         int `json:"names"`
}

type authorsOut struct {
	Scanned        int                   `json:"items_scanned"`
	AuthorsScanned int                   `json:"authors_scanned"`
	Found          int                   `json:"total_findings"  jsonschema:"items, records and names together, before limit"`
	Counts         authorCounts          `json:"counts"          jsonschema:"how many of each problem, before limit; a record with two problems counts in both"`
	Items          []authorItemFinding   `json:"items"           jsonschema:"books whose author field holds the title: fix with item_edit, then author_delete the stray record"`
	Records        []authorRecordFinding `json:"records"         jsonschema:"author records with something wrong, worst first: a wrong biography, then no books, then unmatched, then only the photo missing; most books first within each"`
	Names          []vocabGroup          `json:"names"           jsonschema:"author records that are one another spelled differently, cut short, or a typo apart: merge with author_edit name= or metadata_rename field=authors"`
}

// leadingName matches a description that opens with a person's name, the way
// third-person biographies do ("Sarah Miller began writing").
var leadingName = regexp.MustCompile(`^\s*\p{Lu}[\p{Ll}'\-]+(?: \p{Lu}\.?)* \p{Lu}[\p{Ll}'\-]+`)

// possessive is the 's that makes "Foster's work" not contain the word foster.
var possessive = regexp.MustCompile(`['’]s\b`)

// bioNamesSomeoneElse reports whether an author's description reads as another
// person's: it opens with a name, and the author's surname appears nowhere in
// it. A first-person biography that names nobody is left alone, and so is one
// that opens with a shouted tagline rather than a name.
func bioNamesSomeoneElse(name, description string) bool {
	d := plain(description)
	if strings.TrimSpace(d) == "" || !leadingName.MatchString(d) {
		return false
	}
	sur := surname(name)
	if sur == "" {
		return false
	}
	return !strings.Contains(" "+norm(possessive.ReplaceAllString(d, ""))+" ", " "+sur+" ")
}

// surname is the last real word of a name: not an initial, not a suffix.
func surname(name string) string {
	w := strings.Fields(norm(name))
	for len(w) > 0 && (fragments[w[len(w)-1]] || len(w[len(w)-1]) < 2) {
		w = w[:len(w)-1]
	}
	if len(w) == 0 {
		return ""
	}
	return w[len(w)-1]
}

// authorRecords gathers one library's author records into out: the findings
// and the name spellings. Podcast libraries have no authors.
func authorRecords(ctx context.Context, client *abs.Client, lib *abs.Library, out *authorsOut, names spellingCounts) error {
	if lib.IsPodcast() {
		return nil
	}
	authors, err := allAuthors(ctx, client, lib.ID, abs.ListOptions{Sort: "numBooks", Desc: true})
	if err != nil {
		return err
	}
	for j := range authors {
		a := &authors[j]
		out.AuthorsScanned++
		names.addValue("authors", a.Name, max(a.NumBooks, 1))

		f := authorRecordFinding{ID: a.ID, Name: a.Name, Books: a.NumBooks, ASIN: a.ASIN}
		if bioNamesSomeoneElse(a.Name, a.Description) {
			f.Problems = append(f.Problems, "bio_mismatch")
			f.Description = clip(plain(a.Description), 160)
			out.Counts.BioMismatch++
		}
		if a.NumBooks == 0 {
			f.Problems = append(f.Problems, "no_books")
			out.Counts.NoBooks++
		}
		if a.ASIN == "" {
			f.Problems = append(f.Problems, "unmatched")
			out.Counts.Unmatched++
		}
		if a.ImagePath == "" {
			f.Problems = append(f.Problems, "no_photo")
			out.Counts.NoPhoto++
		}
		if len(f.Problems) > 0 {
			out.Records = append(out.Records, f)
		}
	}

	return nil
}

// recordRank orders the records: the worst problem first, then most books.
func recordRank(f authorRecordFinding) int {
	switch f.Problems[0] {
	case "bio_mismatch":
		return 0
	case "no_books":
		return 1
	case "unmatched":
		return 2
	}
	return 3
}

func sortAuthorRecords(records []authorRecordFinding) {
	slices.SortFunc(records, func(x, y authorRecordFinding) int {
		if rx, ry := recordRank(x), recordRank(y); rx != ry {
			return rx - ry
		}
		if x.Books != y.Books {
			return y.Books - x.Books
		}
		return strings.Compare(x.Name, y.Name)
	})
}

func registerAuthorAudit(r *registry) {
	client := r.client

	type authorsIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum rows per section, default 100, at most 1000"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_authors",
		Description: "Everything wrong with authors. Items: a book whose author field holds its title (fix with item_edit, then author_delete the stray record). " +
			"Records: authors never matched (no asin: author_match), with no photo (author_match, or author_image_set with a photo from elsewhere when Audible has none), with no books (author_delete), or whose biography opens with someone else's name and never mentions theirs, a wrong match to undo with author_edit clear=[asin, description, image]. " +
			"Names: author records that are one another spelled differently, cut short or a typo apart, merged by author_edit name= onto the one to keep. counts says how many of each; the sections are worst first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in authorsIn) (*mcp.CallToolResult, authorsOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, authorsOut{}, err
		}
		out := authorsOut{Items: []authorItemFinding{}, Records: []authorRecordFinding{}, Names: []vocabGroup{}}
		names := newSpellingCounts([]string{"authors"})
		check := auditChecksByName["author_as_title"]
		for i := range libs {
			lib := &libs[i]
			if err := client.ItemsAll(ctx, lib.ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
				for j := range items {
					it := &items[j]
					out.Scanned++
					if _, suspect := check(it); suspect {
						out.Items = append(out.Items, authorItemFinding{ID: it.ID, Title: it.Media.Metadata.Title, Author: it.Media.Metadata.AuthorName})
					}
				}
				return true
			}); err != nil {
				return nil, authorsOut{}, err
			}
			if err := authorRecords(ctx, client, lib, &out, names); err != nil {
				return nil, authorsOut{}, err
			}
		}
		sortAuthorRecords(out.Records)
		allNames := names.report("authors")
		out.Counts.AuthorAsTitle = len(out.Items)
		out.Counts.Names = len(allNames)
		out.Found = len(out.Items) + len(out.Records) + len(allNames)

		limit := auditLimit(in.Limit, 100)
		out.Items = out.Items[:min(len(out.Items), limit)]
		out.Records = out.Records[:min(len(out.Records), limit)]
		out.Names = append(out.Names, allNames[:min(len(allNames), limit)]...)

		return nil, out, nil
	})
}
