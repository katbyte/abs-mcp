package audioprobe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/katbyte/abs-mcp/lib/abs"
)

// ItemFile is a Fetch over one file of an item, through the server's file
// route with byte ranges.
func ItemFile(client *abs.Client, itemID, fileID string) Fetch {
	return func(ctx context.Context, off, n int64) ([]byte, int64, error) {
		resp, err := client.ItemFileRange(ctx, itemID, fileID, fmt.Sprintf("bytes=%d-%d", off, off+n-1))
		if err != nil {
			// a range past the end of an empty file: nothing to read
			if he, ok := errors.AsType[*abs.HTTPError](err); ok && he.Status == http.StatusRequestedRangeNotSatisfiable && off == 0 {
				return nil, 0, nil
			}
			return nil, 0, err
		}
		defer func() { _ = resp.Body.Close() }()

		var size int64
		switch resp.StatusCode {
		case http.StatusPartialContent:
			var start int64
			start, size, err = rangeOf(resp.Header.Get("Content-Range"))
			if err != nil {
				return nil, 0, err
			}
			// bytes from anywhere else would be read as the ones asked for
			if start != off {
				return nil, 0, fmt.Errorf("the server sent bytes from %d for a range from %d", start, off)
			}
		case http.StatusOK:
			// the whole file, from its start: usable only for a read from
			// the start, and read no further than asked
			if off != 0 {
				return nil, 0, errors.New("the server sent the whole file for a byte range, so its end cannot be read without reading all of it")
			}
			size = resp.ContentLength
		default:
			return nil, 0, fmt.Errorf("the server answered a byte range with %s", resp.Status)
		}

		buf := make([]byte, n)
		k, err := io.ReadFull(resp.Body, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return nil, 0, fmt.Errorf("reading bytes %d-%d of the file: %w", off, off+n-1, err)
		}
		if size < 0 {
			if int64(k) == n {
				return nil, 0, errors.New("the server sent the whole file for a byte range without saying how long it is")
			}
			size = int64(k) // a whole file of unknown length that fit in one read
		}
		// fewer bytes than asked before the end is the reply breaking off,
		// which a short read would otherwise pass off as the end of the file
		if want := min(n, max(size-off, 0)); int64(k) < want {
			return nil, 0, fmt.Errorf("the server's reply broke off after %d of %d bytes at %d", k, want, off)
		}
		return buf[:k], size, nil
	}
}

// rangeOf reads where a Content-Range starts and how long the file is,
// "bytes 0-9/1234".
func rangeOf(cr string) (start, total int64, err error) {
	bad := fmt.Errorf("the server's Content-Range %q does not say which bytes of how long a file it sent", cr)
	span, whole, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(cr), "bytes "), "/")
	first, _, ok2 := strings.Cut(span, "-")
	if !ok || !ok2 {
		return 0, 0, bad
	}
	start, err = strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, bad
	}
	total, err = strconv.ParseInt(strings.TrimSpace(whole), 10, 64)
	if err != nil || total < 0 {
		return 0, 0, bad
	}
	return start, total, nil
}
