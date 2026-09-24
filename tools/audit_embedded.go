package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// embedBatchSize is how many expanded items one batch request asks for. The
// expanded shape carries every audio file with its probed tags, which is the
// point here, but it is also the heaviest thing the server returns.
const embedBatchSize = 50

// registerEmbeddedAudit adds audit_unembedded, which is apart from the other
// audits because the minified sweep cannot answer it: the tags live on the
// audio files, and only the expanded item carries those.
func registerEmbeddedAudit(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_unembedded",
		Description: "Find books whose audio files do not carry the library's metadata in their tags: never embedded (no title or artist tag at all), or stale (the tags disagree with the current title, author, narrator, series, genres, year or publisher, because the book was edited or matched after the last embed). " +
			"This is what says item_embed_metadata is due; run it after a curation pass. It fetches every audio file's tags, so it is slower than the sweeping audits, and audit_all runs it only with deep. Fix with item_embed_metadata.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, auditOut{}, err
		}

		out := auditOut{Check: "unembedded", Findings: []auditFinding{}}
		limit := auditLimit(in.Limit, 100)
		for i := range libs {
			if err := sweepUnembedded(ctx, client, &libs[i], limit, &out); err != nil {
				return nil, auditOut{}, err
			}
		}

		return nil, out, nil
	})
}

// sweepUnembedded adds a book library's never- or stale-embedded books to
// out, keeping at most limit of them as findings but counting every one. A
// podcast library is skipped: nothing embeds into a podcast.
func sweepUnembedded(ctx context.Context, client *abs.Client, lib *abs.Library, limit int, out *auditOut) error {
	if lib.IsPodcast() {
		return nil
	}
	// the cheap sweep picks the candidates: books with audio
	var ids []string
	if err := client.ItemsAll(ctx, lib.ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
		for j := range items {
			it := &items[j]
			if it.IsPodcast() || (it.Media.NumAudioFiles == 0 && it.Media.NumTracks == 0) {
				continue
			}
			ids = append(ids, it.ID)
		}
		return true
	}); err != nil {
		return err
	}

	// then the expanded shape, a batch at a time, for the tags
	for chunk := range slices.Chunk(ids, embedBatchSize) {
		items, err := client.ItemsBatch(ctx, chunk)
		if err != nil {
			return err
		}
		for j := range items {
			out.Scanned++
			detail, suspect := checkEmbedded(&items[j])
			if !suspect {
				continue
			}
			out.Found++
			if len(out.Findings) < limit {
				out.Findings = append(out.Findings, finding(&items[j], detail))
			}
		}
	}

	return nil
}

// checkEmbedded reports whether any of an item's audio files lacks the
// library's metadata in its tags, and says which files and which fields.
func checkEmbedded(it *abs.Item) (string, bool) {
	files := it.Media.AudioFiles
	if len(files) == 0 {
		return "", false
	}

	untagged := 0
	stale := 0
	var reasons []string
	for i := range files {
		af := &files[i]
		if tagsEmpty(af) {
			untagged++
			continue
		}
		if mismatches := embedMismatches(&it.Media.Metadata, af); len(mismatches) > 0 {
			stale++
			for _, m := range mismatches {
				if !slices.Contains(reasons, m) {
					reasons = append(reasons, m)
				}
			}
		}
	}

	var parts []string
	if untagged > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d files carry no tags", untagged, len(files)))
	}
	if stale > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d files disagree with the metadata: %s", stale, len(files), strings.Join(reasons, "; ")))
	}
	if len(parts) == 0 {
		return "", false
	}

	return strings.Join(parts, "; "), true
}

// tagsEmpty reports whether a file has none of the tags an embed writes.
// Track numbers and an encoder name are what a ripper leaves; they do not
// count.
func tagsEmpty(af *abs.AudioFile) bool {
	for _, k := range []string{"tagTitle", "tagAlbum", "tagArtist", "tagAlbumArtist"} {
		if strings.TrimSpace(af.MetaTags[k]) != "" {
			return false
		}
	}
	return true
}

// embedMismatches compares the tags the server writes on embed against the
// item's metadata: title (also the album, which is title plus subtitle),
// artist and album artist for the author, composer for the narrators,
// grouping or series for the series, genre, date and publisher. Fields the
// item does not have are not expected in the file either, and nor is a
// publisher in anything but an mp3: the server writes it to an m4b only as
// the copyright, which its scan does not read back as the publisher.
func embedMismatches(m *abs.Metadata, af *abs.AudioFile) []string {
	tag := func(k string) string { return strings.TrimSpace(af.MetaTags[k]) }
	var out []string

	if title := strings.TrimSpace(m.Title); title != "" {
		if !sameText(tag("tagTitle"), title) && !strings.HasPrefix(strings.ToLower(tag("tagAlbum")), strings.ToLower(title)) {
			out = append(out, differs("title", tag("tagTitle"), title))
		}
	}
	if author := m.AuthorDisplay(); author != "" {
		if !sameText(tag("tagArtist"), author) && !sameText(tag("tagAlbumArtist"), author) {
			out = append(out, differs("author", tag("tagArtist"), author))
		}
	}
	if narrator := m.NarratorDisplay(); narrator != "" {
		if !sameText(tag("tagComposer"), narrator) && !sameText(tag("tagNarrator"), narrator) {
			out = append(out, differs("narrator", tag("tagComposer"), narrator))
		}
	}
	if series := m.SeriesDisplay(); len(series) > 0 {
		name := strings.ToLower(strings.TrimSpace(strings.Split(series[0], " #")[0]))
		have := strings.ToLower(tag("tagGrouping") + " " + tag("tagSeries"))
		if name != "" && !strings.Contains(have, name) {
			out = append(out, differs("series", tag("tagSeries"), series[0]))
		}
	}
	if len(m.Genres) > 0 && !sameList(tag("tagGenre"), m.Genres) {
		out = append(out, differs("genres", tag("tagGenre"), strings.Join(m.Genres, "; ")))
	}
	if year := strings.TrimSpace(m.PublishedYear.String()); year != "" && !strings.HasPrefix(tag("tagDate"), year) {
		out = append(out, differs("year", tag("tagDate"), year))
	}
	if publisher := strings.TrimSpace(m.Publisher); publisher != "" && isMP3(af) && !sameText(tag("tagPublisher"), publisher) {
		out = append(out, differs("publisher", tag("tagPublisher"), publisher))
	}

	return out
}

// differs words one mismatch: an absent tag and a wrong one read differently.
func differs(field, have, want string) string {
	if have == "" {
		return "no " + field + " tag"
	}
	return fmt.Sprintf("%s %q vs %q", field, have, want)
}

// sameText compares a tag with a value ignoring case and surrounding space.
func sameText(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// isMP3 reports whether an audio file is an mp3, the one format the server
// writes a publisher tag into on embed. Its mime type says so, or failing
// that its extension.
func isMP3(af *abs.AudioFile) bool {
	if af.MimeType != "" {
		return strings.EqualFold(af.MimeType, "audio/mpeg")
	}
	return strings.EqualFold(af.Metadata.Ext, ".mp3")
}

// sameList compares a tag holding several values against a list, as sets.
// The server joins genres with "; " on embed, and a genre may hold a comma
// or a slash of its own ("Mystery, Thriller & Suspense"), so the tag is read
// split at semicolons first; a tag another tool wrote with "/" or ","
// between the values is read split at those as well.
func sameList(tag string, want []string) bool {
	return sameSet(tag, want, func(r rune) bool { return r == ';' }) ||
		sameSet(tag, want, func(r rune) bool { return r == ';' || r == '/' || r == ',' })
}

func sameSet(tag string, want []string, sep func(rune) bool) bool {
	have := map[string]bool{}
	for v := range strings.FieldsFuncSeq(tag, sep) {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			have[v] = true
		}
	}
	wanted := map[string]bool{}
	for _, w := range want {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" {
			wanted[w] = true
		}
	}
	if len(have) != len(wanted) {
		return false
	}
	for w := range wanted {
		if !have[w] {
			return false
		}
	}
	return true
}
