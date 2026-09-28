package abs

import (
	"net/http"
	"testing"
)

func TestProgressNotFoundIsNil(t *testing.T) {
	t.Parallel()

	c := newClient(t, newJSONServer(t, always(http.StatusNotFound, "")))
	p, err := c.Progress(t.Context(), "i1", "")
	if err != nil || p != nil {
		t.Errorf("got %v, %v", p, err)
	}
}
