package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit_covers is everything wrong with cover art, in five parts. Three come
// from the library alone: a book with no cover, a cover that is not square
// (audiobook art is square by convention, so a tall book-jacket scan stands
// out), and a cover too small to look right. Two come from asking the store
// a matched book came from: a bigger copy of the same picture on offer, and
// a cover that is a different picture from the one the store shows for that
// asin, which is either a wrong match or another edition's art, and either
// way worth a look. The store checks cost a provider request and two image
// fetches per book, so they run over a page of matched books at a time.
//
// item_cover_upgrade does the fix: for each book it fetches the store's
// full-size cover and sets it when it is bigger and, unless told otherwise,
// the same picture as what is there now, so a curated cover is never
// replaced by another edition's art by accident.

const (
	defaultCoverTolerance = 0.1
	defaultCoverMinPixels = 400
	defaultUpgradeFactor  = 1.5
	coverPageSize         = 50
	coverPageMax          = 200
	coverFetchMax         = 32 << 20 // a cover larger than this is not a cover
)

// coverHTTP fetches images from the stores' image hosts.
var coverHTTP = &http.Client{Timeout: 60 * time.Second}

// amazonSize is the size suffix Amazon's image host puts on a URL:
// "51abc._SL500_.jpg" is the 500-pixel rendition of "51abc.jpg".
var amazonSize = regexp.MustCompile(`\._S[A-Z]{1,2}\d+_(\.[a-zA-Z]+)$`)

// fullSizeCoverURL is the store's cover URL without its size suffix, which is
// the original the renditions are made from. A URL of any other shape is
// returned as it is.
func fullSizeCoverURL(u string) string {
	return amazonSize.ReplaceAllString(u, "$1")
}

type coverRow struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Problem  string `json:"problem"               jsonschema:"missing: no cover; ratio: not square (with store, a second row carries the store's square art: item_cover_upgrade square=true takes it); small: narrower than min_pixels; banner: an 'Only from Audible' ribbon across the bottom-right corner; upgrade: the store has the same picture bigger, or has one where the book has none; differs: the store's cover for the book's asin is another picture"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Ratio    string `json:"ratio,omitempty"`
	Why      string `json:"why"`
	StoreW   int    `json:"store_width,omitempty" jsonschema:"the width of the store's full-size cover"`
	StoreURL string `json:"store_url,omitempty"   jsonschema:"the store's full-size cover, for item_cover_edit url= or item_cover_upgrade"`
	FoundIn  string `json:"found_in,omitempty"    jsonschema:"the store that had the asin"`
	Distance string `json:"distance,omitempty"    jsonschema:"differs only: how far apart the two pictures are, 'phash 23, dhash 19'; under about 10 is the same picture"`
	CoverURL string `json:"cover_url,omitempty"   jsonschema:"differs only: the library's cover as the server serves it, to look at beside store_url"`
}

type coverCounts struct {
	Missing int `json:"missing"`
	Ratio   int `json:"ratio"`
	Small   int `json:"small"`
	Banner  int `json:"banner,omitempty"  jsonschema:"only with banner=true"`
	Upgrade int `json:"upgrade,omitempty"`
	Differs int `json:"differs,omitempty"`
}

type coversOut struct {
	Checked  int         `json:"covers_checked"`
	Skipped  int         `json:"skipped,omitempty"       jsonschema:"covers in a format Go cannot read (webp), or files that are gone"`
	Store    int         `json:"store_checked,omitempty" jsonschema:"matched books compared with their store, this page"`
	Found    int         `json:"total_findings"`
	Counts   coverCounts `json:"counts"`
	Findings []coverRow  `json:"findings"`
	NextPage *int        `json:"next_page,omitempty"     jsonschema:"pass as page to compare the next books with their store; absent when every matched book has been compared"`
}

// coverLocalScope is what the library-only sweep judges.
type coverLocalScope struct {
	Tolerance float64
	MinPixels int
	Banner    bool // also fetch each cover and look for the Audible ribbon
	Limit     int  // findings kept; 0 keeps none, as audit_all wants
}

// sweepCoverLocal measures every cover in a library from the library alone:
// the ones missing, not square within tolerance, or narrower than minPixels,
// and with Banner the ones wearing the "Only from Audible" ribbon, which
// costs a fetch of the server's 400-pixel copy of every cover.
func sweepCoverLocal(ctx context.Context, client *abs.Client, libraryID string, scope coverLocalScope, out *coversOut) error {
	tolerance, minPixels, limit := scope.Tolerance, scope.MinPixels, scope.Limit
	return client.ItemsAll(ctx, libraryID, abs.ItemsOptions{Minified: true}, func(items []abs.Item) bool {
		for j := range items {
			it := &items[j]
			if it.IsPodcast() {
				continue
			}
			row := coverRow{ID: it.ID, Title: it.Title()}
			if !it.HasCover() { // the listing already says so: no request needed
				row.Problem, row.Why = "missing", "no cover"
				out.Counts.Missing++
				out.add(row, limit)
				continue
			}
			w, h, err := client.CoverSize(ctx, it.ID)
			if err != nil {
				out.Skipped++ // a format we cannot read, or the file is gone
				continue
			}
			out.Checked++
			row.Width, row.Height = w, h
			if h > 0 {
				row.Ratio = fmt.Sprintf("%.2f", float64(w)/float64(h))
			}
			switch {
			case h == 0 || w == 0:
				row.Problem, row.Why = "ratio", "cover has no dimensions"
				out.Counts.Ratio++
			case math.Abs(float64(w)/float64(h)-1) > tolerance:
				row.Problem, row.Why = "ratio", fmt.Sprintf("not square (%dx%d)", w, h)
				out.Counts.Ratio++
			case w < minPixels:
				row.Problem, row.Why = "small", fmt.Sprintf("only %dpx wide", w)
				out.Counts.Small++
			}
			if row.Problem != "" {
				out.add(row, limit)
			}
			if !scope.Banner {
				continue
			}
			img, err := libraryCoverImage(ctx, client, it.ID)
			if err != nil {
				out.Skipped++
				continue
			}
			if region, found := findAudibleBanner(img); found {
				banner := coverRow{
					ID: it.ID, Title: it.Title(), Width: w, Height: h, Ratio: row.Ratio, Problem: "banner",
					Why: fmt.Sprintf("an 'Only from Audible' ribbon across the bottom-right corner (%.0f%% yellow along the band)", region.Inside*100),
				}
				out.Counts.Banner++
				out.add(banner, limit)
			}
		}
		return true
	})
}

func (o *coversOut) add(row coverRow, limit int) {
	o.Found++
	if len(o.Findings) < limit {
		o.Findings = append(o.Findings, row)
	}
}

// storeCover is what the store says about a matched book's cover.
type storeCover struct {
	URL     string // the full-size cover
	Width   int
	Height  int
	FoundIn string
	Hash    coverHash
	Banner  bool // the store's copy wears the "Only from Audible" ribbon
}

// storeCoverFor looks a matched book's asin up at the stores in order and
// measures and hashes the cover the first hit carries. nil when no store
// has the asin or the record has no cover.
func storeCoverFor(ctx context.Context, client *abs.Client, it *abs.Item, providers []string) (*storeCover, error) {
	asin := strings.TrimSpace(it.Media.Metadata.ASIN)
	for _, provider := range providers {
		results, err := client.SearchBooks(ctx, provider, asin, "", it.ID)
		if err != nil {
			return nil, err
		}
		idx := slices.IndexFunc(results, func(r abs.BookSearchResult) bool { return strings.EqualFold(r.ASIN, asin) })
		if idx < 0 {
			continue
		}
		if results[idx].Cover == "" {
			return nil, nil
		}
		sc := &storeCover{URL: fullSizeCoverURL(results[idx].Cover), FoundIn: provider}
		w, h, err := remoteImageSize(ctx, sc.URL)
		if err != nil {
			// the full-size guess may not exist; fall back to the rendition
			sc.URL = results[idx].Cover
			if w, h, err = remoteImageSize(ctx, sc.URL); err != nil {
				return nil, fmt.Errorf("%s cover for %s: %w", provider, it.Title(), err)
			}
		}
		sc.Width, sc.Height = w, h
		img, err := remoteImage(ctx, results[idx].Cover) // the rendition is enough to hash and far smaller
		if err != nil {
			return nil, fmt.Errorf("%s cover for %s: %w", provider, it.Title(), err)
		}
		sc.Hash = hashImage(img)
		_, sc.Banner = findAudibleBanner(img)
		return sc, nil
	}
	return nil, nil
}

// libraryCoverImage is the cover as the server serves it, resized to 400
// wide as JPEG, which is small and is a format Go can read whatever the file
// on disk is.
func libraryCoverImage(ctx context.Context, client *abs.Client, itemID string) (image.Image, error) {
	body, err := client.Cover(ctx, itemID, 400, 0, "jpeg")
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	img, _, err := image.Decode(io.LimitReader(body, coverFetchMax))
	if err != nil {
		return nil, err
	}
	return img, nil
}

// libraryCoverHash hashes the cover as the server serves it.
func libraryCoverHash(ctx context.Context, client *abs.Client, itemID string) (coverHash, error) {
	img, err := libraryCoverImage(ctx, client, itemID)
	if err != nil {
		return coverHash{}, err
	}
	return hashImage(img), nil
}

// remoteImageSize reads an image's dimensions from its header, closing the
// body as soon as they are known.
func remoteImageSize(ctx context.Context, u string) (width, height int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return 0, 0, err
	}
	resp, err := coverHTTP.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("%s: %s", u, resp.Status)
	}
	cfg, _, err := image.DecodeConfig(bufio.NewReader(resp.Body))
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", u, err)
	}
	return cfg.Width, cfg.Height, nil
}

// remoteImage fetches and decodes an image.
func remoteImage(ctx context.Context, u string) (image.Image, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := coverHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	img, _, err := image.Decode(io.LimitReader(resp.Body, coverFetchMax))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	return img, nil
}

// coverStoreScope is which matched books a store comparison covers.
type coverStoreScope struct {
	Providers []string
	Factor    float64 // how much wider the store's must be to count as an upgrade
	Tolerance float64 // how far from square is still square
	PageSize  int
	Page      int
}

// sweepCoverStore compares a page of a library's matched books with their
// store: a bigger copy of the same picture is an upgrade, and so is any
// cover where the book has none; another picture is reported as differing,
// with both images to look at. Says whether a further page exists.
func sweepCoverStore(ctx context.Context, client *abs.Client, lib *abs.Library, scope coverStoreScope, out *coversOut) (bool, error) {
	if lib.IsPodcast() {
		return false, nil
	}
	providers := providersFor(scope.Providers, lib)
	page, err := client.Items(ctx, lib.ID, abs.ItemsOptions{Limit: scope.PageSize, Page: scope.Page, Sort: "media.metadata.title", Minified: true})
	if err != nil {
		return false, err
	}
	for i := range page.Results {
		it := &page.Results[i]
		if it.IsPodcast() || strings.TrimSpace(it.Media.Metadata.ASIN) == "" {
			continue
		}
		row, err := compareWithStore(ctx, client, it, providerOrder(it, providers), scope.Factor, scope.Tolerance)
		if err != nil {
			return false, err
		}
		out.Store++
		if row == nil {
			continue
		}
		switch row.Problem {
		case "upgrade":
			out.Counts.Upgrade++
		case "differs":
			out.Counts.Differs++
		case "ratio":
			out.Counts.Ratio++ // the library-only sweep counted it too; this row carries the store's art
		}
		out.Found++
		out.Findings = append(out.Findings, *row) // a page is already a bound: the limit is for the library-wide checks
	}
	return (scope.Page+1)*scope.PageSize < page.Total, nil
}

// compareWithStore is one book against its store's cover: nil when they
// agree and the store's is not worth having, or when no store has the asin.
func compareWithStore(ctx context.Context, client *abs.Client, it *abs.Item, providers []string, factor, tolerance float64) (*coverRow, error) {
	sc, err := storeCoverFor(ctx, client, it, providers)
	if err != nil || sc == nil {
		return nil, err
	}
	row := coverRow{ID: it.ID, Title: it.Title(), StoreW: sc.Width, StoreURL: sc.URL, FoundIn: sc.FoundIn}
	if !it.HasCover() {
		row.Problem, row.Why = "upgrade", fmt.Sprintf("no cover; %s has one %dpx wide", sc.FoundIn, sc.Width)
		return &row, nil
	}
	w, h, err := client.CoverSize(ctx, it.ID)
	if err != nil && !errors.Is(err, abs.ErrNoCover) {
		w, h = 0, 0 // unreadable on disk (webp): the size is unknown, the picture can still be compared
	}
	row.Width, row.Height = w, h
	if w > 0 && h > 0 && math.Abs(float64(w)/float64(h)-1) > tolerance {
		// a jacket scan against square store art: the hash would say
		// "different picture", which is true and beside the point. Store
		// art is square by convention, so this is the ratio problem with
		// its fix attached
		row.Problem = "ratio"
		row.Ratio = fmt.Sprintf("%.2f", float64(w)/float64(h))
		row.Why = fmt.Sprintf("not square (%dx%d); %s has square art %dpx wide", w, h, sc.FoundIn, sc.Width)
		return &row, nil
	}
	have, err := libraryCoverHash(ctx, client, it.ID)
	if err != nil {
		return nil, fmt.Errorf("cover of %s: %w", it.Title(), err)
	}
	p, d := have.distance(sc.Hash)
	if !have.samePicture(sc.Hash) {
		row.Problem = "differs"
		row.Why = fmt.Sprintf("%s shows a different picture for asin %s", sc.FoundIn, strings.TrimSpace(it.Media.Metadata.ASIN))
		row.Distance = fmt.Sprintf("phash %d, dhash %d", p, d)
		row.CoverURL = client.BaseURL() + "/api/items/" + it.ID + "/cover"
		return &row, nil
	}
	if w > 0 && float64(sc.Width) >= float64(w)*factor {
		row.Problem = "upgrade"
		row.Why = fmt.Sprintf("same picture, %dpx wide here and %dpx at %s", w, sc.Width, sc.FoundIn)
		row.Distance = fmt.Sprintf("phash %d, dhash %d", p, d)
		return &row, nil
	}
	return nil, nil
}

func registerCoverAudit(r *registry) {
	client := r.client

	type coversIn struct {
		Library   string   `json:"library,omitempty"    jsonschema:"library name or id; default every book library"`
		Tolerance float64  `json:"tolerance,omitempty"  jsonschema:"how far from square counts as square, default 0.1 (a 10% deviation)"`
		MinPixels int      `json:"min_pixels,omitempty" jsonschema:"report covers narrower than this, default 400"`
		Limit     int      `json:"limit,omitempty"      jsonschema:"maximum findings from the library-wide checks, default 50; with store, also how many books to compare per call, at most 200, and every store finding on the page is reported"`
		Banner    bool     `json:"banner,omitempty"     jsonschema:"also look for the 'Only from Audible' ribbon across the bottom-right corner of every cover: a fetch of the server's 400-pixel copy per cover, found by its colour and angle"`
		Store     bool     `json:"store,omitempty"      jsonschema:"also compare each matched book's cover with its store's: a bigger copy of the same picture is an upgrade, another picture is reported as differing. A provider request and two image fetches per book, so this works through a page of matched books per call: pass next_page back as page"`
		Page      int      `json:"page,omitempty"       jsonschema:"with store: which page of matched books, from next_page"`
		Providers []string `json:"providers,omitempty"  jsonschema:"with store: where to look the asins up, in order, default the server's --providers, else the library's provider"`
		Factor    float64  `json:"factor,omitempty"     jsonschema:"with store: how much wider the store's cover must be to count as an upgrade, default 1.5"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_covers",
		Description: "Everything wrong with cover art. Missing: no cover. Ratio: not square, so a tall book-jacket scan (audiobook art is square by convention). Small: narrower than min_pixels. " +
			"These read every cover file's header, one request per item with a cover, so audit_all runs this only with deep. " +
			"With banner=true, every cover is fetched small and checked for the 'Only from Audible' ribbon across its bottom-right corner, found by colour and angle. " +
			"With store=true, two more: upgrade, where the book's store has the same picture bigger (or has one where the book has none), with the full-size url for item_cover_edit or item_cover_upgrade; and differs, where the store's cover for the book's asin is another picture, which is a wrong match or another edition's art, with both images to look at and how far apart they are; a cover that is not square is reported as ratio with the store's square art attached rather than as differs, since square is the convention and the store's art is the fix. " +
			"Same or different is decided by perceptual hash (a DCT hash, with a difference hash as a second opinion), so a resize, a recompression or a shaved border is still the same picture. The store checks work through a page of matched books per call; next_page says when there are more. " +
			"Fix a missing, tall or small cover with item_cover_search then item_cover_edit, and an upgrade with item_cover_upgrade.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in coversIn) (*mcp.CallToolResult, coversOut, error) {
		tolerance := in.Tolerance
		if tolerance <= 0 {
			tolerance = defaultCoverTolerance
		}
		minPixels := in.MinPixels
		if minPixels <= 0 {
			minPixels = defaultCoverMinPixels
		}
		limit := limitOr(in.Limit, coverPageSize)
		factor := in.Factor
		if factor <= 1 {
			factor = defaultUpgradeFactor
		}

		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, coversOut{}, err
		}

		out := coversOut{Findings: []coverRow{}}
		local := coverLocalScope{Tolerance: tolerance, MinPixels: minPixels, Banner: in.Banner, Limit: limit}
		for i := range libs {
			if err := sweepCoverLocal(ctx, client, libs[i].ID, local, &out); err != nil {
				return nil, coversOut{}, err
			}
		}
		if in.Store {
			if len(libs) != 1 {
				return nil, coversOut{}, errors.New("store compares one library at a time: pass library")
			}
			scope := coverStoreScope{Providers: in.Providers, Factor: factor, Tolerance: tolerance, PageSize: min(limit, coverPageMax), Page: in.Page}
			more, err := sweepCoverStore(ctx, client, &libs[0], scope, &out)
			if err != nil {
				return nil, coversOut{}, err
			}
			if more {
				out.NextPage = new(in.Page + 1)
			}
		}

		return nil, out, nil
	})

	type upgradeIn struct {
		Items      []string `json:"items"                 jsonschema:"the books to upgrade, by id or exact title"`
		Library    string   `json:"library,omitempty"     jsonschema:"library name or id, for resolving titles"`
		Providers  []string `json:"providers,omitempty"   jsonschema:"where to look the asins up, in order, default the server's --providers, else the library's provider"`
		Factor     float64  `json:"factor,omitempty"      jsonschema:"how much wider the store's cover must be to replace the current one, default 1.5; a book with no cover takes any"`
		AnyPicture bool     `json:"any_picture,omitempty" jsonschema:"replace the cover even when the store's is a different picture; off by default so a curated cover is not swapped for another edition's art"`
		Square     bool     `json:"square,omitempty"      jsonschema:"take the store's cover whenever the current one is not square (a jacket scan), whatever the picture and size: audiobook art is square by convention"`
		Tolerance  float64  `json:"tolerance,omitempty"   jsonschema:"with square: how far from square is still square, default 0.1"`
		Preview    bool     `json:"preview,omitempty"     jsonschema:"say what would happen and change nothing"`
	}
	type upgradeRow struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Action   string `json:"action"                jsonschema:"upgraded: the store's cover was set; would_upgrade: preview; kept_size: the store's is not enough bigger; kept_picture: the store's is another picture, pass any_picture to take it; kept_banner: the store's copy wears the 'Only from Audible' ribbon and the current cover does not, so it is not taken; no_asin: the book is not matched; not_found: no store has the asin or its record has no cover"`
		Width    int    `json:"width,omitempty"       jsonschema:"the cover before"`
		StoreW   int    `json:"store_width,omitempty"`
		StoreURL string `json:"store_url,omitempty"`
		FoundIn  string `json:"found_in,omitempty"`
		Distance string `json:"distance,omitempty"`
	}
	type upgradeOut struct {
		Upgraded int          `json:"upgraded"`
		Rows     []upgradeRow `json:"items"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "item_cover_upgrade",
		Description: "Replace a matched book's cover with its store's full-size one when that is bigger: the asin is looked up, the store's cover fetched at full size and compared, and set if it is at least factor times as wide and the same picture by perceptual hash (any_picture takes it regardless; square takes it whenever the current cover is not square). A book with no cover takes the store's. " +
			"A store copy wearing the 'Only from Audible' ribbon is never put over a cover that does not wear one. " +
			"Takes the rows audit_covers reports as upgrade or ratio, or any list of books. preview reports without changing anything. Changes server state.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in upgradeIn) (*mcp.CallToolResult, upgradeOut, error) {
		if len(in.Items) == 0 {
			return nil, upgradeOut{}, errors.New("at least one item is required")
		}
		factor := in.Factor
		if factor <= 1 {
			factor = defaultUpgradeFactor
		}
		tolerance := in.Tolerance
		if tolerance <= 0 {
			tolerance = defaultCoverTolerance
		}
		out := upgradeOut{Rows: make([]upgradeRow, 0, len(in.Items))}
		for _, ref := range in.Items {
			it, err := resolveItem(ctx, client, in.Library, ref)
			if err != nil {
				return nil, upgradeOut{}, err
			}
			row := upgradeRow{ID: it.ID, Title: it.Title()}
			if it.IsPodcast() || strings.TrimSpace(it.Media.Metadata.ASIN) == "" {
				row.Action = "no_asin"
				out.Rows = append(out.Rows, row)
				continue
			}
			lib, err := client.Library(ctx, it.LibraryID)
			if err != nil {
				return nil, upgradeOut{}, err
			}
			sc, err := storeCoverFor(ctx, client, it, providerOrder(it, providersFor(in.Providers, lib)))
			if err != nil {
				return nil, upgradeOut{}, err
			}
			if sc == nil {
				row.Action = "not_found"
				out.Rows = append(out.Rows, row)
				continue
			}
			row.StoreW, row.StoreURL, row.FoundIn = sc.Width, sc.URL, sc.FoundIn
			if it.HasCover() {
				w, h, err := client.CoverSize(ctx, it.ID)
				if err == nil {
					row.Width = w
				}
				have, err := libraryCoverImage(ctx, client, it.ID)
				if err != nil {
					return nil, upgradeOut{}, fmt.Errorf("cover of %s: %w", it.Title(), err)
				}
				hash := hashImage(have)
				_, ribboned := findAudibleBanner(have)
				notSquare := w > 0 && h > 0 && math.Abs(float64(w)/float64(h)-1) > tolerance
				p, d := hash.distance(sc.Hash)
				row.Distance = fmt.Sprintf("phash %d, dhash %d", p, d)
				same := hash.samePicture(sc.Hash)
				switch {
				case sc.Banner && !ribboned:
					row.Action = "kept_banner"
				case in.Square && notSquare:
					// a jacket scan: the store's square art wins whatever the picture or size
				case !same && in.AnyPicture:
					// another picture, asked for: size does not come into it
				case !same:
					row.Action = "kept_picture"
				case row.Width > 0 && float64(sc.Width) < float64(row.Width)*factor:
					row.Action = "kept_size" // the same picture, and not enough bigger to be worth the swap
				}
				if row.Action != "" {
					out.Rows = append(out.Rows, row)
					continue
				}
			}
			if in.Preview {
				row.Action = "would_upgrade"
			} else {
				if err := client.SetCoverFromURL(ctx, it.ID, sc.URL); err != nil {
					return nil, upgradeOut{}, fmt.Errorf("setting cover of %s: %w", it.Title(), err)
				}
				row.Action = "upgraded"
				out.Upgraded++
			}
			out.Rows = append(out.Rows, row)
		}

		return nil, out, nil
	})
}
