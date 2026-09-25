package tools

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The folder is the collector's own record of what a book is; the metadata
// is whatever the last match wrote. When they disagree the match was usually
// wrong. But folders and titles are both written by hand in a dozen styles -
// "Series - 03 - Title", "Title: Series, Book 3", "Title (Unabridged)",
// "Title Disc 1" - so both are reduced to the words that name the book
// before they are compared, or the audit reports the style rather than the
// mismatch (393 findings over 1316 items on 2026-09-13, a dozen of them real).

var (
	// " - " between segments of a folder or title; either side of the dash
	// may be missing its space ("04- Title"), but a hyphenated name may not
	// be split ("Adler-Olsen")
	pathSegment = regexp.MustCompile(`\s+[-–]\s*|\s*[-–]\s+`)
	// "(Unabridged)", "[Disc 1]", "(Narrator Name)": a parenthetical never
	// names the book
	pathParen = regexp.MustCompile(`\s*[(\[][^)\]]*[)\]]`)
	// "Disc 1", "CD 2", "Part 3" trailing a title, and "v2" trailing a folder
	pathTrailer = regexp.MustCompile(`(?i)(\s+(disc|cd|part)\s*\d+|\s+v\d+)\s*$`)
	// "01 Title": a track-style number in front of a title
	pathLead = regexp.MustCompile(`^\d{1,3}\s+`)
	// leading zeros so that "Vol. 04" and "Vol. 4" are the same words
	pathZeros = regexp.MustCompile(`\b0+(\d)`)
	// a number long enough to be a title rather than a place in a series
	pathYearLike = regexp.MustCompile(`^\d{3,}$`)
	// "Alcatraz vs the Shattered Lens" is "Alcatraz Versus the Shattered Lens"
	pathVersus = regexp.MustCompile(`\bvs\b`)
)

// pathWords reduces a folder or title to comparable words: lower-case, no
// punctuation, no leading zeros, "vs" written out.
func pathWords(s string) string {
	return pathVersus.ReplaceAllString(pathZeros.ReplaceAllString(norm(s), "$1"), "versus")
}

// titleNames returns the ways a title may name the book, most complete
// first: whole, without its subtitle, and the part after or before a dash
// when a series or author was written into it ("Bridge, Book 2 - Idoru",
// "Master of the Revels - A Return to D.O.D.O.").
func titleNames(s string) []string {
	s = pathStrip(s)
	names := pathNameSet{}
	names.add(s)
	if i := strings.Index(s, ":"); i > 0 {
		names.add(s[:i])
	}
	if segs := pathSegment.Split(s, -1); len(segs) > 1 {
		names.add(segs[len(segs)-1])
		names.add(segs[0])
	}
	return names.out
}

// folderNames returns the ways a folder may name the book: whole, for the
// "Series, Vol. 4" style where the number is the name, and its last worded
// segment, which is the book in "Series - 03 - Title". The series in front
// is never enough on its own: "Jumper - 4 - Exo" does not name "Jumper
// Series Bk 4".
func folderNames(s string) []string {
	s = pathStrip(s)
	names := pathNameSet{}
	names.add(s)
	segs := pathSegment.Split(s, -1)
	for _, seg := range slices.Backward(segs) {
		if names.add(seg) {
			break
		}
	}
	// "1984 - Original Adaptation": a numbered title in front of an edition
	if len(segs) > 1 && pathYearLike.MatchString(pathWords(segs[0])) {
		names.add(segs[0])
	}
	return names.out
}

// pathStrip removes what never names the book: parentheticals, disc and
// version trailers, and a leading track number.
func pathStrip(s string) string {
	s = pathParen.ReplaceAllString(s, "")
	s = pathTrailer.ReplaceAllString(s, "")
	return pathLead.ReplaceAllString(s, "")
}

// pathNameSet collects distinct comparable names, dropping any that could
// not be one: a series number is not a name, though "2001" and "1984" are.
type pathNameSet struct {
	out  []string
	seen map[string]bool
}

func (n *pathNameSet) add(v string) bool {
	v = pathWords(v)
	hasLetter := strings.ContainsFunc(v, unicode.IsLetter)
	if v == "" || n.seen[v] || (!hasLetter && !pathYearLike.MatchString(v)) {
		return false
	}
	if n.seen == nil {
		n.seen = map[string]bool{}
	}
	n.seen[v] = true
	n.out = append(n.out, v)
	return true
}

// pathAgree reports whether any name for the folder and any name for the
// title is contained in the other, word for word: a book matched to "It" in
// a folder called "The Institute" is not named by its folder, though the
// letters are there.
func pathAgree(folder, title []string) bool {
	for _, f := range folder {
		for _, t := range title {
			if containsWords(f, t) || containsWords(t, f) {
				return true
			}
		}
	}
	return false
}

// containsWords reports whether the words of part appear together in whole,
// both already reduced to space-separated words.
func containsWords(whole, part string) bool {
	return strings.Contains(" "+whole+" ", " "+part+" ")
}

// checkPath flags items whose folder name does not name their title, or
// whose parent folder names neither an author, nor the book's series, nor a
// genre it carries: a sign of a wrong match or a misfiled folder in an
// Author/Title, Author/Series/Title or Series/Title layout.
func checkPath(it *abs.Item) (string, bool) {
	if it.IsPodcast() || it.RelPath == "" || it.IsFile {
		return "", false
	}
	m := it.Media.Metadata
	rel := strings.Trim(it.RelPath, "/")
	base := path.Base(rel)
	if strings.TrimSpace(m.Title) == "" {
		return "", false
	}

	titleOK := pathAgree(folderNames(base), titleNames(m.Title))

	authorOK := true
	if parent := path.Dir(rel); parent != "." && parent != "/" {
		authorOK = false
		baseWords := pathWords(pathParen.ReplaceAllString(base, ""))
		for p := range strings.SplitSeq(parent, "/") {
			// "psychology/Blink": a folder with no capital in it is a
			// shelf, not a person; people write names with capitals
			if p == strings.ToLower(p) {
				authorOK = true
				continue
			}
			for _, np := range parentNames(p) {
				if np == "" {
					continue
				}
				// "DragonLance/DragonLance Chronicles 1 - ...": a folder
				// whose children repeat its name is a series or collection
				// folder, not an author's
				if strings.HasPrefix(baseWords, np) {
					authorOK = true
				}
				for a := range strings.SplitSeq(m.AuthorDisplay(), ",") {
					na := pathWords(a)
					if na != "" && (strings.Contains(np, na) || strings.Contains(na, np) || lastFirstMatch(np, na)) {
						authorOK = true
					}
				}
				for _, s := range m.SeriesDisplay() {
					ns := pathWords(strings.Split(s, " #")[0])
					if ns != "" && (strings.Contains(np, ns) || strings.Contains(ns, np)) {
						authorOK = true
					}
				}
				// "Psychology/Radical Belonging": a genre folder
				for _, g := range append(m.Genres, it.Media.Tags...) {
					ng := pathWords(g)
					if ng != "" && (strings.Contains(np, ng) || strings.Contains(ng, np)) {
						authorOK = true
					}
				}
			}
		}
	}

	switch {
	case !titleOK && !authorOK:
		return fmt.Sprintf("folder %q does not match title %q or author %q", rel, m.Title, m.AuthorDisplay()), true
	case !titleOK:
		return fmt.Sprintf("folder %q does not contain title %q", base, m.Title), true
	case !authorOK:
		return fmt.Sprintf("parent folder %q does not name author %q", path.Dir(rel), m.AuthorDisplay()), true
	}

	return "", false
}

// parentNames is one parent folder as the names it may carry: the whole
// ("Stephen King"), and each dash-separated part of a compound like
// "Warhammer 40k - Siege of Terra", which names a setting and a series.
func parentNames(folder string) []string {
	words := pathWords(folder)
	if words == "" {
		return nil
	}
	out := []string{words}
	if segs := pathSegment.Split(folder, -1); len(segs) > 1 {
		for _, s := range segs {
			if w := pathWords(s); w != "" && w != words {
				out = append(out, w)
			}
		}
	}
	return out
}

// pathIn is auditIn plus the switch for the filename check, which is off by
// default: filenames are the least curated part of a library, and looking at
// them means fetching every book's file list.
type pathIn struct {
	auditIn
	Files bool `json:"files,omitempty" jsonschema:"also compare the audio filenames to the title: a single-file book by its filename, a folder by the name its tracks share once numbers and words like chapter or part are dropped. Off by default because filenames are rarely tidy; it fetches every book's file list, so it takes longer"`
}

// registerPathAudit adds audit_path, which is apart from the spec loop for
// its files option.
func registerPathAudit(r *registry) {
	client := r.client
	spec := auditSpecs[slices.IndexFunc(auditSpecs, func(s auditSpec) bool { return s.Tool == "audit_path" })]

	add(r, readTool, &mcp.Tool{
		Name:        spec.Tool,
		Description: spec.Description + " With files, the audio filenames are compared too.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pathIn) (*mcp.CallToolResult, auditOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, auditOut{}, err
		}

		out := auditOut{Check: spec.Check, Findings: []auditFinding{}}
		limit := auditLimit(in.Limit, 100)
		for i := range libs {
			if err := sweepPath(ctx, client, &libs[i], in.Files, limit, &out); err != nil {
				return nil, auditOut{}, err
			}
		}

		return nil, out, nil
	})
}

// sweepPath runs the folder check over a library and, with files, the
// filename check over every book the folder check cleared: a book already
// reported for its folder is not reported again for its files.
func sweepPath(ctx context.Context, client *abs.Client, lib *abs.Library, files bool, limit int, out *auditOut) error {
	if lib.IsPodcast() {
		return nil
	}
	report := func(it *abs.Item, detail string) {
		out.Found++
		if len(out.Findings) < limit {
			out.Findings = append(out.Findings, finding(it, detail))
		}
	}

	var ids []string
	if err := client.ItemsAll(ctx, lib.ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
		for j := range items {
			it := &items[j]
			out.Scanned++
			if detail, suspect := checkPath(it); suspect {
				report(it, detail)
				continue
			}
			if files && !it.IsPodcast() && (it.Media.NumAudioFiles > 0 || it.Media.NumTracks > 0) {
				ids = append(ids, it.ID)
			}
		}
		return true
	}); err != nil {
		return err
	}

	for chunk := range slices.Chunk(ids, embedBatchSize) {
		items, err := client.ItemsBatch(ctx, chunk)
		if err != nil {
			return err
		}
		for j := range items {
			if detail, suspect := checkFiles(&items[j]); suspect {
				report(&items[j], detail)
			}
		}
	}

	return nil
}

// fileGeneric are the words in a track name that place it rather than name
// it; with the numbers, they are dropped before the tracks are compared.
var fileGeneric = map[string]bool{
	"chapter": true, "chapters": true, "ch": true, "chap": true, "track": true, "trk": true,
	"part": true, "pt": true, "disc": true, "cd": true, "section": true, "sec": true,
	"of": true, "and": true, "the": true, "a": true,
}

// fileStem is the name a set of tracks share: the words every name opens
// with and the words every name closes with, less numbers and the generic
// words. "Dune - 01", "Dune - 02" share "dune"; "01 - Arrakis", "02 -
// Caladan" share nothing, and a folder of chapter-named tracks says nothing
// about which book it holds.
func fileStem(names []string) string {
	if len(names) == 0 {
		return ""
	}
	words := make([][]string, len(names))
	for i, n := range names {
		words[i] = strings.Fields(pathWords(pathStrip(n)))
	}

	prefix := len(words[0])
	for _, w := range words[1:] {
		prefix = min(prefix, len(w))
		for k := 0; k < prefix; k++ {
			if w[k] != words[0][k] {
				prefix = k
				break
			}
		}
	}
	suffix := len(words[0]) - prefix
	for _, w := range words[1:] {
		suffix = min(suffix, len(w)-prefix)
		for k := 0; k < suffix; k++ {
			if w[len(w)-1-k] != words[0][len(words[0])-1-k] {
				suffix = k
				break
			}
		}
	}

	return strings.Join(fileKeyWords(slices.Concat(words[0][:prefix], words[0][len(words[0])-suffix:])), " ")
}

// fileKeyWords drops the numbers and the generic words, so that a stem and a
// title are compared on the words that name the book: "burden loyalty" is
// "The Burden of Loyalty".
func fileKeyWords(words []string) []string {
	var out []string
	for _, w := range words {
		if fileGeneric[w] || !strings.ContainsFunc(w, unicode.IsLetter) {
			continue
		}
		out = append(out, w)
	}
	return out
}

// fileKey is a name reduced the way a stem is.
func fileKey(s string) string {
	return strings.Join(fileKeyWords(strings.Fields(pathWords(pathStrip(s)))), " ")
}

// checkFiles flags a book whose audio files are named for something other
// than the book: a single file by its own name, a folder of tracks by the
// name they share. Tracks that share nothing but numbers are not judged.
func checkFiles(it *abs.Item) (string, bool) {
	files := it.Media.AudioFiles
	if it.IsPodcast() || len(files) == 0 {
		return "", false
	}
	m := it.Media.Metadata
	if strings.TrimSpace(m.Title) == "" {
		return "", false
	}

	names := make([]string, 0, len(files))
	for i := range files {
		f := files[i].Metadata.Filename
		names = append(names, strings.TrimSuffix(f, path.Ext(f)))
	}
	stem := fileStem(names)
	if stem == "" {
		return "", false
	}

	// the title, the author and the series all name the book; so does the
	// folder, which the folder check has already held to the title
	known := titleNames(m.Title)
	if !it.IsFile {
		known = append(known, folderNames(path.Base(strings.Trim(it.RelPath, "/")))...)
	}
	for a := range strings.SplitSeq(m.AuthorDisplay(), ",") {
		if na := pathWords(a); na != "" {
			known = append(known, na)
		}
	}
	for _, s := range m.SeriesDisplay() {
		if ns := pathWords(strings.Split(s, " #")[0]); ns != "" {
			known = append(known, ns)
		}
	}
	keys := make([]string, 0, len(known))
	for _, k := range known {
		if key := fileKey(k); key != "" {
			keys = append(keys, key)
		}
	}
	if pathAgree([]string{stem}, keys) {
		return "", false
	}

	if len(files) == 1 {
		return fmt.Sprintf("file %q does not name title %q", files[0].Metadata.Filename, m.Title), true
	}
	return fmt.Sprintf("%d audio files share the name %q, which is not title %q", len(files), stem, m.Title), true
}
