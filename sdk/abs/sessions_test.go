package abs

import (
	"reflect"
	"testing"
)

// Every field of a session is copied from a client's form of it, so a field a new generation adds to both forms fails here until it is copied.
func TestSession(t *testing.T) {
	t.Parallel()

	var client PlaybackSessionForClient
	fill(reflect.ValueOf(&client).Elem(), 0)

	for _, empty := range emptyFields(reflect.ValueOf(client.Session()), "") {
		t.Errorf("Session leaves %s empty, which the client's form has", empty)
	}
}
