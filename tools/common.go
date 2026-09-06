package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
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

func mb(bytes int64) int64 { return bytes / (1 << 20) }

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

	for i := range libs {
		if strings.EqualFold(libs[i].Name, nameOrID) || libs[i].ID == nameOrID {
			return &libs[i], nil
		}
	}

	return nil, fmt.Errorf("no library named %q (have: %s)", nameOrID, libraryNames(libs))
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
	for i := range libs {
		if strings.EqualFold(libs[i].Name, nameOrID) || libs[i].ID == nameOrID {
			return libs[i : i+1], nil
		}
	}

	return nil, fmt.Errorf("no library named %q (have: %s)", nameOrID, libraryNames(libs))
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
// item; otherwise the candidates are listed in the error so the caller can
// pick an id.
func resolveItem(ctx context.Context, client *abs.Client, library, idOrTitle string) (*abs.Item, error) {
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

	var exact, partial []abs.Item
	for i := range libs {
		res, err := client.Search(ctx, libs[i].ID, idOrTitle, 10)
		if err != nil {
			return nil, err
		}
		matches := res.Items()
		for i := range matches {
			if strings.EqualFold(matches[i].LibraryItem.Title(), idOrTitle) {
				exact = append(exact, matches[i].LibraryItem)
			} else {
				partial = append(partial, matches[i].LibraryItem)
			}
		}
	}

	switch {
	case len(exact) == 1:
		return client.Item(ctx, exact[0].ID)
	case len(exact) == 0 && len(partial) == 1:
		return client.Item(ctx, partial[0].ID)
	case len(exact)+len(partial) == 0:
		return nil, fmt.Errorf("no item titled %q", idOrTitle)
	}

	all := append(exact, partial...) //nolint:gocritic // building the candidate list for the error
	names := make([]string, 0, len(all))
	for i := range all {
		it := &all[i]
		names = append(names, fmt.Sprintf("%q by %s (%s)", it.Title(), it.Media.Metadata.AuthorDisplay(), it.ID))
	}

	return nil, fmt.Errorf("%d items match %q; pass an id: %s", len(all), idOrTitle, strings.Join(names, "; "))
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
