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
			"This is what says item_embed_metadata is due; run it after a curation pass. It fetches every audio file's tags, so it is slower than the sweeping audits and is not part of audit_all. Fix with item_embed_metadata.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, auditOut{}, err
		}

		out := auditOut{Check: "unembedded", Findings: []auditFinding{}}
		limit := limitOr(in.Limit, 100)
		for i := range libs {
			if libs[i].IsPodcast() {
				continue
			}
			// the cheap sweep picks the candidates: books with audio
			var ids []string
			if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
				for j := range items {
					it := &items[j]
					if it.IsPodcast() || (it.Media.NumAudioFiles == 0 && it.Media.NumTracks == 0) {
						continue
					}
					ids = append(ids, it.ID)
				}
				return true
			}); err != nil {
				return nil, auditOut{}, err
			}

			// then the expanded shape, a batch at a time, for the tags
			for chunk := range slices.Chunk(ids, embedBatchSize) {
				items, err := client.ItemsBatch(ctx, chunk)
				if err != nil {
					return nil, auditOut{}, err
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
		}

		return nil, out, nil
	})
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
// item does not have are not expected in the file either.
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
	if publisher := strings.TrimSpace(m.Publisher); publisher != "" && !sameText(tag("tagPublisher"), publisher) {
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

// sameList compares a tag holding several values ("Science Fiction; Classic",
// or "/" or "," separated by another tool) against a list, as sets.
func sameList(tag string, want []string) bool {
	have := map[string]bool{}
	for v := range strings.FieldsFuncSeq(tag, func(r rune) bool { return r == ';' || r == '/' || r == ',' }) {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			have[v] = true
		}
	}
	if len(have) != len(want) {
		return false
	}
	for _, w := range want {
		if !have[strings.ToLower(strings.TrimSpace(w))] {
			return false
		}
	}
	return true
}
