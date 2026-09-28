package tools

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mp4Box builds an MP4 box around its body.
func mp4Box(typ string, body ...[]byte) []byte {
	b := bytes.Join(body, nil)
	out := binary.BigEndian.AppendUint32(nil, uint32(8+len(b))) //nolint:gosec // test sizes are small
	return append(append(out, typ...), b...)
}

// m4b is a minimal MP4 file whose one track's sample description is entry
// ("mp4a" plays, "drms" is an iTunes FairPlay book), its audio cut short
// by cut bytes.
func m4b(entry string, cut int) []byte {
	desc := mp4Box(entry, make([]byte, 28))
	stsd := mp4Box("stsd", make([]byte, 4), binary.BigEndian.AppendUint32(nil, 1), desc)
	moov := mp4Box("moov", mp4Box("trak", mp4Box("mdia", mp4Box("minf", mp4Box("stbl", stsd)))))
	f := bytes.Join([][]byte{mp4Box("ftyp", []byte("M4B "), make([]byte, 4)), moov, mp4Box("mdat", make([]byte, 4000))}, nil)
	return f[:len(f)-cut]
}

// serveFiles answers the file route with the bytes given by item and ino,
// with ranges, the way the server's static handler does; an ino of "boom"
// fails.
func serveFiles(f *fakeABS, files map[string][]byte) {
	f.mux.HandleFunc("GET /api/items/{id}/file/{ino}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("id") + "/" + r.PathValue("ino")
		if r.PathValue("ino") == "boom" {
			http.Error(w, "disk gone", http.StatusInternalServerError)
			return
		}
		body, ok := files[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, key, time.Time{}, bytes.NewReader(body))
	})
}

// audioFile is an expanded record's audio file.
func audioFile(ino, name, codec, extra string) string {
	ext := name[strings.LastIndex(name, "."):]
	if extra != "" {
		extra = "," + extra
	}
	return `{"index":1,"ino":"` + ino + `","codec":"` + codec + `","duration":3600,"metadata":{"filename":"` + name + `","ext":"` + ext + `"}` + extra + `}`
}

func expandedBook(id, title, audio, other string) string {
	return `{"id":"` + id + `","libraryId":"` + libID + `","mediaType":"book","relPath":"A/` + title + `",` +
		`"media":{"metadata":{"title":"` + title + `","authorName":"An Author"},"audioFiles":[` + audio + `]},"libraryFiles":[` + other + `]}`
}

func libraryFile(name, kind string) string {
	return `{"ino":"x","fileType":"` + kind + `","metadata":{"filename":"` + name + `","ext":"` + name[strings.LastIndex(name, "."):] + `"}}`
}

// audit_unplayable over one book of each kind: the locked ones found and
// named worst first, the misnamed one found as playing, the ones that play
// left alone, a file the server fails to send left unknown rather than
// clean, and no file read that could not be locked.
func TestAuditUnplayable(t *testing.T) {
	t.Parallel()

	mp3 := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x10"), append(make([]byte, 16), 0xFF, 0xFB, 0x90, 0x00)...)
	f := newFakeABS(t)
	oneLibrary(f)
	listing := make([]string, 0, 10)
	for _, id := range []string{"i1", "i2", "i3", "i4", "i5", "i6", "i7", "i8", "i9", "i10"} {
		listing = append(listing, item(id, id, "", ""))
	}
	f.json("GET /api/libraries/"+libID+"/items", page(listing...))
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+strings.Join([]string{
		expandedBook("i1", "Children of the Mind", audioFile("11", "01 Children.m4b", "aac", "")+","+audioFile("12", "02 Children.m4b", "aac", ""), ""),
		expandedBook("i2", "Clean", audioFile("21", "Clean.m4b", "aac", "")+","+audioFile("22", "Clean 2.mp3", "mp3", ""), ""),
		expandedBook("i3", "Network Effect", audioFile("31", "Network Effect.m4b", "mp3", ""), ""),
		expandedBook("i4", "Only Original", "", libraryFile("Only Original.aax", "unknown")+","+libraryFile("cover.jpg", "image")),
		expandedBook("i5", "Horus Rising", audioFile("51", "Horus 01.m4b", "aac", ""), libraryFile("Horus Rising.aax", "unknown")),
		expandedBook("i6", "Unread", audioFile("61", "Unread.mp3", "mp3", `"error":"Invalid data found when processing input"`), ""),
		expandedBook("i7", "Broke Off", audioFile("71", "Broke Off.m4b", "aac", ""), ""),
		expandedBook("i8", "Server Fails", audioFile("boom", "Server Fails.m4b", "aac", ""), ""),
		expandedBook("i9", "Excluded", audioFile("91", "Extra.m4b", "aac", `"exclude":true`)+","+audioFile("92", "Main.mp3", "mp3", ""), ""),
		expandedBook("i10", "AAC Named MP3", audioFile("101", "Part 1.mp3", "aac", ""), ""),
	}, ",")+`]}`)
	serveFiles(f, map[string][]byte{
		"i1/11": m4b("drms", 0), "i1/12": m4b("drms", 0),
		"i2/21": m4b("mp4a", 0), "i2/22": mp3,
		"i3/31": mp3,
		"i5/51": m4b("mp4a", 0),
		"i7/71": m4b("mp4a", 3000),
		"i9/91": m4b("drms", 0), "i9/92": mp3,
		"i10/101": m4b("mp4a", 0),
	})

	out, err := toolCaller(t, f)("audit_unplayable", nil)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ id, problem string }
	found := list(t, out["findings"])
	got := make([]row, 0, len(found))
	for _, r := range found {
		got = append(got, row{fmt.Sprint(r["id"]), fmt.Sprint(r["problem"])})
		if plays := fmt.Sprint(r["plays"]); plays != strconv.FormatBool(r["problem"] == unplayExt) {
			t.Errorf("%v says plays=%v", r["id"], r["plays"])
		}
	}
	want := []row{{"i1", unplayLocked}, {"i4", unplayOriginal}, {"i6", unplayUnread}, {"i7", unplayCut}, {"i10", unplayExt}, {"i3", unplayExt}}
	if !slices.Equal(got, want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
	detail := map[string]string{}
	for _, r := range list(t, out["findings"]) {
		detail[fmt.Sprint(r["id"])] = fmt.Sprint(r["detail"])
	}
	for id, words := range map[string][]string{
		"i1":  {"all 2 files are locked", "Apple FairPlay (drms)", "stays locked"},
		"i4":  {"only an Audible original (.aax)", "Libation"},
		"i6":  {"Invalid data found"},
		"i7":  {"cut short", "Broke Off.m4b has"},
		"i3":  {"named .m4b but holds mp3"},
		"i10": {"named .mp3 but holds MPEG-4 audio"},
	} {
		for _, w := range words {
			if !strings.Contains(detail[id], w) {
				t.Errorf("%s detail = %q, want %q in it", id, detail[id], w)
			}
		}
	}
	if n := num(t, out["unchecked_count"]); n != 1 {
		t.Errorf("unchecked_count = %d, want the file the server failed to send", n)
	}
	if u := list(t, out["unchecked"]); len(u) != 1 || u[0]["id"] != "i8" || !strings.Contains(fmt.Sprint(u[0]["why"]), "500") {
		t.Errorf("unchecked = %v, want i8 with the server's 500", u)
	}
	// every m4b but the excluded one and the one the server failed to
	// send, and the aac named .mp3; never an mp3 holding mp3
	if n := num(t, out["files_read"]); n != 7 {
		t.Errorf("files_read = %d, want 7", n)
	}
	for _, p := range []string{"/api/items/i2/file/22", "/api/items/i9/file/92", "/api/items/i9/file/91", "/api/items/i6/file/61"} {
		if len(f.requests(p)) > 0 {
			t.Errorf("%s was read", p)
		}
	}
	if c, ok := out["counts"].(map[string]any); !ok || num(t, c[unplayExt]) != 2 || num(t, c[unplayLocked]) != 1 {
		t.Errorf("counts = %v", out["counts"])
	}

	// the limit keeps the worst
	one, err := toolCaller(t, f)("audit_unplayable", map[string]any{"limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if r := list(t, one["findings"]); len(r) != 1 || r[0]["id"] != "i1" || num(t, one["total_findings"]) != 6 {
		t.Errorf("limit 1 = %v of %v, want i1 of 6", r, one["total_findings"])
	}
}

// The server failing as a whole - its proxy down, the key refused - stops
// the audit and says so, where a file gone from disk is only that file
// unchecked; and a file whose first bytes are no audio format known is
// unchecked, not clean.
func TestAuditUnplayableServerOrFile(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		status int
		fails  bool
	}{{http.StatusBadGateway, true}, {http.StatusUnauthorized, true}, {http.StatusServiceUnavailable, true}, {http.StatusNotFound, false}, {http.StatusInternalServerError, false}} {
		f := newFakeABS(t)
		oneLibrary(f)
		f.json("GET /api/libraries/"+libID+"/items", page(item("i1", "A", "", "")))
		f.json("POST /api/items/batch/get", `{"libraryItems":[`+expandedBook("i1", "A", audioFile("11", "A.m4b", "aac", ""), "")+`]}`)
		f.mux.HandleFunc("GET /api/items/{id}/file/{ino}", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no", c.status)
		})
		out, err := toolCaller(t, f)("audit_unplayable", nil)
		switch {
		case c.fails && (err == nil || !strings.Contains(err.Error(), strconv.Itoa(c.status))):
			t.Errorf("%d = %v, %v; want the audit to fail saying so", c.status, out, err)
		case !c.fails && (err != nil || num(t, out["unchecked_count"]) != 1):
			t.Errorf("%d = %v, %v; want the one file unchecked", c.status, out, err)
		}
	}

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(item("i1", "A", "", "")))
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+expandedBook("i1", "A", audioFile("11", "A.m4b", "aac", ""), "")+`]}`)
	serveFiles(f, map[string][]byte{"i1/11": make([]byte, 100<<10)})
	out, err := toolCaller(t, f)("audit_unplayable", nil)
	if err != nil {
		t.Fatal(err)
	}
	if u := list(t, out["unchecked"]); len(u) != 1 || !strings.Contains(fmt.Sprint(u[0]["why"]), "no audio format known") || num(t, out["files_read"]) != 1 {
		t.Errorf("a file of zeros = %v, want it read and unchecked", out)
	}
}

// A listing or batch the server fails is the audit failing, never a
// library with nothing unplayable in it.
func TestAuditUnplayableServerFails(t *testing.T) {
	t.Parallel()

	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(item("i1", "A", "", "")))
	f.fails("POST /api/items/batch/get")
	if out, err := toolCaller(t, f)("audit_unplayable", nil); err == nil {
		t.Errorf("= %v, want the failed batch as an error", out)
	}
}

// With decode, a file that plays is played at its start, middle and end,
// and one with noise through its middle is found damaged; a locked one is
// not played at all.
func TestAuditUnplayableDecode(t *testing.T) {
	t.Parallel()
	needFFmpeg(t)

	good := ffmpegSine(t, "good.m4b", 60)
	damaged := bytes.Clone(good)
	for i := len(good) / 3; i < len(good)*2/3; i++ {
		damaged[i] = byte(i * 7 % 256)
	}
	f := newFakeABS(t)
	oneLibrary(f)
	f.json("GET /api/libraries/"+libID+"/items", page(item("i1", "Good", "", ""), item("i2", "Damaged", "", ""), item("i3", "Locked", "", "")))
	dur := `"duration":60`
	f.json("POST /api/items/batch/get", `{"libraryItems":[`+strings.Join([]string{
		expandedBook("i1", "Good", strings.Replace(audioFile("11", "Good.m4b", "aac", ""), `"duration":3600`, dur, 1), ""),
		expandedBook("i2", "Damaged", strings.Replace(audioFile("21", "Damaged.m4b", "aac", ""), `"duration":3600`, dur, 1), ""),
		expandedBook("i3", "Locked", audioFile("31", "Locked.m4b", "aac", ""), ""),
	}, ",")+`]}`)
	serveFiles(f, map[string][]byte{"i1/11": good, "i2/21": damaged, "i3/31": m4b("drms", 0)})

	out, err := toolCaller(t, f)("audit_unplayable", map[string]any{"decode": true})
	if err != nil {
		t.Fatal(err)
	}
	found := list(t, out["findings"])
	got := make([]string, 0, len(found))
	for _, r := range found {
		got = append(got, fmt.Sprint(r["id"])+" "+fmt.Sprint(r["problem"]))
	}
	if !slices.Equal(got, []string{"i3 locked", "i2 damaged"}) {
		t.Errorf("findings = %v (%v), want i3 locked, i2 damaged", got, out["findings"])
	}
	if n := num(t, out["files_played"]); n != 2 {
		t.Errorf("files_played = %d, want the two that are not locked", n)
	}
}

// ffmpegSine writes seconds of a tone as an m4b.
func ffmpegSine(t *testing.T, name string, seconds int) []byte {
	t.Helper()
	p := t.TempDir() + "/" + name
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=f=440:r=8000", "-t", strconv.Itoa(seconds), "-c:a", "aac", p) //nolint:gosec // this test's own file
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}
	b, err := os.ReadFile(p) //nolint:gosec // this test's own file
	if err != nil {
		t.Fatal(err)
	}
	return b
}
