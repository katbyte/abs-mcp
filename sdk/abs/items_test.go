package abs

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

// Every field of the short form is copied from the whole item, so a field a new generation adds to both forms fails here until it is copied;
// the list's own additions are the only ones left empty.
func TestMinified(t *testing.T) {
	t.Parallel()

	var whole LibraryItemExpanded
	fill(reflect.ValueOf(&whole).Elem(), 0)

	listOnly := []string{"CollapsedSeries", "ProgressLastUpdate", "Media.Metadata.Series"}
	for _, empty := range emptyFields(reflect.ValueOf(whole.Minified()), "") {
		if !slices.Contains(listOnly, empty) {
			t.Errorf("Minified leaves %s empty, which the whole item has", empty)
		}
	}
}

// An item the server sent no media for still has its media and metadata, empty, so a check can read them.
func TestMinifiedWithoutMedia(t *testing.T) {
	t.Parallel()

	m := LibraryItemExpanded{Id: "li"}.Minified()
	if m.Id != "li" || m.Media == nil || m.Media.Metadata == nil {
		t.Errorf("Minified = %+v, want the id with an empty media and metadata", m)
	}
}

// An item's title, cover and size read the same whether the server sent its media or not, and its author and series read as one line from
// whichever fields the form has.
func TestItemReadings(t *testing.T) {
	t.Parallel()

	bare := LibraryItemMinified{Size: 0}
	if bare.Title() != "" || bare.HasCover() || bare.SizeBytes() != 0 || (LibraryItemExpanded{}).Title() != "" {
		t.Error("an item with no media: want an empty title, no cover and no size, and no panic")
	}
	if got := (LibraryItemMinified{Media: &LibraryItemMinifiedMedia{Size: 2048}}).SizeBytes(); got != 2048 {
		t.Errorf("SizeBytes with only the media's size = %d, want 2048", got)
	}

	for _, tt := range []struct {
		name, author string
		want         []string
		got          func() (string, []string)
	}{
		{"a book in a list", "Terry Pratchett", []string{"Discworld #3", "Witches"}, func() (string, []string) {
			m := LibraryItemMinifiedMediaMetadata{AuthorName: "Terry Pratchett", SeriesName: "Discworld #3, Witches"}
			return m.AuthorDisplay(), m.SeriesDisplay()
		}},
		{"a list filtered by series", "", []string{"Discworld #3"}, func() (string, []string) {
			m := LibraryItemMinifiedMediaMetadata{SeriesName: "Discworld #3, Witches", Series: &SeriesMinimal{Name: "Discworld", Sequence: "3"}}
			return m.AuthorDisplay(), m.SeriesDisplay()
		}},
		{"a podcast in a list", "Host", nil, func() (string, []string) {
			m := LibraryItemMinifiedMediaMetadata{Author: "Host"}
			return m.AuthorDisplay(), m.SeriesDisplay()
		}},
		{"a whole book", "A, B", []string{"Discworld #3", "Witches"}, func() (string, []string) {
			m := LibraryItemExpandedMediaMetadata{Authors: []AuthorMinimal{{Name: "A"}, {Name: "B"}}, Series: []SeriesMinimal{{Name: "Discworld", Sequence: "3"}, {Name: "Witches"}}}
			return m.AuthorDisplay(), m.SeriesDisplay()
		}},
		{"a book a session played", "A, B", nil, func() (string, []string) {
			return MediaMetadata{Authors: []AuthorMinimal{{Name: "A"}, {Name: "B"}}}.AuthorDisplay(), nil
		}},
		{"a podcast a session played", "Host", nil, func() (string, []string) {
			return MediaMetadata{Author: "Host"}.AuthorDisplay(), nil
		}},
		{"a whole book with its names written out", "A and B", []string{"Discworld #3"}, func() (string, []string) {
			m := LibraryItemExpandedMediaMetadata{AuthorName: "A and B", Authors: []AuthorMinimal{{Name: "A"}}, SeriesName: "Discworld #3"}
			return m.AuthorDisplay(), m.SeriesDisplay()
		}},
	} {
		author, series := tt.got()
		if author != tt.author || !slices.Equal(series, tt.want) {
			t.Errorf("%s: author %q, series %q; want %q, %q", tt.name, author, series, tt.author, tt.want)
		}
	}

	if got := (LibraryItemExpandedMediaMetadata{Narrators: []string{"A", "B"}}).NarratorDisplay(); got != "A, B" {
		t.Errorf("a whole book's narrators = %q, want \"A, B\"", got)
	}
	if got := (LibraryItemExpandedMediaMetadata{NarratorName: "A and B", Narrators: []string{"A"}}).NarratorDisplay(); got != "A and B" {
		t.Errorf("a whole book's narrators written out = %q, want \"A and B\"", got)
	}

	if got := (SearchFindBooksAnswerSeries{Series: "Discworld", Sequence: "3"}).Title(); got != "Discworld" {
		t.Errorf("a built-in provider's series = %q, want Discworld", got)
	}
	if got := (SearchFindBooksAnswerSeries{Name: "Discworld"}).Title(); got != "Discworld" {
		t.Errorf("a custom provider's series = %q, want Discworld", got)
	}
}

// fill gives every field of v a value that is not its zero, through records, lists and pointers, a few levels down.
func fill(v reflect.Value, depth int) {
	if depth > 6 {
		return
	}

	if v.Type() == reflect.TypeFor[UnixMilli]() {
		v.Set(reflect.ValueOf(UnixMilli{time.UnixMilli(1)}))
		return
	}

	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(1)
	case reflect.Float64:
		v.SetFloat(1)
	case reflect.Interface:
		v.Set(reflect.ValueOf("x"))
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), depth+1)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0), depth+1)
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i), depth+1)
			}
		}
	default:
		// the generated records hold no other kind of value
	}
}

// emptyFields are the fields of v left at their zero value, named by their path, looking into the records v points at.
func emptyFields(v reflect.Value, at string) []string {
	var out []string
	for i := range v.NumField() {
		f, name := v.Field(i), at+v.Type().Field(i).Name
		if !v.Type().Field(i).IsExported() {
			continue
		}
		if f.IsZero() {
			out = append(out, name)
			continue
		}
		if f.Kind() == reflect.Pointer && f.Elem().Kind() == reflect.Struct && f.Type().Elem() != reflect.TypeFor[PodcastEpisode]() {
			out = append(out, emptyFields(f.Elem(), name+".")...)
		}
	}

	return out
}
