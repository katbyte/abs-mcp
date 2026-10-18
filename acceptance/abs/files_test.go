//go:build acceptance

package abs_test

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"

	"github.com/katbyte/abs-mcp/sdk/abs"
)

// A ranged read is what lets the audits look at a few kilobytes of a file
// rather than all of it: the server has to honour the Range the client passes
// through, say which bytes it sent, and send those bytes and no others.
func TestItemFileRange(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	full := whole(ctx, firstItems(ctx, id, 1)[0].Id)
	if len(full.LibraryFiles) == 0 {
		t.Skip("no files to read")
	}
	file := full.LibraryFiles[0]

	read := func(rng string) (status int, contentRange string, body []byte) {
		t.Helper()

		got, err := client.LibraryItemGetLibraryFile(ctx, full.Id, file.Ino, abs.LibraryItemGetLibraryFileOptions{Range: rng})
		if err != nil {
			t.Fatalf("reading the file with range %q: %v", rng, err)
		}
		status, contentRange = got.HttpResponse.StatusCode, got.HttpResponse.Header.Get("Content-Range")
		if body, err = got.Bytes(1 << 30); err != nil {
			t.Fatalf("reading the file with range %q: %v", rng, err)
		}

		return status, contentRange, body
	}

	// the size is the file's own, read whole: the record's is as of the last
	// scan, and an embed since then has rewritten the file
	status, _, want := read("")
	if status != http.StatusOK {
		t.Errorf("no range = HTTP %d, want 200 and the whole file", status)
	}
	size := int64(len(want))
	if size < 200 {
		t.Skipf("%s is %d bytes, too short to read a part of", file.Ino, size)
	}

	status, sent, got := read("bytes=100-199")
	if status != http.StatusPartialContent || sent != fmt.Sprintf("bytes 100-199/%d", size) {
		t.Errorf("a part = HTTP %d, Content-Range %q; want 206 and bytes 100-199 of %d", status, sent, size)
	}
	if !bytes.Equal(got, want[100:200]) {
		t.Errorf("a part is %d bytes that are not bytes 100-199 of the file", len(got))
	}

	// the end of the file, asked for from a place to the end
	from := size - 50
	if status, _, got := read(fmt.Sprintf("bytes=%d-", from)); status != http.StatusPartialContent || !bytes.Equal(got, want[from:]) {
		t.Errorf("the last 50 bytes = HTTP %d and %d bytes, want 206 and the file's last 50", status, len(got))
	}

	// a file the item does not have is a 404 like any other
	none, err := client.LibraryItemGetLibraryFile(ctx, full.Id, "0", abs.LibraryItemGetLibraryFileOptions{Range: "bytes=0-9"})
	if err == nil {
		body, _ := none.Reader()
		_ = body.Close()
	}
	if !abs.IsNotFound(err) {
		t.Errorf("a file the item does not have = %v, want a 404", err)
	}
}
