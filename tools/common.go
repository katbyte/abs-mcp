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

	"github.com/katbyte/abs-mcp/sdk/abs"
)

const (
	defaultLimit   = 25
	descriptionCap = 400
	// sweepPageSize is how many items a sweep of a library asks for at a time
	sweepPageSize = 500
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

// fmtTime is a time as RFC3339, in UTC, empty when unset.
func fmtTime(t abs.UnixMilli) string {
	if t.IsZero() {
		return ""
	}

	return t.UTC().Format(time.RFC3339)
}

// fmtDate is a time's day, empty when unset.
func fmtDate(t abs.UnixMilli) string {
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

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// looksLikeID reports whether s is an Audiobookshelf uuid (or a legacy
// "li_xxx" id).
func looksLikeID(s string) bool {
	return uuidRe.MatchString(s) || strings.HasPrefix(s, "li_")
}

// record is what a call to the server answered, or the call's error: record(gen.LibraryFindAll(ctx)).
func record[T any, R interface{ Record() (T, error) }](res R, err error) (T, error) {
	if err != nil {
		var none T
		return none, err
	}

	return res.Record()
}

// descFlag is a listing's desc query: 1 for the order reversed, nothing for the server's own.
func descFlag(desc bool) string {
	if desc {
		return "1"
	}

	return ""
}

// libraries are the libraries the API key can see.
func libraries(ctx context.Context, gen *abs.Client) ([]abs.Library, error) {
	all, err := record(gen.LibraryFindAll(ctx))
	if err != nil {
		return nil, err
	}

	return all.Libraries, nil
}

// resolveLibrary finds a library by name, ignoring case, or by id. No name is taken only when the server has exactly one library.
func resolveLibrary(ctx context.Context, gen *abs.Client, nameOrID string) (*abs.Library, error) {
	libs, err := libraries(ctx, gen)
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

// resolveLibraries is the one library named, or every library when no name is given.
func resolveLibraries(ctx context.Context, gen *abs.Client, nameOrID string) ([]abs.Library, error) {
	libs, err := libraries(ctx, gen)
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

// pickLibrary finds one library by id or name. Audiobookshelf lets two libraries share a name, so a name more than one has is refused with
// their ids, rather than settled by whichever the server lists first.
func pickLibrary(libs []abs.Library, nameOrID string) (int, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	var named []int
	for i := range libs {
		if string(libs[i].Id) == nameOrID {
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
		ids = append(ids, string(libs[i].Id))
	}

	return -1, fmt.Errorf("%d libraries are named %q; pass an id: %s", len(named), nameOrID, strings.Join(ids, ", "))
}

func libraryNames(libs []abs.Library) string {
	names := make([]string, 0, len(libs))
	for _, l := range libs {
		names = append(names, l.Name)
	}

	return strings.Join(names, ", ")
}

// resolveItem finds a library item by id, or by title searched across the library given, or every library when none is. A title must match
// exactly one item, or be part of exactly one title when none matches whole; otherwise the candidates are listed in the error so the caller
// can pick an id. It is for the tools that only read: see resolveItemToChange for the rest.
func resolveItem(ctx context.Context, gen *abs.Client, library, idOrTitle string) (*abs.LibraryItemExpanded, error) {
	return findItem(ctx, gen, library, idOrTitle, false)
}

// resolveItemToChange is resolveItem for a tool that writes or deletes: a title must be the whole title of exactly one item. A title that is
// only part of one would otherwise change the book it happens to be part of, "Foundation" deleting Foundation and Empire once Foundation
// itself is gone, so that book is named in the refusal instead.
func resolveItemToChange(ctx context.Context, gen *abs.Client, library, idOrTitle string) (*abs.LibraryItemExpanded, error) {
	return findItem(ctx, gen, library, idOrTitle, true)
}

func findItem(ctx context.Context, gen *abs.Client, library, idOrTitle string, whole bool) (*abs.LibraryItemExpanded, error) {
	idOrTitle = strings.TrimSpace(idOrTitle)
	if idOrTitle == "" {
		return nil, errors.New("item id or title is required")
	}
	if looksLikeID(idOrTitle) {
		return getItem(ctx, gen, abs.LibraryItemID(idOrTitle))
	}

	libs, err := resolveLibraries(ctx, gen, library)
	if err != nil {
		return nil, err
	}

	// the server searches more than the title (subtitle, asin and isbn, and on older servers authors and narrators too) and does not say which
	// field matched, so the title is judged here: a hit whose title does not even contain the words asked for is not the item that was named,
	// however alone it is
	var exact, partial, other []abs.LibraryItemExpanded
	for i := range libs {
		found, err := record(gen.LibrarySearch(ctx, libs[i].Id, abs.LibrarySearchOptions{Q: idOrTitle, Limit: new(10)}))
		if err != nil {
			return nil, err
		}

		var matches []*abs.LibraryItemExpanded
		for _, m := range found.Book {
			matches = append(matches, m.LibraryItem)
		}
		if len(matches) == 0 {
			for _, m := range found.Podcast {
				matches = append(matches, m.LibraryItem)
			}
		}
		for _, m := range matches {
			if m == nil {
				continue
			}
			it := *m
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
		return getItem(ctx, gen, exact[0].Id)
	case len(exact) == 0 && len(partial) == 1 && whole:
		return nil, fmt.Errorf("no item titled %q; the nearest is %s: pass its id or its whole title to change it", idOrTitle, itemNames(partial))
	case len(exact) == 0 && len(partial) == 1:
		return getItem(ctx, gen, partial[0].Id)
	case len(exact)+len(partial)+len(other) == 0:
		return nil, fmt.Errorf("no item titled %q", idOrTitle)
	case len(exact)+len(partial) == 0:
		return nil, fmt.Errorf("no item titled %q; %d matched on another field (subtitle, asin or isbn), pass an id if one of them is meant: %s", idOrTitle, len(other), itemNames(other))
	}

	all := slices.Concat(exact, partial, other)

	return nil, fmt.Errorf("%d items match %q; pass an id: %s", len(all), idOrTitle, itemNames(all))
}

// itemNames lists items as "title" by author (id), for an error a caller can pick an id out of.
func itemNames(items []abs.LibraryItemExpanded) string {
	names := make([]string, 0, len(items))
	for i := range items {
		m := items[i].Minified()
		names = append(names, fmt.Sprintf("%q by %s (%s)", m.Title(), m.Media.Metadata.AuthorDisplay(), m.Id))
	}

	return strings.Join(names, "; ")
}

// getItem is one item whole, with the API key user's progress in it.
func getItem(ctx context.Context, gen *abs.Client, id abs.LibraryItemID) (*abs.LibraryItemExpanded, error) {
	it, err := record(gen.LibraryItemFindOne(ctx, id, abs.LibraryItemFindOneOptions{Expanded: "1", Include: "progress"}))
	if err != nil {
		return nil, err
	}
	filledExpanded(&it)

	return &it, nil
}

// itemsBatch is several items whole, in one request.
func itemsBatch(ctx context.Context, gen *abs.Client, ids []abs.LibraryItemID) ([]abs.LibraryItemExpanded, error) {
	got, err := record(gen.LibraryItemBatchGet(ctx, abs.LibraryItemBatchGetRequest{LibraryItemIds: ids}))
	if err != nil {
		return nil, err
	}
	for i := range got.LibraryItems {
		filledExpanded(&got.LibraryItems[i])
	}

	return got.LibraryItems, nil
}

// eachItem hands fn every item of a library as its list sends them, a page at a time, until fn returns false.
func eachItem(ctx context.Context, gen *abs.Client, library abs.LibraryID, opts abs.LibraryGetLibraryItemsOptions, fn func(it *abs.LibraryItemMinified) bool) error {
	opts.Limit, opts.Minified = new(sweepPageSize), "1"
	for it, err := range gen.LibraryGetLibraryItemsEach(ctx, library, opts) {
		if err != nil {
			return err
		}
		filledMinified(&it)
		if !fn(&it) {
			return nil
		}
	}

	return nil
}

// filledMinified and filledExpanded give an item its media and metadata, empty, where the server sent none, which it never does for an item
// it has, so the tools read them without a guard.
func filledMinified(it *abs.LibraryItemMinified) {
	if it.Media == nil {
		it.Media = &abs.LibraryItemMinifiedMedia{}
	}
	if it.Media.Metadata == nil {
		it.Media.Metadata = &abs.LibraryItemMinifiedMediaMetadata{}
	}
}

func filledExpanded(it *abs.LibraryItemExpanded) {
	if it.Media == nil {
		it.Media = &abs.LibraryItemExpandedMedia{}
	}
	if it.Media.Metadata == nil {
		it.Media.Metadata = &abs.LibraryItemExpandedMediaMetadata{}
	}
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

// titleContains reports whether a title carries the words asked for, the way
// the server's own title search matches: anywhere, ignoring case.
func titleContains(title, query string) bool {
	return strings.Contains(strings.ToLower(title), strings.ToLower(query))
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
