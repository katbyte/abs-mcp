package abs

import (
	"encoding/json"
	"slices"
	"testing"
)

// A list reads as itself, and empty text or null as an empty list; something else is an error.
func TestListOrEmpty(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name, data string
		want       []int
	}{
		{"a list", `[1, 2]`, []int{1, 2}},
		{"empty text", ` "" `, []int{}},
		{"null", `null`, []int{}},
		{"an empty list", `[]`, []int{}},
	} {
		var got ListOrEmpty[int]
		if err := json.Unmarshal([]byte(tt.data), &got); err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("%s: %v, %v; want %v", tt.name, got, err, tt.want)
		}
	}

	var got ListOrEmpty[int]
	if err := json.Unmarshal([]byte(`"text"`), &got); err == nil {
		t.Errorf("text that is not empty: %v, want an error", got)
	}
}

// A filter is its group, and its value in base64 when it has one.
func TestListFilter(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ group, value, want string }{
		{"issues", "", "issues"},
		{"series", "s1", "series.czE="},
		{"genres", "Science Fiction", "genres.U2NpZW5jZSBGaWN0aW9u"},
		{"", "x", ""},
	} {
		if got := ListFilter(tt.group, tt.value); got != tt.want {
			t.Errorf("ListFilter(%q, %q) = %q, want %q", tt.group, tt.value, got, tt.want)
		}
	}
}

// A name in a path is its value in base64, which the server decodes: a value with a slash in it is one name, not two parts of the path.
func TestPathName(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ value, want string }{
		{"Sci-Fi", "U2NpLUZp"},
		{"Fantasy / Horror", "RmFudGFzeSAvIEhvcnJvcg=="},
		{"Jane Doe, Ph.D.", "SmFuZSBEb2UsIFBoLkQu"},
	} {
		if got := PathName(tt.value); got != tt.want {
			t.Errorf("PathName(%q) = %q, want %q", tt.value, got, tt.want)
		}
	}
}
