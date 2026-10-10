package tools

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func num(t *testing.T, v any) int {
	t.Helper()

	n, ok := v.(float64)
	if !ok {
		t.Fatalf("%v is %T, want a number", v, v)
	}

	return int(n)
}

// str reads a string field, treating an absent one as empty.
func str(t *testing.T, v any) string {
	t.Helper()

	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%v is %T, want a string", v, v)
	}

	return s
}

func boolOf(t *testing.T, v any) bool {
	t.Helper()

	b, ok := v.(bool)
	if !ok {
		t.Fatalf("%v is %T, want a bool", v, v)
	}

	return b
}

// isTrue reports whether an answer's field is the boolean true.
func isTrue(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func list(t *testing.T, v any) []map[string]any {
	t.Helper()

	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%v is %T, want a list", v, v)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		row, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("%v is %T, want an object", e, e)
		}
		out = append(out, row)
	}

	return out
}

// object reads a nested object of an answer.
func object(t *testing.T, v any) map[string]any {
	t.Helper()

	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%v is %T, want an object", v, v)
	}
	return m
}

// column is one field of every row of a list, in order.
func column(t *testing.T, field string, v any) []string {
	t.Helper()

	rows := list(t, v)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, str(t, row[field]))
	}
	return out
}

// strs reads a list of strings, treating an absent one as empty.
func strs(t *testing.T, v any) []string {
	t.Helper()

	if v == nil {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%v is %T, want a list", v, v)
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		out = append(out, str(t, e))
	}
	return out
}

// dig is the value at a dotted path in an answer, nil when there is none.
func dig(out map[string]any, path string) any {
	var v any = out
	for step := range strings.SplitSeq(path, ".") {
		if i, err := strconv.Atoi(step); err == nil {
			l, ok := v.([]any)
			if !ok || i >= len(l) {
				return nil
			}
			v = l[i]
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[step]
	}
	return v
}

// wantNumbers checks the number at each dotted path in an answer:
// "chapter_list.1.start_s" is the second chapter's start.
//
// Every answer gives a length, position or total as a number of seconds in a
// field ending _s, and a size as a number of bytes: "11h 5m" and size_mb
// could not be compared or added up, and a size in whole gigabytes said a
// 50 MB podcast library held nothing. Chapter and bookmark times keep the
// server's fractions, since they are passed back; the rest are whole seconds.
func wantNumbers(t *testing.T, tool string, out map[string]any, want map[string]float64) {
	t.Helper()

	for path, n := range want {
		if got, ok := dig(out, path).(float64); !ok || got != n {
			t.Errorf("%s %s = %v, want %v", tool, path, dig(out, path), n)
		}
	}
}

// wantAbsent checks an answer carries nothing at each dotted path.
func wantAbsent(t *testing.T, tool string, out map[string]any, paths ...string) {
	t.Helper()

	for _, path := range paths {
		if v := dig(out, path); v != nil {
			t.Errorf("%s %s = %v, want it absent", tool, path, v)
		}
	}
}

// wantErr fails the test unless err is set and says each of the words. The
// server failing a request a tool relies on is an error the caller sees,
// never an answer built as if the request had come back empty: a title left
// blank, a book counted as missing from a store, progress read as none.
func wantErr(t *testing.T, what string, err error, words ...string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: no error, want one saying %q", what, words)
		return
	}
	for _, w := range words {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("%s: %v, want it to say %q", what, err, w)
		}
	}
}

func parseQuery(t *testing.T, raw string) url.Values {
	t.Helper()

	q, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func sameRefs(a, b []abs.SeriesRef) bool {
	return slices.EqualFunc(a, b, func(x, y abs.SeriesRef) bool {
		return x.ID == y.ID && x.Name == y.Name && x.Sequence == y.Sequence
	})
}

func numbered(id, path, series, seq string) *abs.Item {
	it := &abs.Item{ID: id, MediaType: "book", RelPath: path}
	it.Media.Metadata.Title = path
	it.Media.Metadata.Series = abs.SeriesRefs{{Name: series, Sequence: seq}}
	return it
}

func newTestClient(t *testing.T) *abs.Client {
	t.Helper()

	c, err := abs.New("http://127.0.0.1:1", "test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func register(t *testing.T, opts Options) []string {
	t.Helper()

	names, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// artwork draws a cover of n pixels: a diagonal wash with a dark disc and a
// light bar, placed by the seed so that different seeds are different covers.
func artwork(n int, seed float64) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			u, v := float64(x)/float64(n), float64(y)/float64(n)
			shade := 60 + 120*(u*0.6+v*0.4)
			dx, dy := u-(0.3+0.4*seed), v-(0.35+0.3*seed)
			if dx*dx+dy*dy < 0.04 {
				shade = 20
			}
			if v > 0.7+0.15*seed && v < 0.8+0.15*seed && u > 0.1 && u < 0.6+0.3*seed {
				shade = 230
			}
			c := uint8(min(255, max(0, int(shade))))
			g := uint8(min(255, int(shade)+20*int(seed*3))) //nolint:gosec // clamped
			img.Set(x, y, color.RGBA{c, g, c, 255})
		}
	}
	return img
}

// ribboned is a cover with the Audible ribbon drawn across its bottom-right
// corner: a yellow band at 45 degrees with dark lettering on it.
func ribboned(n int, seed float64) image.Image {
	base := artwork(n, seed)
	img := image.NewRGBA(base.Bounds())
	for y := range n {
		for x := range n {
			c := base.At(x, y)
			s := float64(x+y) / float64(n)
			if s > 1.42 && s < 1.62 {
				c = color.RGBA{250, 230, 40, 255}
				if (x-y)%11 < 3 && s > 1.47 && s < 1.57 { // the lettering
					c = color.RGBA{30, 30, 30, 255}
				}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func encodeJPEG(t *testing.T, img image.Image, quality int) string {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// pngOf is a blank PNG of a size, for a test that reads only the size.
func pngOf(t *testing.T, w, h int) string {
	t.Helper()

	return encodePNG(t, image.NewRGBA(image.Rect(0, 0, w, h)))
}

// encodePNG is a picture as the bytes of a PNG file.
func encodePNG(t *testing.T, img image.Image) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// needFFmpeg skips a test that plays audio where ffmpeg is not installed, and
// fails it in CI, where it is: a test CI skipped would look like one it ran.
func needFFmpeg(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("ffmpeg is not installed, and CI has to run this test: install it in the job")
		}
		t.Skip("ffmpeg is not installed")
	}
}
