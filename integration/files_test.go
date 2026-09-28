//go:build integration

package integration

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// A ranged read is what lets the audits look at a few kilobytes of a file
// rather than all of it: the server has to honour the Range the client passes
// through, say which bytes it sent, and send those bytes and no others.
func TestItemFileRange(t *testing.T) {
	ctx := skipUnlessLive(t)
	id := library(t)

	item := must(client.Items(ctx, id, abs.ItemsOptions{Limit: 1})).Results[0]
	full := must(client.Item(ctx, item.ID))
	if len(full.LibraryFiles) == 0 {
		t.Skip("no files to read")
	}
	file := full.LibraryFiles[0]

	// the size is the file's own, read whole: the record's is as of the last
	// scan, and an embed since then has rewritten the file
	whole := must(client.ItemFile(ctx, item.ID, file.Ino))
	want, err := io.ReadAll(whole)
	_ = whole.Close()
	if err != nil {
		t.Fatalf("reading the whole file: %v", err)
	}
	size := int64(len(want))
	if size < 200 {
		t.Skipf("%s is %d bytes, too short to read a part of", file.Metadata.Filename, size)
	}

	read := func(rng string) (status int, contentRange string, body []byte) {
		t.Helper()

		resp, rerr := client.ItemFileRange(ctx, item.ID, file.Ino, rng)
		if rerr != nil {
			t.Fatalf("ItemFileRange %q: %v", rng, rerr)
		}
		defer func() { _ = resp.Body.Close() }()
		if body, rerr = io.ReadAll(resp.Body); rerr != nil {
			t.Fatalf("ItemFileRange %q: reading: %v", rng, rerr)
		}

		return resp.StatusCode, resp.Header.Get("Content-Range"), body
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

	// no range is the whole file
	if status, _, got := read(""); status != http.StatusOK || !bytes.Equal(got, want) {
		t.Errorf("no range = HTTP %d and %d bytes, want 200 and the whole file, %d", status, len(got), len(want))
	}

	// a file the item does not have is a 404 like any other
	resp, err := client.ItemFileRange(ctx, item.ID, "0", "bytes=0-9")
	if err == nil {
		_ = resp.Body.Close()
	}
	if !abs.IsNotFound(err) {
		t.Errorf("a file the item does not have = %v, want a 404", err)
	}
}
