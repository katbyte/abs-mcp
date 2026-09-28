//go:build integration

package acceptance

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A shelf of books that will not play, laid out on disk the way they turn
// up in a library: an iTunes book whose audio is locked, a download that
// broke off, an mp3 named .m4b, a folder holding only the locked Audible
// original beside an ebook, a book with noise through the middle of its
// file, and a book that plays. audit_unplayable has to find each for what it
// is, leave the good one alone, and, with decode, hear the damaged one.
//
// The locked book is a playable m4b with its sample description renamed
// from mp4a to drms, which is all the lock looks like to anything but
// Apple's player: what ffprobe reports of Children of the Mind.
func TestJourneyBooksThatWillNotPlay(t *testing.T) {
	const author = "Zzyzx Unplayable Author"
	locked, cut, misnamed, original, damaged, good := author+"/Zzyzx Locked Book", author+"/Zzyzx Cut Book", author+"/Zzyzx Misnamed Book", author+"/Zzyzx Original Only", author+"/Zzyzx Damaged Book", author+"/Zzyzx Good Book"

	s := newDiskShelf(t, "Zzyzx Unplayable Shelf", "zzyzx-unplayable")
	m4b := func(rel string, seconds string, extra ...string) []byte {
		t.Helper()
		p := filepath.Join(s.root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
			t.Fatal(err)
		}
		args := append([]string{"-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=f=440:r=8000", "-t", seconds, "-c:a", "aac", "-b:a", "16k"}, extra...)
		if out, err := exec.CommandContext(t.Context(), "ffmpeg", append(args, p)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %s: %v\n%s", rel, err, out)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	rewrite := func(rel string, b []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(s.root, rel), b, 0o666); err != nil {
			t.Fatal(err)
		}
	}

	// locked: the first mp4a after the stsd box becomes drms
	b := m4b(locked+"/01 Zzyzx Locked Book.m4b", "30")
	i := bytes.Index(b, []byte("stsd"))
	j := bytes.Index(b[i:], []byte("mp4a"))
	if i < 0 || j < 0 {
		t.Fatal("ffmpeg's m4b has no mp4a description to lock")
	}
	copy(b[i+j:], "drms")
	rewrite(locked+"/01 Zzyzx Locked Book.m4b", b)

	// cut: the header first, so the server still reads the whole length,
	// and the last third of the audio gone
	b = m4b(cut+"/Zzyzx Cut Book.m4b", "60", "-movflags", "+faststart")
	rewrite(cut+"/Zzyzx Cut Book.m4b", b[:len(b)*2/3])

	// misnamed: an mp3 through and through, named .m4b
	diskSilence(t, filepath.Join(s.root, misnamed, "Zzyzx Misnamed Book.mp3"), 5)
	if err := os.Rename(filepath.Join(s.root, misnamed, "Zzyzx Misnamed Book.mp3"), filepath.Join(s.root, misnamed, "Zzyzx Misnamed Book.m4b")); err != nil {
		t.Fatal(err)
	}

	// original only: the locked download beside an ebook, which is what
	// makes the folder a book the server lists
	s.write(t, original+"/Zzyzx Original Only.epub")
	rewrite(original+"/Zzyzx Original Only.aax", bytes.Repeat([]byte("locked"), 2000))

	// damaged: noise over the middle of the audio
	b = m4b(damaged+"/Zzyzx Damaged Book.m4b", "60", "-movflags", "+faststart")
	for k := len(b) / 3; k < len(b)*2/3; k++ {
		b[k] = byte(k * 7)
	}
	rewrite(damaged+"/Zzyzx Damaged Book.m4b", b)

	m4b(good+"/Zzyzx Good Book.m4b", "30")
	s.open(t, 6)

	byPath := func(out map[string]any) map[string]map[string]any {
		got := map[string]map[string]any{}
		for _, r := range rows(t, out["findings"], "findings") {
			got[text(r["path"])+" "+text(r["problem"])] = r
		}
		return got
	}

	out := call(t, "audit_unplayable", map[string]any{"library": s.name})
	got := byPath(out)
	for key, words := range map[string]string{
		locked + " locked":            "Apple FairPlay (drms)",
		cut + " cut_short":            "cut short",
		misnamed + " wrong_extension": "named .m4b but holds mp3",
		original + " original_only":   "an Audible original (.aax)",
	} {
		r, ok := got[key]
		if !ok {
			t.Errorf("no finding %q in %v", key, out["findings"])
			continue
		}
		if !strings.Contains(text(r["detail"]), words) {
			t.Errorf("%s says %q, want %q in it", key, r["detail"], words)
		}
		if plays := truth(r["plays"]); plays != strings.HasSuffix(key, "wrong_extension") {
			t.Errorf("%s says plays=%v", key, r["plays"])
		}
	}
	if len(got) != 4 {
		t.Errorf("findings = %v, want the four above and nothing for the good or the damaged book without decode", out["findings"])
	}
	if n := num(t, out["files_read"], "files_read"); n != 5 {
		t.Errorf("files_read = %d, want the five m4b files", n)
	}
	if out["unchecked_count"] != nil {
		t.Errorf("unchecked = %v, want every file read", out["unchecked"])
	}

	// with decode the damaged book is heard; the good one, and the locked
	// and cut ones already found, are not reported for it
	out = call(t, "audit_unplayable", map[string]any{"library": s.name, "decode": true})
	got = byPath(out)
	if _, ok := got[damaged+" damaged"]; !ok {
		t.Errorf("decode did not find the damaged book: %v", out["findings"])
	}
	for key := range got {
		if strings.HasPrefix(key, good+" ") || (strings.HasSuffix(key, " damaged") && !strings.HasPrefix(key, damaged+" ")) {
			t.Errorf("decode reported %s", key)
		}
	}
	if n := num(t, out["files_played"], "files_played"); n != 3 {
		t.Errorf("files_played = %d, want the good, the misnamed and the damaged book's files", n)
	}
}
