package abs

import (
	"encoding/json"
	"testing"
)

// The built-in providers write a candidate's series name under "series"; a
// custom provider may write "name". Both have to decode to a title, or every
// candidate's series comes back as a bare " #4".
func TestSearchSeriesTitle(t *testing.T) {
	t.Parallel()

	raw := `[
		{"title":"So Long, and Thanks for All the Fish","series":[{"series":"The Hitchhiker's Guide to the Galaxy","sequence":"4"}]},
		{"title":"Custom","series":[{"name":"Custom Series","sequence":"2"}]},
		{"title":"None","series":[]}
	]`
	var hits []BookSearchResult
	if err := json.Unmarshal([]byte(raw), &hits); err != nil {
		t.Fatal(err)
	}
	if got := hits[0].Series[0].Title(); got != "The Hitchhiker's Guide to the Galaxy" {
		t.Errorf("provider series = %q", got)
	}
	if got := hits[1].Series[0].Title(); got != "Custom Series" {
		t.Errorf("custom series = %q", got)
	}
	if len(hits[2].Series) != 0 {
		t.Errorf("empty series = %v", hits[2].Series)
	}
}
