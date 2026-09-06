package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit checks. Detection is code, correction is judgment: each check is a
// cheap deterministic sweep that produces a worklist for the AI to act on
// with item_match, item_edit or item_cover_set.
const auditChecks = "unmatched (no asin and no isbn: never matched to a provider), cover, description, narrator, series, author, genres, year, publisher, language, " +
	"chapters (multi-hour audio with no chapters), single_file (one long audio file, e.g. an unsplit m4b/mp3 with no chapters), " +
	"issues (folder missing or no playable media), no_audio (ebook-only), path (folder name disagrees with the metadata title/author), " +
	"stale_feed (podcasts: no new episodes in 90 days or the feed was never checked), no_episodes (podcasts with nothing downloaded)"

type auditIn struct {
	Library string `json:"library,omitempty" jsonschema:"library name or id; default all libraries"`
	Check   string `json:"check"             jsonschema:"which problem to look for; the tool description lists the checks"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum findings to return, default 100"`
}

type auditFinding struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Author string `json:"author,omitempty"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type auditOut struct {
	Check    string         `json:"check"`
	Scanned  int            `json:"items_scanned"`
	Found    int            `json:"total_findings"`
	Findings []auditFinding `json:"findings"       jsonschema:"capped at limit; total_findings is the real count"`
}

// auditCheck returns (detail, true) when the item is suspect.
type auditCheck func(it *abs.Item) (string, bool)

func registerAuditTools(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name:        "library_audit",
		Description: "Sweep a library for metadata problems and return a worklist. Checks: " + auditChecks + ". Fix findings with item_match (unmatched, description, narrator, series, year...), item_cover_set (cover), item_edit, or item_chapters_set.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		check, ok := auditChecksByName[strings.ToLower(strings.TrimSpace(in.Check))]
		if !ok {
			return nil, auditOut{}, fmt.Errorf("unknown check %q; choose one of: %s", in.Check, auditChecks)
		}

		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, auditOut{}, err
		}

		out := auditOut{Check: in.Check, Findings: []auditFinding{}}
		limit := limitOr(in.Limit, 100)
		for i := range libs {
			// the server has a native filter for some checks, which is much
			// cheaper than a sweep
			if filter := nativeFilter[strings.ToLower(in.Check)]; filter != "" {
				res, err := client.Items(ctx, libs[i].ID, abs.ItemsOptions{Limit: limit - len(out.Findings), Filter: filter, Minified: true})
				if err != nil {
					return nil, auditOut{}, err
				}
				out.Scanned += res.Total
				out.Found += res.Total
				for j := range res.Results {
					detail, _ := check(&res.Results[j])
					out.Findings = append(out.Findings, finding(&res.Results[j], detail))
				}
				continue
			}

			if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
				for j := range items {
					out.Scanned++
					detail, suspect := check(&items[j])
					if !suspect {
						continue
					}
					out.Found++
					if len(out.Findings) < limit {
						out.Findings = append(out.Findings, finding(&items[j], detail))
					}
				}
				return true
			}); err != nil {
				return nil, auditOut{}, err
			}
		}

		return nil, out, nil
	})

	type dupIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default all libraries"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum groups to return, default 50"`
	}
	type dupGroup struct {
		Key   string        `json:"key"   jsonschema:"what matched: asin, isbn, or title+author"`
		Items []itemSummary `json:"items"`
	}
	type dupOut struct {
		Groups []dupGroup `json:"groups" jsonschema:"each group is one work with several copies"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "library_duplicates",
		Description: "Find items that appear to be the same work: identical asin, isbn, or title+author. Each group lists every copy with size, duration and path so you can pick which to keep.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dupIn) (*mcp.CallToolResult, dupOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, dupOut{}, err
		}

		groups := map[string][]abs.Item{}
		for i := range libs {
			if err := client.ItemsAll(ctx, libs[i].ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
				for i := range items {
					it := &items[i]
					m := &it.Media.Metadata
					var key string
					switch {
					case m.ASIN != "":
						key = "asin:" + strings.ToUpper(m.ASIN)
					case m.ISBN != "":
						key = "isbn:" + strings.ReplaceAll(m.ISBN, "-", "")
					default:
						key = "title:" + strings.ToLower(strings.TrimSpace(m.Title)) + "|" + strings.ToLower(strings.TrimSpace(m.AuthorDisplay()))
					}
					groups[key] = append(groups[key], *it)
				}
				return true
			}); err != nil {
				return nil, dupOut{}, err
			}
		}

		out := dupOut{Groups: []dupGroup{}}
		keys := make([]string, 0, len(groups))
		for k, items := range groups {
			if len(items) > 1 {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		limit := limitOr(in.Limit, 50)
		for _, k := range keys {
			if len(out.Groups) >= limit {
				break
			}
			out.Groups = append(out.Groups, dupGroup{Key: k, Items: summariseAll(groups[k])})
		}

		return nil, out, nil
	})
}

func finding(it *abs.Item, detail string) auditFinding {
	return auditFinding{
		ID:     it.ID,
		Title:  it.Title(),
		Author: it.Media.Metadata.AuthorDisplay(),
		Path:   it.RelPath,
		Detail: detail,
	}
}

// nativeFilter maps checks the server can filter itself to the encoded
// filter value.
var nativeFilter = map[string]string{
	"issues":      "issues",
	"cover":       abs.EncodeFilter("missing", "cover"),
	"description": abs.EncodeFilter("missing", "description"),
	"narrator":    abs.EncodeFilter("missing", "narrators"),
	"series":      abs.EncodeFilter("missing", "series"),
	"author":      abs.EncodeFilter("missing", "authors"),
	"genres":      abs.EncodeFilter("missing", "genres"),
	"year":        abs.EncodeFilter("missing", "publishedYear"),
	"publisher":   abs.EncodeFilter("missing", "publisher"),
	"language":    abs.EncodeFilter("missing", "language"),
	"no_audio":    abs.EncodeFilter("tracks", "none"),
}

const (
	chapterlessMinHours = 2
	staleFeedDays       = 90
)

var auditChecksByName = map[string]auditCheck{
	"unmatched": func(it *abs.Item) (string, bool) {
		if it.IsPodcast() {
			return "", false
		}
		m := it.Media.Metadata
		if m.ASIN == "" && m.ISBN == "" {
			return "no asin or isbn", true
		}
		return "", false
	},
	"cover": func(it *abs.Item) (string, bool) {
		if !it.HasCover() {
			return "no cover", true
		}
		return "", false
	},
	"description": func(it *abs.Item) (string, bool) {
		if strings.TrimSpace(it.Media.Metadata.Description) == "" {
			return "no description", true
		}
		return "", false
	},
	"narrator": func(it *abs.Item) (string, bool) {
		if !it.IsPodcast() && it.Media.Metadata.NarratorDisplay() == "" {
			return "no narrator", true
		}
		return "", false
	},
	"series": func(it *abs.Item) (string, bool) {
		if !it.IsPodcast() && len(it.Media.Metadata.SeriesDisplay()) == 0 {
			return "no series", true
		}
		return "", false
	},
	"author": func(it *abs.Item) (string, bool) {
		if it.Media.Metadata.AuthorDisplay() == "" {
			return "no author", true
		}
		return "", false
	},
	"genres": func(it *abs.Item) (string, bool) {
		if len(it.Media.Metadata.Genres) == 0 {
			return "no genres", true
		}
		return "", false
	},
	"year": func(it *abs.Item) (string, bool) {
		if !it.IsPodcast() && it.Media.Metadata.PublishedYear == "" {
			return "no published year", true
		}
		return "", false
	},
	"publisher": func(it *abs.Item) (string, bool) {
		if !it.IsPodcast() && it.Media.Metadata.Publisher == "" {
			return "no publisher", true
		}
		return "", false
	},
	"language": func(it *abs.Item) (string, bool) {
		if it.Media.Metadata.Language == "" {
			return "no language", true
		}
		return "", false
	},
	"chapters": func(it *abs.Item) (string, bool) {
		if it.IsPodcast() || it.Media.NumChapters > 0 || it.Media.Duration < chapterlessMinHours*3600 {
			return "", false
		}
		return fmt.Sprintf("%s of audio in %d file(s) with no chapters", fmtDuration(it.Media.Duration), it.Media.NumTracks), true
	},
	"single_file": func(it *abs.Item) (string, bool) {
		if it.IsPodcast() || it.Media.NumTracks != 1 || it.Media.NumChapters > 0 || it.Media.Duration < chapterlessMinHours*3600 {
			return "", false
		}
		return fmt.Sprintf("one %s audio file, no chapters", fmtDuration(it.Media.Duration)), true
	},
	"issues": func(it *abs.Item) (string, bool) {
		switch {
		case it.IsMissing:
			return "folder missing from disk", true
		case it.IsInvalid:
			return "no playable media in folder", true
		}
		return "", false
	},
	"no_audio": func(it *abs.Item) (string, bool) {
		if !it.IsPodcast() && it.Media.NumTracks == 0 && it.Media.NumAudioFiles == 0 {
			if it.Media.EbookFormat != "" {
				return "ebook only (" + it.Media.EbookFormat + ")", true
			}
			return "no audio files", true
		}
		return "", false
	},
	"path": checkPath,
	"stale_feed": func(it *abs.Item) (string, bool) {
		if !it.IsPodcast() {
			return "", false
		}
		if it.Media.Metadata.FeedURL == "" {
			return "no feed url", true
		}
		last := abs.Millis(it.Media.LastEpisodeCheck)
		if last.IsZero() {
			return "feed never checked", true
		}
		if age := time.Since(last); age > staleFeedDays*24*time.Hour {
			return fmt.Sprintf("feed last checked %d days ago", int(age.Hours()/24)), true
		}
		return "", false
	},
	"no_episodes": func(it *abs.Item) (string, bool) {
		if it.IsPodcast() && it.Media.NumEpisodes == 0 && len(it.Media.Episodes) == 0 {
			return "no episodes downloaded", true
		}
		return "", false
	},
}

// checkPath flags items whose folder name does not contain the title and
// whose parent folder does not name an author: a sign of a wrong match or a
// misfiled folder in an Author/Title or Author/Series/Title layout.
func checkPath(it *abs.Item) (string, bool) {
	if it.IsPodcast() || it.RelPath == "" || it.IsFile {
		return "", false
	}
	m := it.Media.Metadata
	rel := strings.Trim(it.RelPath, "/")
	folder := norm(path.Base(rel))
	title := norm(m.Title)
	if title == "" {
		return "", false
	}

	titleOK := strings.Contains(folder, title) || strings.Contains(title, folder)
	authorOK := true
	if parent := path.Dir(rel); parent != "." && parent != "/" {
		authorOK = false
		parts := strings.SplitSeq(parent, "/")
		for p := range parts {
			np := norm(p)
			if np == "" {
				continue
			}
			for a := range strings.SplitSeq(m.AuthorDisplay(), ",") {
				na := norm(a)
				if na != "" && (strings.Contains(np, na) || strings.Contains(na, np) || lastFirstMatch(np, na)) {
					authorOK = true
				}
			}
			for _, s := range m.SeriesDisplay() {
				if ns := norm(strings.Split(s, " #")[0]); ns != "" && strings.Contains(np, ns) {
					authorOK = true
				}
			}
		}
	}

	switch {
	case !titleOK && !authorOK:
		return fmt.Sprintf("folder %q does not match title %q or author %q", rel, m.Title, m.AuthorDisplay()), true
	case !titleOK:
		return fmt.Sprintf("folder %q does not contain title %q", path.Base(rel), m.Title), true
	case !authorOK:
		return fmt.Sprintf("parent folder %q does not name author %q", path.Dir(rel), m.AuthorDisplay()), true
	}

	return "", false
}

// lastFirstMatch treats "Last, First" folders as matching "First Last".
func lastFirstMatch(folder, author string) bool {
	parts := strings.Fields(author)
	if len(parts) < 2 {
		return false
	}
	return strings.Contains(folder, parts[len(parts)-1]+" "+strings.Join(parts[:len(parts)-1], " "))
}

// norm lowercases and strips punctuation so folder names and metadata
// compare loosely.
func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == ' ':
			b.WriteRune(r)
		case r == '_' || r == '-' || r == '.':
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// errNotBook is returned by tools that only make sense for books.
var errNotBook = errors.New("this item is a podcast, not a book")
