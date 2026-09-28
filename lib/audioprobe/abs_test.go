package audioprobe

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/abs-mcp/lib/abs"
)

func serve(t *testing.T, h http.HandlerFunc) *abs.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := abs.New(srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A locked book read through the server's file route, header last: the
// ranges the walk asks for are what comes back, and the lock is found.
func TestItemFileRanges(t *testing.T) {
	t.Parallel()

	data := bytes.Join([][]byte{ftyp, bx("mdat", make([]byte, 2<<20)), moov(audioEntry("drms", 0, sinf("itun")))}, nil)
	var ranges []string
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/items/li_1/file/7" || r.Header.Get("Authorization") != "Bearer k" {
			http.NotFound(w, r)
			return
		}
		ranges = append(ranges, r.Header.Get("Range"))
		http.ServeContent(w, r, "a.m4b", time.Time{}, bytes.NewReader(data))
	})
	rep, err := Probe(t.Context(), ItemFile(c, "li_1", "7"))
	if err != nil || rep.Locked != fairPlay || rep.Size != int64(len(data)) {
		t.Fatalf("= %+v, %v; want the drms, %d bytes", rep, err, len(data))
	}
	if len(ranges) != 2 || ranges[0] != fmt.Sprintf("bytes=0-%d", block-1) {
		t.Errorf("ranges asked = %q, want the first block then the end", ranges)
	}
}

// A server that ignores the range and sends the whole file can still be
// read from the start, and no further than asked; a read past the start
// from it is an error, not a file read wrong.
func TestItemFileWholeReply(t *testing.T) {
	t.Parallel()

	small := bytes.Join([][]byte{ftyp, moov(audioEntry("mp4a", 0)), bx("mdat", make([]byte, 100))}, nil)
	big := bytes.Join([][]byte{ftyp, bx("mdat", make([]byte, 2<<20)), moov(audioEntry("mp4a", 0))}, nil)
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"a small file", small, ""},
		{"a header past the first read", big, "whole file for a byte range"},
	} {
		client := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(c.data)))
			_, _ = w.Write(c.data)
		})
		rep, err := Probe(t.Context(), ItemFile(client, "li_1", "7"))
		switch {
		case c.want == "" && (err != nil || rep.Container != MP4):
			t.Errorf("%s = %+v, %v; want it read", c.name, rep, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s = %+v, %v; want %q", c.name, rep, err, c.want)
		}
	}
}

// A reply that breaks off is the server failing, not the end of the file.
func TestItemFileBrokeOff(t *testing.T) {
	t.Parallel()

	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-65535/900000")
		w.Header().Set("Content-Length", "65536")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(ftyp)
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	})
	if rep, err := Probe(t.Context(), ItemFile(c, "li_1", "7")); err == nil {
		t.Errorf("= %+v, want the break reported", rep)
	}
}

// An empty file is empty, not an error; a file the server refuses is.
func TestItemFileEmptyAndRefused(t *testing.T) {
	t.Parallel()

	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/403") {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		http.ServeContent(w, r, "a.m4b", time.Time{}, bytes.NewReader(nil))
	})
	if rep, err := Probe(t.Context(), ItemFile(c, "li_1", "7")); err != nil || rep.Container != "" || rep.Size != 0 {
		t.Errorf("empty = %+v, %v; want nothing known, 0 bytes", rep, err)
	}
	if _, err := Probe(t.Context(), ItemFile(c, "li_1", "403")); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("refused = %v, want the 403", err)
	}
}

func TestRangeOf(t *testing.T) {
	t.Parallel()

	for cr, want := range map[string][2]int64{"bytes 0-9/1234": {0, 1234}, "bytes 8192-16383/90000": {8192, 90000}} {
		if start, n, err := rangeOf(cr); err != nil || start != want[0] || n != want[1] {
			t.Errorf("%q = %d, %d, %v; want %v", cr, start, n, err, want)
		}
	}
	for _, cr := range []string{"", "bytes 0-9/*", "bytes 0-9/x", "bytes 0-9/-1", "bytes */55", "bytes x-9/10"} {
		if _, _, err := rangeOf(cr); err == nil {
			t.Errorf("%q read, want an error", cr)
		}
	}
}

// A reply for another stretch than the one asked for is an error, never
// read as the bytes asked for.
func TestItemFileWrongRange(t *testing.T) {
	t.Parallel()

	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 100-8291/900000")
		w.Header().Set("Content-Length", "8192")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(make([]byte, 8192))
	})
	if _, err := Probe(t.Context(), ItemFile(c, "li_1", "7")); err == nil || !strings.Contains(err.Error(), "from 100 for a range from 0") {
		t.Errorf("= %v, want the wrong range said", err)
	}
}
