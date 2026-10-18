package abs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/pandorest/client"
)

// What Audiobookshelf says went wrong is read out of its plain-text refusals and its few JSON ones, and anything else is left for the client to
// show the start of.
func TestErrorMessages(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name, body string
		want       []string
	}{
		{"a line of plain text", " Invalid request \n", []string{"Invalid request"}},
		{"JSON with an error", `{"error":"Library not found"}`, []string{"Library not found"}},
		{"JSON with a message", `{"message":"Not allowed"}`, []string{"Not allowed"}},
		{"JSON with neither", `{"code":3}`, nil},
		{"JSON that does not read", `{"error":`, nil},
		{"a web page", "<!doctype html><title>Login</title>", nil},
		{"text too long to be a message", strings.Repeat("a", maxMessage+1), nil},
		{"nothing", "  ", nil},
	} {
		if got := errorMessages([]byte(tt.body)); !slices.Equal(got, tt.want) {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}

// A refusal carries Audiobookshelf's own words, and a 401 for a key that was sent says where keys are made.
func TestRefusals(t *testing.T) {
	t.Parallel()

	status := http.StatusBadRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "Invalid library id")
	}))
	t.Cleanup(srv.Close)

	c, err := New(srv.URL, "the-key")
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.LibraryFindOne(t.Context(), "l1")
	if got := client.Messages(err); !slices.Equal(got, []string{"Invalid library id"}) || client.StatusCode(err) != http.StatusBadRequest {
		t.Errorf("a 400 = %v, messages %q", err, got)
	}

	status = http.StatusUnauthorized
	if _, err = c.LibraryFindOne(t.Context(), "l1"); err == nil || !strings.Contains(err.Error(), "Settings → Users → API Keys") {
		t.Errorf("a 401 = %v, want where keys are made", err)
	}
}

func TestNote(t *testing.T) {
	t.Parallel()

	if got := note(client.Failure{StatusCode: http.StatusUnauthorized, Credentialed: false}); got != "" {
		t.Errorf("a 401 with no key sent = %q, want nothing about the key", got)
	}
	if got := note(client.Failure{StatusCode: http.StatusNotFound, Credentialed: true}); got != "" {
		t.Errorf("a 404 = %q, want nothing", got)
	}
	if got := note(client.Failure{StatusCode: http.StatusForbidden, Credentialed: true}); !strings.Contains(got, "admin account") {
		t.Errorf("a 403 = %q, want that the key's user lacks permission", got)
	}
}

// What the tools ask of a refusal: whether the thing is not there, and whether the key may not do it.
func TestIsNotFoundAndIsForbidden(t *testing.T) {
	t.Parallel()

	status := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "the-key")
	if err != nil {
		t.Fatal(err)
	}

	for code, want := range map[int][2]bool{http.StatusNotFound: {true, false}, http.StatusForbidden: {false, true}, http.StatusUnauthorized: {false, true}, http.StatusBadRequest: {false, false}} {
		status = code
		_, err := c.LibraryFindOne(t.Context(), "l1")
		if got := [2]bool{IsNotFound(err), IsForbidden(err)}; got != want {
			t.Errorf("a %d: not found %v, forbidden %v; want %v", code, got[0], got[1], want)
		}
	}
	if IsNotFound(nil) || IsForbidden(nil) {
		t.Error("no error is neither")
	}
}
