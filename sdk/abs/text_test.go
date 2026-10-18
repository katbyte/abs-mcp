package abs

import (
	"encoding/json"
	"testing"
)

// Text reads as itself, a number as it was written, and null as empty; something else is an error. It writes as text.
func TestTextOrNumber(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ name, data, want string }{
		{"text", `"1965"`, "1965"},
		{"a whole number", ` 1965 `, "1965"},
		{"a fraction", `4.5`, "4.5"},
		{"null", `null`, ""},
		{"empty text", `""`, ""},
	} {
		var got TextOrNumber
		if err := json.Unmarshal([]byte(tt.data), &got); err != nil || string(got) != tt.want {
			t.Errorf("%s: %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}

	var got TextOrNumber
	if err := json.Unmarshal([]byte(`true`), &got); err == nil {
		t.Errorf("true: %q, want an error", got)
	}

	written, err := json.Marshal(struct {
		Year TextOrNumber `json:"year"`
	}{"1965"})
	if err != nil || string(written) != `{"year":"1965"}` {
		t.Errorf("written: %s, %v; want the year as text", written, err)
	}
}
