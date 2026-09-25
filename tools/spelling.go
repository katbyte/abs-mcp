package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// vocabFields are the free-text fields worth normalizing. Every one of them is
// typed by hand or filled by an importer, so the same thing arrives spelled
// several ways: "Jim Dale" and "jim dale", "Sci-Fi" and "sci fi", "en" and
// "eng" and "English". audit_spelling finds the variants and metadata_rename
// merges them, and both take the field by these names.
var vocabFields = []string{"genres", "tags", "narrators", "languages", "publishers", "authors"}

// spellingFields are the ones audit_spelling reports on: the vocabulary. People
// have audits of their own, audit_authors and audit_narrators, which run the
// same detectors on their names beside checks only a person needs.
var spellingFields = []string{"genres", "tags", "languages", "publishers"}

// vocabField maps what a caller wrote (tag, Tags, narrator...) onto the name
// in vocabFields, or returns "" for anything else.
func vocabField(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s != "" && !strings.HasSuffix(s, "s") {
		s += "s"
	}
	if slices.Contains(vocabFields, s) {
		return s
	}

	return ""
}

// languageAliases folds the ISO 639-1 and 639-2 codes and the English names of
// the languages a library is likely to hold onto one key, so that en, eng,
// English and english cluster together. Anything not here is compared on its
// normalized spelling alone and reported as unrecognized.
var languageAliases = map[string]string{
	"en": "en", "eng": "en", "english": "en",
	"de": "de", "ger": "de", "deu": "de", "german": "de", "deutsch": "de",
	"fr": "fr", "fra": "fr", "fre": "fr", "french": "fr", "francais": "fr",
	"es": "es", "spa": "es", "spanish": "es", "espanol": "es",
	"it": "it", "ita": "it", "italian": "it", "italiano": "it",
	"nl": "nl", "dut": "nl", "nld": "nl", "dutch": "nl", "nederlands": "nl",
	"pt": "pt", "por": "pt", "portuguese": "pt",
	"ru": "ru", "rus": "ru", "russian": "ru",
	"ja": "ja", "jpn": "ja", "japanese": "ja",
	"zh": "zh", "chi": "zh", "zho": "zh", "chinese": "zh", "mandarin": "zh",
	"sv": "sv", "swe": "sv", "swedish": "sv",
	"no": "no", "nor": "no", "norwegian": "no",
	"da": "da", "dan": "da", "danish": "da",
	"fi": "fi", "fin": "fi", "finnish": "fi",
	"pl": "pl", "pol": "pl", "polish": "pl",
	"cs": "cs", "cze": "cs", "ces": "cs", "czech": "cs",
	"la": "la", "lat": "la", "latin": "la",
}

// vocabKey is the value two spellings must share to count as the same thing.
func vocabKey(field, value string) string {
	if field == "authors" || field == "narrators" {
		value = firstLast(value) // "Sanderson, Brandon" is Brandon Sanderson
	}
	n, _ := nameCore(field, norm(value))
	if field == "series" { // "The Chronicles of Amber", "Chronicles of Amber" and "Chronicles of Amber Series" are one series
		n = strings.TrimSuffix(strings.TrimPrefix(n, "the "), " series")
	}
	if field != "languages" {
		return n
	}
	if canonical, ok := languageAliases[norm(languageBase(value))]; ok {
		return canonical
	}

	return n
}

// nameSuffix is what may follow a comma in a name without being the first
// name: "Martin Luther King, Jr."
var nameSuffix = regexp.MustCompile(`(?i)^(jr|sr|ii|iii|iv|phd|md|esq|dds)\.?$`)

// firstLast turns a "Last, First" name round. A name with more than one
// comma is several names, or a list, and is left alone.
func firstLast(value string) string {
	last, first, ok := strings.Cut(value, ",")
	if !ok || strings.Contains(first, ",") {
		return value
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	if first == "" || last == "" || nameSuffix.MatchString(first) {
		return value
	}
	return first + " " + last
}

// knownLanguage reports whether a language value is one this tool
// recognizes, its region aside: en-US is English.
func knownLanguage(value string) bool {
	_, ok := languageAliases[norm(languageBase(value))]
	return ok
}

// valuesOf pulls a field's values off one item. The sweep sees the minified
// shape, which carries authors and narrators only as one joined string
// (authorName, narratorName), so both fall back to splitting that; the
// narrator audits fill the lists in first (joinedNames), since a name may
// hold a comma.
func valuesOf(field string, it *abs.Item) []string {
	m := &it.Media.Metadata
	switch field {
	case "genres":
		return m.Genres
	case "tags":
		return it.Media.Tags
	case "narrators":
		if len(m.Narrators) == 0 && m.NarratorName != "" {
			return strings.Split(m.NarratorName, ",")
		}
		return m.Narrators
	case "languages":
		if m.Language == "" {
			return nil
		}
		return []string{m.Language}
	case "publishers":
		if m.Publisher == "" {
			return nil
		}
		return []string{m.Publisher}
	case "authors":
		var out []string
		for _, a := range m.Authors {
			if a.Name != "" {
				out = append(out, a.Name)
			}
		}
		if len(out) == 0 && m.AuthorName != "" {
			out = strings.Split(m.AuthorName, ",")
		}
		return out
	}

	return nil
}

// spellingCounts gathers, per field, every spelling of every value and how
// many items carry it: field -> normalized key -> spelling -> count.
type spellingCounts map[string]map[string]map[string]int

func newSpellingCounts(fields []string) spellingCounts {
	c := spellingCounts{}
	for _, f := range fields {
		c[f] = map[string]map[string]int{}
	}
	return c
}

// add counts the item's values for every field being gathered.
func (c spellingCounts) add(it *abs.Item) {
	for f := range c {
		for _, v := range valuesOf(f, it) {
			c.addValue(f, v, 1)
		}
	}
}

// addValue counts one spelling of a field's value n times: what a sweep over
// author records feeds in, with the record's book count.
func (c spellingCounts) addValue(field, v string, n int) {
	v = strings.TrimSpace(v)
	if v == "" {
		return
	}
	k := vocabKey(field, v)
	if k == "" {
		return
	}
	if c[field][k] == nil {
		c[field][k] = map[string]int{}
	}
	c[field][k][v] += n
}

type spelling struct {
	Value  string `json:"value"`
	Items  int    `json:"items"            jsonschema:"how many items carry this exact spelling"`
	Author string `json:"author,omitempty" jsonschema:"series only: whose series this spelling is"`

	affixed bool // wrapped in "read by" or the like; never the one to keep
}

type vocabGroup struct {
	Field     string     `json:"field"           jsonschema:"pass to metadata_rename"`
	Kind      string     `json:"kind"            jsonschema:"spelling: one value spelled several ways; affix: a name wrapped in 'read by', 'narrator' or the like; contains: one value is another cut short or without its middle initials; near: a letter or two apart, a typo or two people; split: two names in one value, fix with item_edit; fragment: a credential or leftover such as Ph.D., fix with metadata_rename remove"`
	Keep      string     `json:"keep,omitempty"  jsonschema:"the spelling to merge into: the most used clean one, or for affix the name with the wrapper cut off"`
	Spellings []spelling `json:"spellings"       jsonschema:"every spelling involved, the one to keep first"`
	Parts     []string   `json:"parts,omitempty" jsonschema:"split only: the names inside the value"`
}

// spellingsOf lists one key's spellings, the one to keep first: a clean
// spelling over an affixed one, then the most used, then alphabetical so the
// output is stable.
func (c spellingCounts) spellingsOf(field, key string) []spelling {
	out := make([]spelling, 0, len(c[field][key]))
	for v, n := range c[field][key] {
		_, affixed := nameCore(field, norm(v))
		out = append(out, spelling{Value: v, Items: n, affixed: affixed})
	}
	sortSpellings(out)
	return out
}

func sortSpellings(sp []spelling) {
	slices.SortFunc(sp, func(a, b spelling) int {
		if a.affixed != b.affixed {
			if a.affixed {
				return 1
			}
			return -1
		}
		if a.Items != b.Items {
			return b.Items - a.Items
		}
		return strings.Compare(a.Value, b.Value)
	})
}

// report is everything audit_spelling has to say about one field: the keys
// spelled more than one way (or once, wrapped in an affix), then the values
// that are not a name at all, then the pairs of keys that are one another cut
// short or a typo apart. Stable order, so two runs read the same.
func (c spellingCounts) report(field string) []vocabGroup {
	keys := make([]string, 0, len(c[field]))
	for k := range c[field] {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	var out []vocabGroup
	for _, k := range keys {
		sp := c.spellingsOf(field, k)
		affixed := slices.ContainsFunc(sp, func(s spelling) bool { return s.affixed })
		if len(sp) < 2 && !affixed {
			continue
		}
		g := vocabGroup{Field: field, Kind: "spelling", Keep: sp[0].Value, Spellings: sp}
		if affixed {
			g.Kind = "affix"
			if sp[0].affixed { // nobody spelled it clean: offer the name with the wrapper cut off
				n := norm(sp[0].Value)
				core, _ := nameCore(field, n)
				g.Keep = cleanName(sp[0].Value, len(strings.Fields(n))-len(strings.Fields(core)))
			}
		}
		out = append(out, g)
	}

	if personFields[field] {
		for _, k := range keys {
			sp := c.spellingsOf(field, k)
			switch {
			case fragments[k] || len(k) <= 2:
				out = append(out, vocabGroup{Field: field, Kind: "fragment", Spellings: sp})
			case splitValue(field, sp[0].Value, k):
				out = append(out, vocabGroup{Field: field, Kind: "split", Spellings: sp, Parts: splitParts(sp[0].Value)})
			}
		}
	}

	if field != "languages" { // codes are short and a letter apart by design
		// pairs that are one another cut short or a typo apart, then joined
		// into clusters so that Audiobook, Audio Book and Audiobooks are one
		// group rather than three overlapping ones
		parent := map[string]string{}
		find := func(k string) string {
			for parent[k] != "" && parent[k] != k {
				k = parent[k]
			}
			return k
		}
		kinds := map[string]string{}
		for i, a := range keys {
			for _, b := range keys[i+1:] {
				short, long := a, b
				if len(short) > len(long) {
					short, long = b, a
				}
				var kind string
				switch {
				case nameFields[field] && field != "series" && truncationOf(short, long), personFields[field] && initialsOf(short, long):
					kind = "contains"
				case typoApart(a, b):
					kind = "near"
				default:
					continue
				}
				ra, rb := find(a), find(b)
				if ra != rb {
					parent[ra] = rb
				}
				root := find(a)
				if kinds[root] == "" || kind == "contains" { // a cluster with a truncation in it is reported as one
					kinds[root] = kind
				}
			}
		}
		clusters := map[string][]string{}
		for _, k := range keys {
			if parent[k] != "" || slices.ContainsFunc(keys, func(o string) bool { return parent[o] == k }) {
				clusters[find(k)] = append(clusters[find(k)], k)
			}
		}
		roots := make([]string, 0, len(clusters))
		for r := range clusters {
			roots = append(roots, r)
		}
		slices.Sort(roots)
		for _, r := range roots {
			kind := kinds[r]
			for _, k := range clusters[r] { // a kind recorded under a member that was merged in later
				if kinds[k] == "contains" {
					kind = "contains"
				}
			}
			var sp []spelling
			for _, k := range clusters[r] {
				sp = append(sp, c.spellingsOf(field, k)...)
			}
			sortSpellings(sp)
			if kind == "contains" && personFields[field] { // a person's name cut short: the longer one is the whole name
				slices.SortFunc(sp, func(x, y spelling) int { return len(norm(y.Value)) - len(norm(x.Value)) })
			}
			out = append(out, vocabGroup{Field: field, Kind: kind, Keep: sp[0].Value, Spellings: sp})
		}
	}

	return out
}

// findingCount is how many groups every field gathered reports, for a
// count-only summary.
func (c spellingCounts) findingCount() int {
	n := 0
	for f := range c {
		n += len(c.report(f))
	}
	return n
}

func registerSpellingTools(r *registry) {
	client := r.client

	type vocabIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
		Field   string `json:"field,omitempty"   jsonschema:"genres, tags, languages or publishers; default all of them (authors: audit_authors, narrators: audit_narrators)"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum groups to return, default 50, at most 1000"`
	}
	type vocabOut struct {
		Scanned      int          `json:"items_scanned"`
		Found        int          `json:"total_findings"                   jsonschema:"groups of every kind before limit, and the unrecognized languages"`
		Groups       []vocabGroup `json:"groups"`
		OddLanguages []spelling   `json:"unrecognized_languages,omitempty" jsonschema:"language values that are not a code or name this tool knows, e.g. a placeholder like XXX"`
	}

	add(r, readTool, &mcp.Tool{
		Name: "audit_spelling",
		Description: "Find values that mean the same thing but are spelled differently, across genres, tags, languages and publishers: 'Sci-Fi' and 'sci fi', 'en' and 'eng' and 'English', 'Harper Audio' and 'HarperAudio'. " +
			"Publishers also get one name that is another cut short ('Recorded Books' and 'Recorded Books, Inc.') and two a typo apart, and every field gets spellings a letter or two apart ('Romance' and 'Romances'). Each group says which kind it is. Authors and narrators get the same treatment, and more, from audit_authors and audit_narrators. " +
			"Merge any group with metadata_rename, passing the field and the spellings as reported here; a near group can also be two different people, so read both names first. " +
			"Marker tags, the provider tag and any other 'prefix:value' tag, are left out: two stores' markers are a letter apart by design. " +
			"Language values that are not a code or name this tool recognizes, a region aside (en-US is English), are reported separately and counted, which is how placeholders like XXX surface.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in vocabIn) (*mcp.CallToolResult, vocabOut, error) {
		fields := spellingFields
		if f := strings.ToLower(strings.TrimSpace(in.Field)); f != "" && f != "all" {
			f = vocabField(f)
			switch f {
			case "":
				return nil, vocabOut{}, fmt.Errorf("unknown field %q; choose one of: %s", in.Field, strings.Join(spellingFields, ", "))
			case "narrators":
				return nil, vocabOut{}, errors.New("narrators are audited by audit_narrators, which reports their spellings beside its role check")
			case "authors":
				return nil, vocabOut{}, errors.New("authors are audited by audit_authors, which reports their spellings beside the record checks")
			}
			fields = []string{f}
		}

		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, vocabOut{}, err
		}

		// The sweep is the reliable source: the server's own filter data is
		// cached and is not invalidated by an edit or a rescan, so it goes
		// stale as soon as anything is fixed.
		counts := newSpellingCounts(fields)
		out := vocabOut{Groups: []vocabGroup{}}
		for i := range libs {
			if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
				for j := range items {
					out.Scanned++
					counts.add(&items[j])
				}
				return true
			}); err != nil {
				return nil, vocabOut{}, err
			}
		}
		counts.dropMarkers(r.providerConfig())

		limit := auditLimit(in.Limit, 50)
		for _, f := range fields {
			for _, g := range counts.report(f) {
				out.Found++
				if len(out.Groups) < limit {
					out.Groups = append(out.Groups, g)
				}
			}
		}

		// a language nobody recognizes is usually a placeholder rather than a
		// spelling variant, so it needs looking at rather than merging
		out.OddLanguages = oddLanguages(counts)
		out.Found += len(out.OddLanguages)

		return nil, out, nil
	})

	type renameIn struct {
		Field   string          `json:"field"              jsonschema:"tags, genres, narrators, authors, languages or publishers, as audit_spelling reports it"`
		From    string          `json:"from"               jsonschema:"the value to replace, exactly as it is spelled now"`
		To      string          `json:"to,omitempty"       jsonschema:"the value to keep; renaming onto one that already exists merges the two"`
		Remove  bool            `json:"remove,omitempty"   jsonschema:"instead of renaming: drop the value from every item that carries it (not authors). Without confirm it only reports those items"`
		Confirm bool            `json:"confirm,omitempty"  jsonschema:"with remove: true to drop the value; without it the call only reports what it would drop it from"`
		Library string          `json:"library,omitempty"  jsonschema:"library name or id; default every library. Tags and genres are server-wide and refuse it"`
		Into    []string        `json:"into,omitempty"     jsonschema:"tags and genres: split the value into these, so \"Science Fiction & Fantasy, Fantasy\" becomes two; with to_field the parts land in the other field"`
		ToField string          `json:"to_field,omitempty" jsonschema:"tags and genres: move the value (or the into parts) to the other field, genres or tags, dropping it from this one"`
		Split   *genreSplitPlan `json:"split,omitempty"    jsonschema:"tags and genres: replace the value with parts in both fields at once, {genres: [...], tags: [...]}, exactly as audit_genres suggests for a compound value; only the items carrying from change, so a part that is a genre on other books stays theirs"`
	}
	type renameOut struct {
		Field        string         `json:"field"`
		ItemsUpdated int            `json:"items_updated"     jsonschema:"items changed; for authors, the books the author had, which now carry the new name, or after a merge the other author"`
		Merged       bool           `json:"merged,omitempty"  jsonschema:"authors: the rename merged into an author that already existed"`
		Items        []string       `json:"items,omitempty"   jsonschema:"languages and publishers: the titles changed, capped at 50"`
		Preview      *removePreview `json:"preview,omitempty" jsonschema:"remove without confirm: what confirm=true would drop the value from; nothing was changed"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "metadata_rename",
		Description: "Rename one metadata value everywhere it is used - a tag, genre, narrator, author, language or publisher - or with remove drop it from every item. " +
			"A remove has nothing to put back and a tag or genre is server-wide, so without confirm=true it only says how many items carry the value and which, and changes nothing. " +
			"Renaming onto a value that already exists merges the two, which is how a group from audit_spelling is fixed ('jim dale' into 'Jim Dale', 'Sci-Fi' into 'Science Fiction'). " +
			"For tags and genres, into splits one value into several, to_field moves a value to the other field, and split does both at once, which is how audit_genres findings are fixed: pass a compound finding's suggest as split. " +
			"Tags and genres are server-wide; the rest can be narrowed to one library. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in renameIn) (*mcp.CallToolResult, renameOut, error) {
		// a sweep reads every item and writes the ones it changes back
		// whole: no edit of an item may land in between
		defer r.locks.holdAll()()
		field := vocabField(in.Field)
		if field == "" {
			return nil, renameOut{}, fmt.Errorf("unknown field %q; choose one of: %s", in.Field, strings.Join(vocabFields, ", "))
		}
		from, to := strings.TrimSpace(in.From), strings.TrimSpace(in.To)
		switch {
		case from == "":
			return nil, renameOut{}, errors.New("from is required")
		case in.Remove && to != "":
			return nil, renameOut{}, errors.New("pass either to or remove, not both")
		case in.Split != nil && (len(in.Into) > 0 || in.ToField != "" || in.Remove || to != ""):
			return nil, renameOut{}, errors.New("split says where every part goes; it takes no to, remove, into or to_field")
		case in.Split != nil && field != "tags" && field != "genres":
			return nil, renameOut{}, fmt.Errorf("split is for tags and genres, not %s", field)
		case in.Split != nil && len(in.Split.Genres)+len(in.Split.Tags) == 0:
			return nil, renameOut{}, errors.New("split needs at least one part in genres or tags")
		case (len(in.Into) > 0 || in.ToField != "") && (in.Remove || to != ""):
			return nil, renameOut{}, errors.New("into and to_field are a split or a move; they do not combine with to or remove")
		case (len(in.Into) > 0 || in.ToField != "") && field != "tags" && field != "genres":
			return nil, renameOut{}, errors.New("into and to_field are for tags and genres")
		case in.ToField != "" && vocabField(in.ToField) != "tags" && vocabField(in.ToField) != "genres":
			return nil, renameOut{}, fmt.Errorf("to_field must be tags or genres, not %q", in.ToField)
		case in.ToField != "" && vocabField(in.ToField) == field:
			return nil, renameOut{}, errors.New("to_field is the field the value is already in; use to or into")
		case !in.Remove && to == "" && len(in.Into) == 0 && in.ToField == "" && in.Split == nil:
			return nil, renameOut{}, errors.New("to is required unless remove, into or to_field is set")
		case in.Remove && field == "authors":
			return nil, renameOut{}, errors.New("an author cannot be removed here: author_delete removes the record")
		}

		out := renameOut{Field: field}
		preview := in.Remove && !in.Confirm
		switch field {
		case "tags", "genres":
			if in.Library != "" {
				return nil, renameOut{}, fmt.Errorf("%s are server-wide: omit library", field)
			}
			if preview {
				libs, err := resolveLibraries(ctx, client, "")
				if err != nil {
					return nil, renameOut{}, err
				}
				if out.Preview, err = carrying(ctx, client, libs, field, from); err != nil {
					return nil, renameOut{}, err
				}
				break
			}
			if in.Split != nil || len(in.Into) > 0 || in.ToField != "" {
				var genres, tags []string
				if in.Split != nil {
					genres, tags = in.Split.Genres, in.Split.Tags
				} else {
					parts := in.Into
					if len(parts) == 0 {
						parts = []string{from}
					}
					dst := vocabField(in.ToField)
					if dst == "" {
						dst = field
					}
					if dst == "tags" {
						tags = parts
					} else {
						genres = parts
					}
				}
				n, err := splitVocabulary(ctx, client, field, from, genres, tags)
				if err != nil {
					return nil, renameOut{}, partly(n, err)
				}
				out.ItemsUpdated = n
				break
			}
			n, err := renameVocabulary(ctx, client, field, from, to, in.Remove)
			if err != nil {
				return nil, renameOut{}, err
			}
			out.ItemsUpdated = n
		case "narrators":
			libs, err := resolveLibraries(ctx, client, in.Library)
			if err != nil {
				return nil, renameOut{}, err
			}
			if preview {
				if out.Preview, err = carrying(ctx, client, libs, field, from); err != nil {
					return nil, renameOut{}, err
				}
				break
			}
			for i := range libs {
				var n int
				if in.Remove {
					n, err = client.RemoveNarrator(ctx, libs[i].ID, from)
				} else {
					n, err = client.RenameNarrator(ctx, libs[i].ID, from, to)
				}
				if err != nil {
					return nil, renameOut{}, partly(out.ItemsUpdated, fmt.Errorf("in %s: %w", libs[i].Name, err))
				}
				out.ItemsUpdated += n
			}
		case "authors":
			a, err := resolveAuthor(ctx, client, in.Library, from)
			if err != nil {
				return nil, renameOut{}, err
			}
			_, merged, err := client.UpdateAuthor(ctx, a.ID, abs.AuthorUpdate{Name: &to})
			if err != nil {
				return nil, renameOut{}, err
			}
			// the books that changed are the ones this author had; the reply
			// is the surviving record, whose count after a merge takes in
			// the other author's books too
			out.Merged = merged
			out.ItemsUpdated = len(a.LibraryItems)
			if out.ItemsUpdated == 0 {
				out.ItemsUpdated = a.NumBooks
			}
		default: // languages, publishers: no endpoint, so a sweep and a batch edit
			n, titles, err := renameBySweep(ctx, client, in.Library, field, from, to, preview)
			if err != nil {
				return nil, renameOut{}, partly(n, err)
			}
			if preview {
				out.Preview = &removePreview{Found: n, Items: titles}
				break
			}
			out.ItemsUpdated, out.Items = n, titles
		}

		return nil, out, nil
	})
}

// removePreview is what a remove would drop a value from.
type removePreview struct {
	Found int      `json:"found" jsonschema:"items that carry the value"`
	Items []string `json:"items" jsonschema:"their titles, the first 50"`
}

// carrying finds the items in libs that carry a tag, genre or narrator, by the
// server's own filter for the field, which matches the value the way its
// remove does: whole and exactly.
func carrying(ctx context.Context, client *abs.Client, libs []abs.Library, field, value string) (*removePreview, error) {
	p := &removePreview{Items: []string{}}
	for i := range libs {
		// a podcast library has no narrators, and answers a filter it does
		// not know with every item it holds
		if libs[i].IsPodcast() && !slices.Contains(podcastGroups, field) {
			continue
		}
		res, err := client.Items(ctx, libs[i].ID, abs.ItemsOptions{Limit: previewCap, Sort: "media.metadata.title", Filter: abs.EncodeFilter(field, value), Minified: true})
		if err != nil {
			return nil, err
		}
		p.Found += res.Total
		for _, t := range titles(res.Results) {
			if len(p.Items) < previewCap {
				p.Items = append(p.Items, t)
			}
		}
	}

	return p, nil
}

// partly says how far a change got before it failed: the batches written
// before the error stay written, and a caller told only of the error would
// take the whole change as not made.
func partly(changed int, err error) error {
	if changed == 0 {
		return err
	}
	return fmt.Errorf("%d items were changed before this, and stay changed; the rest were not: %w", changed, err)
}

// renameVocabulary renames or removes a tag or genre server-wide.
func renameVocabulary(ctx context.Context, client *abs.Client, field, from, to string, remove bool) (int, error) {
	switch {
	case field == "tags" && remove:
		return client.DeleteTag(ctx, from)
	case field == "tags":
		return client.RenameTag(ctx, from, to)
	case remove:
		return client.DeleteGenre(ctx, from)
	default:
		return client.RenameGenre(ctx, from, to)
	}
}

// splitVocabulary replaces one tag or genre with parts in either field, on
// every item that carries it: the parts in genres become genres and those in
// tags become tags. The server's rename endpoints map one value to one value
// in one field, so this walks every library and batch-updates the items. An
// item that already carries a part keeps one copy, and items without the
// value are not touched, so a part that is a genre elsewhere stays one.
func splitVocabulary(ctx context.Context, client *abs.Client, field, from string, toGenres, toTags []string) (int, error) {
	clean := func(in []string) []string {
		out := make([]string, 0, len(in))
		for _, p := range in {
			if p = strings.TrimSpace(p); p != "" && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
		return out
	}
	toGenres, toTags = clean(toGenres), clean(toTags)
	libs, err := resolveLibraries(ctx, client, "")
	if err != nil {
		return 0, err
	}
	addAll := func(dst *[]string, parts []string) {
		for _, p := range parts {
			if !slices.ContainsFunc(*dst, func(v string) bool { return strings.EqualFold(v, p) }) {
				*dst = append(*dst, p)
			}
		}
	}
	var updates []abs.BatchMediaUpdate
	for i := range libs {
		if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
			for j := range items {
				it := &items[j]
				genres, tags := slices.Clone(it.Media.Metadata.Genres), slices.Clone(it.Media.Tags)
				src := &genres
				if field == "tags" {
					src = &tags
				}
				if !slices.Contains(*src, from) {
					continue
				}
				*src = slices.DeleteFunc(*src, func(v string) bool { return v == from })
				addAll(&genres, toGenres)
				addAll(&tags, toTags)
				if genres == nil {
					genres = []string{}
				}
				if tags == nil {
					tags = []string{}
				}
				updates = append(updates, abs.BatchMediaUpdate{ID: it.ID, MediaPayload: abs.MediaUpdate{Tags: tags, Metadata: &abs.MetadataUpdate{Genres: genres}}})
			}
			return true
		}); err != nil {
			return 0, err
		}
	}
	updated := 0
	for chunk := range slices.Chunk(updates, 50) {
		n, err := client.BatchUpdate(ctx, chunk)
		if err != nil {
			return updated, err
		}
		updated += n
	}
	return updated, nil
}

// renameBySweep replaces a language or publisher on every item that carries
// it, which the server has no endpoint for: it walks the library and batch
// updates the items that carry from, spelled exactly that way (a rename of
// "english" must not touch the 400 books that say "English"). An empty to
// clears the value. With dryRun it changes nothing and answers how many items
// it would have changed.
func renameBySweep(ctx context.Context, client *abs.Client, library, field, from, to string, dryRun bool) (updated int, titles []string, err error) {
	libs, err := resolveLibraries(ctx, client, library)
	if err != nil {
		return 0, nil, err
	}

	var updates []abs.BatchMediaUpdate
	titles = []string{}
	for i := range libs {
		if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
			for j := range items {
				it := &items[j]
				m := &it.Media.Metadata
				var carries bool
				switch field {
				case "languages":
					carries = strings.TrimSpace(m.Language) == from
				case "publishers":
					carries = strings.TrimSpace(m.Publisher) == from
				}
				if !carries {
					continue
				}

				md := abs.MetadataUpdate{}
				if field == "languages" {
					md.Language = &to
				} else {
					md.Publisher = &to
				}
				updates = append(updates, abs.BatchMediaUpdate{
					ID: it.ID, MediaPayload: abs.MediaUpdate{Metadata: &md},
				})
				if len(titles) < 50 {
					titles = append(titles, it.Title())
				}
			}
			return true
		}); err != nil {
			return 0, nil, err
		}
	}

	if len(updates) == 0 {
		return 0, nil, fmt.Errorf("nothing carries %s %q", field, from)
	}
	if dryRun {
		return len(updates), titles, nil
	}

	// in pages: one request carrying hundreds of items is what a reverse
	// proxy times out on
	for start := 0; start < len(updates); start += sweepBatchSize {
		n, err := client.BatchUpdate(ctx, updates[start:min(start+sweepBatchSize, len(updates))])
		if err != nil {
			return updated, titles, err
		}
		updated += n
	}

	return updated, titles, nil
}

// sweepBatchSize is how many items one batch update carries.
const sweepBatchSize = 100
