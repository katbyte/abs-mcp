package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
)

const (
	defaultLimit   = 25
	descriptionCap = 400
)

// fmtDuration renders seconds as "12h 34m" (or "34m 5s" under an hour).
func fmtDuration(seconds float64) string {
	if seconds <= 0 {
		return ""
	}
	d := time.Duration(seconds * float64(time.Second)).Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// wholeSec rounds seconds for a length, position or total in an answer: the
// server's fractions of a second there are noise. Chapter and bookmark times
// stay as the server has them, since they are passed back and compared.
func wholeSec(seconds float64) int { return int(math.Round(seconds)) }

// fmtTime renders an epoch-milliseconds timestamp as RFC3339, empty when
// unset.
func fmtTime(ms int64) string {
	t := abs.Millis(ms)
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// fmtDate renders an epoch-milliseconds timestamp as a date.
func fmtDate(ms int64) string {
	t := abs.Millis(ms)
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

func percent(p float64) int { return int(math.Round(p * 100)) }

// clip shortens long free text so descriptions do not dominate a response.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := strings.LastIndex(s[:n], " ")
	if cut < n/2 {
		cut = n
	}
	return s[:cut] + "..."
}

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// plain strips HTML tags from a description.
func plain(s string) string {
	return strings.TrimSpace(htmlTagRe.ReplaceAllString(s, ""))
}

func limitOr(limit, def int) int {
	if limit <= 0 {
		return def
	}
	return limit
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// looksLikeID reports whether s is an Audiobookshelf uuid (or a legacy
// "li_xxx" id).
func looksLikeID(s string) bool {
	return uuidRe.MatchString(s) || strings.HasPrefix(s, "li_")
}

// resolveLibrary finds a library by name (case-insensitive) or id. An empty
// name is accepted only when the server has exactly one library.
func resolveLibrary(ctx context.Context, client *abs.Client, nameOrID string) (*abs.Library, error) {
	libs, err := client.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	if len(libs) == 0 {
		return nil, errors.New("the server has no libraries visible to this API key")
	}

	if nameOrID == "" {
		if len(libs) == 1 {
			return &libs[0], nil
		}
		return nil, fmt.Errorf("library is required when the server has more than one (have: %s)", libraryNames(libs))
	}

	i, err := pickLibrary(libs, nameOrID)
	if err != nil {
		return nil, err
	}

	return &libs[i], nil
}

// resolveLibraries returns the one named library, or every library when the
// name is empty.
func resolveLibraries(ctx context.Context, client *abs.Client, nameOrID string) ([]abs.Library, error) {
	libs, err := client.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	if nameOrID == "" {
		return libs, nil
	}
	i, err := pickLibrary(libs, nameOrID)
	if err != nil {
		return nil, err
	}

	return libs[i : i+1], nil
}

// pickLibrary finds one library by id or name. Audiobookshelf lets two
// libraries share a name, so a name that matches more than one is refused
// with their ids rather than settled by whichever the server lists first.
func pickLibrary(libs []abs.Library, nameOrID string) (int, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	var named []int
	for i := range libs {
		if libs[i].ID == nameOrID {
			return i, nil
		}
		if strings.EqualFold(libs[i].Name, nameOrID) {
			named = append(named, i)
		}
	}
	switch len(named) {
	case 1:
		return named[0], nil
	case 0:
		return -1, fmt.Errorf("no library named %q (have: %s)", nameOrID, libraryNames(libs))
	}
	ids := make([]string, 0, len(named))
	for _, i := range named {
		ids = append(ids, libs[i].ID)
	}

	return -1, fmt.Errorf("%d libraries are named %q; pass an id: %s", len(named), nameOrID, strings.Join(ids, ", "))
}

// oneNamed picks the one record whose name is the one asked for, or says how
// many there were: none, or several, listed by id. Collections and playlists
// can share a name, and taking the first would act on whichever the server
// happened to list first.
func oneNamed[T any](kind, name string, all []T, nameOf, idOf func(*T) string) (*T, error) {
	var matches []*T
	names := make([]string, 0, len(all))
	for i := range all {
		if strings.EqualFold(strings.TrimSpace(nameOf(&all[i])), name) {
			matches = append(matches, &all[i])
		}
		names = append(names, nameOf(&all[i]))
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("no %s named %q (have: %s)", kind, name, strings.Join(names, ", "))
	}
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, idOf(m))
	}

	return nil, fmt.Errorf("%d %ss are named %q; pass an id: %s", len(matches), kind, name, strings.Join(ids, ", "))
}

func libraryNames(libs []abs.Library) string {
	names := make([]string, 0, len(libs))
	for _, l := range libs {
		names = append(names, l.Name)
	}
	return strings.Join(names, ", ")
}

// resolveItem finds a library item by id, or by title searched across the
// given library (all libraries when empty). A title must match exactly one
// item, or be part of exactly one title when none matches whole; otherwise
// the candidates are listed in the error so the caller can pick an id. It is
// for the tools that only read: see resolveItemToChange for the rest.
func resolveItem(ctx context.Context, client *abs.Client, library, idOrTitle string) (*abs.Item, error) {
	return findItem(ctx, client, library, idOrTitle, false)
}

// resolveItemToChange is resolveItem for a tool that writes or deletes: a
// title must be the whole title of exactly one item. A title that is only
// part of one would otherwise change the book it happens to be part of -
// "Foundation" deleting Foundation and Empire once Foundation itself is gone -
// so that book is named in the refusal instead.
func resolveItemToChange(ctx context.Context, client *abs.Client, library, idOrTitle string) (*abs.Item, error) {
	return findItem(ctx, client, library, idOrTitle, true)
}

func findItem(ctx context.Context, client *abs.Client, library, idOrTitle string, whole bool) (*abs.Item, error) {
	idOrTitle = strings.TrimSpace(idOrTitle)
	if idOrTitle == "" {
		return nil, errors.New("item id or title is required")
	}
	if looksLikeID(idOrTitle) {
		return client.Item(ctx, idOrTitle)
	}

	libs, err := resolveLibraries(ctx, client, library)
	if err != nil {
		return nil, err
	}

	// the server searches more than the title (subtitle, asin and isbn, and on
	// older servers authors and narrators too) and does not say which field
	// matched, so the title is judged here: a hit whose title does not even
	// contain the words asked for is not the item that was named, however
	// alone it is
	var exact, partial, other []abs.Item
	for i := range libs {
		res, err := client.Search(ctx, libs[i].ID, idOrTitle, 10)
		if err != nil {
			return nil, err
		}
		matches := res.Items()
		for i := range matches {
			it := matches[i].LibraryItem
			switch {
			case strings.EqualFold(it.Title(), idOrTitle):
				exact = append(exact, it)
			case titleContains(it.Title(), idOrTitle):
				partial = append(partial, it)
			default:
				other = append(other, it)
			}
		}
	}

	switch {
	case len(exact) == 1:
		return client.Item(ctx, exact[0].ID)
	case len(exact) == 0 && len(partial) == 1 && whole:
		return nil, fmt.Errorf("no item titled %q; the nearest is %s: pass its id or its whole title to change it", idOrTitle, itemNames(partial))
	case len(exact) == 0 && len(partial) == 1:
		return client.Item(ctx, partial[0].ID)
	case len(exact)+len(partial)+len(other) == 0:
		return nil, fmt.Errorf("no item titled %q", idOrTitle)
	case len(exact)+len(partial) == 0:
		return nil, fmt.Errorf("no item titled %q; %d matched on another field (subtitle, asin or isbn), pass an id if one of them is meant: %s",
			idOrTitle, len(other), itemNames(other))
	}

	all := slices.Concat(exact, partial, other)

	return nil, fmt.Errorf("%d items match %q; pass an id: %s", len(all), idOrTitle, itemNames(all))
}

// titleContains reports whether a title carries the words asked for, the way
// the server's own title search matches: anywhere, ignoring case.
func titleContains(title, query string) bool {
	return strings.Contains(strings.ToLower(title), strings.ToLower(query))
}

// itemNames lists items as "title" by author (id), for an error a caller can
// pick an id out of.
func itemNames(items []abs.Item) string {
	names := make([]string, 0, len(items))
	for i := range items {
		it := &items[i]
		names = append(names, fmt.Sprintf("%q by %s (%s)", it.Title(), it.Media.Metadata.AuthorDisplay(), it.ID))
	}

	return strings.Join(names, "; ")
}

// strPtr returns a pointer for non-empty strings so "unset" and "clear" can
// both be expressed: callers pass clear=true to send an empty string.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

//go:fix inline
