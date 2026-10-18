package abs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/katbyte/pandorest/client"
)

// roundTrip is a function as a transport.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A client acting as another account sends that account's key through the first client's own transport, and never repeats the key in an
// error, even one whose body the server wrote it into.
func TestAs(t *testing.T) {
	t.Parallel()

	const other = "the-other-accounts-key"
	var sent atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "no account for "+other)
	}))
	t.Cleanup(srv.Close)

	var carried atomic.Int32
	through := client.WithTransport(roundTrip(func(r *http.Request) (*http.Response, error) {
		carried.Add(1)
		return http.DefaultTransport.RoundTrip(r)
	}))
	c, err := New(srv.URL, "the-servers-key", through)
	if err != nil {
		t.Fatal(err)
	}
	as, err := c.As(other)
	if err != nil {
		t.Fatal(err)
	}

	_, err = as.MeGetCurrentUser(t.Context())
	if got := sent.Load(); got != "Bearer "+other {
		t.Errorf("sent %v, want the other account's key", got)
	}
	if carried.Load() != 1 {
		t.Errorf("%d calls through the first client's transport, want the one", carried.Load())
	}
	if err == nil || strings.Contains(err.Error(), other) || !strings.Contains(err.Error(), "***") {
		t.Errorf("the error = %v, want the key blanked", err)
	}

	if _, err := c.As(""); err == nil {
		t.Error("acting as no key at all was allowed")
	}
}
