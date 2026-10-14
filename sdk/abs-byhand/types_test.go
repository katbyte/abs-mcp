package abs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCountRowN(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		row  CountRow
		want int
	}{
		{"books win", CountRow{NumBooks: 3, NumItems: 9, Count: 9}, 3},
		{"then items", CountRow{NumItems: 7, Count: 9}, 7},
		{"then the plain count", CountRow{Count: 5}, 5},
		{"and zero when empty", CountRow{}, 0},
	} {
		if got := c.row.N(); got != c.want {
			t.Errorf("%s: N() = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestEpisodeDurationSeconds(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		ep   Episode
		want float64
	}{
		{"its own duration wins", Episode{Duration: 120, AudioFile: &AudioFile{Duration: 999}}, 120},
		{"falls back to the audio file", Episode{AudioFile: &AudioFile{Duration: 300}}, 300},
		{"then to the audio track", Episode{AudioTrack: &AudioTrack{Duration: 42}}, 42},
		{"and is zero with nothing to go on", Episode{}, 0},
	} {
		if got := c.ep.DurationSeconds(); got != c.want {
			t.Errorf("%s: DurationSeconds() = %v, want %v", c.name, got, c.want)
		}
	}
}

// DeviceInfo.Describe names the client and device in a playback session, and
// has to survive a nil receiver and every field being empty.
func TestDeviceInfoDescribe(t *testing.T) {
	t.Parallel()

	var nilDevice *DeviceInfo
	if got := nilDevice.Describe(); got != "" {
		t.Errorf("nil DeviceInfo described as %q, want empty", got)
	}
	if got := (&DeviceInfo{}).Describe(); got != "" {
		t.Errorf("empty DeviceInfo described as %q, want empty", got)
	}
	if got := (&DeviceInfo{ClientName: "abs-mcp"}).Describe(); !strings.Contains(got, "abs-mcp") {
		t.Errorf("Describe() = %q, want the client name", got)
	}
	// browser name stands in when there is no client name
	if got := (&DeviceInfo{BrowserName: "Firefox"}).Describe(); !strings.Contains(got, "Firefox") {
		t.Errorf("Describe() = %q, want the browser name", got)
	}
}

func TestFlexStringAndSeriesRefs(t *testing.T) {
	t.Parallel()

	var m Metadata
	raw := `{"title":"Dune","publishedYear":1965,"series":{"id":"s1","name":"Dune","sequence":"1"},"itunesId":null}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.PublishedYear != "1965" {
		t.Errorf("publishedYear = %q", m.PublishedYear)
	}
	if len(m.Series) != 1 || m.Series[0].Sequence != "1" {
		t.Errorf("single-object series not decoded: %+v", m.Series)
	}
	if got := m.SeriesDisplay(); len(got) != 1 || got[0] != "Dune #1" {
		t.Errorf("SeriesDisplay = %v", got)
	}

	raw = `{"publishedYear":"1965","series":[{"name":"A","sequence":"2"},{"name":"B"}],"narrators":["X","Y"],"authors":[{"id":"a","name":"Frank Herbert"}]}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Series) != 2 || m.AuthorDisplay() != "Frank Herbert" || m.NarratorDisplay() != "X, Y" {
		t.Errorf("array shapes: %+v", m)
	}
}

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
