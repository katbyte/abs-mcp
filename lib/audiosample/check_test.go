package audiosample

import (
	"strings"
	"testing"
)

// A check says how a stretch of one file decoded: whole and quiet for a
// good file; complaints for one with a stretch of noise in its middle;
// ffmpeg giving up on one that is not audio at all; and the server
// refusing, or breaking off, as an error rather than as the file's fault.
func TestCheck(t *testing.T) {
	t.Parallel()
	needFFmpeg(t)

	good := generate(t, "good.m4b", "sine=frequency=440:sample_rate=8000", 20, "-c:a", "aac")
	damaged := append([]byte(nil), good...)
	for i := len(good) / 3; i < len(good)/3+len(good)/4; i++ {
		damaged[i] = byte(i * 7 % 251) // the same noise every run
	}
	f := newFakeFiles(t, map[string][]byte{
		"good":     good,
		"damaged":  damaged,
		"text":     []byte(strings.Repeat("not audio at all\n", 500)),
		brokenFile: good,
	})
	s := f.sampler(t)

	h, err := s.Check(t.Context(), testItem, "good", 8, 5)
	if err != nil || h.Complaints != 0 || h.Failed != "" || h.Seconds < 4.9 || h.Seconds > 5.1 {
		t.Errorf("a good file = %+v, %v; want 5 s and nothing said", h, err)
	}

	h, err = s.Check(t.Context(), testItem, "damaged", 8, 5)
	if err != nil || (h.Complaints == 0 && h.Seconds > 4) || (h.Complaints > 0 && len(h.First) == 0) {
		t.Errorf("a damaged file = %+v, %v; want complaints or audio missing, and no error", h, err)
	}
	for _, line := range h.First {
		if strings.Contains(line, "127.0.0.1") {
			t.Errorf("a complaint shows the proxy's address: %q", line)
		}
	}

	h, err = s.Check(t.Context(), testItem, "text", 0, 5)
	if err != nil || h.Failed == "" || h.Seconds != 0 {
		t.Errorf("a file that is not audio = %+v, %v; want ffmpeg's failure in the health, no error", h, err)
	}

	if _, err := s.Check(t.Context(), testItem, "locked", 0, 5); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("a file the server refuses = %v, want the refusal as an error", err)
	}
	if _, err := s.Check(t.Context(), testItem, brokenFile, 12, 5); err == nil {
		t.Error("a file the server breaks off = no error, want the break as one")
	}
	if _, err := s.Check(t.Context(), testItem, "good", 0, 0); err == nil {
		t.Error("a check of no length = no error")
	}
}
