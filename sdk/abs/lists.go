package abs

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
)

// This file is written by hand, beside the generated ones.

// ListOrEmpty is a list the server writes as empty text, rather than an empty list, when it has nothing in it: the day's log before its first
// line. It reads either as a list, and writes as one.
type ListOrEmpty[T any] []T

// UnmarshalJSON reads a list, and empty text or null as an empty one.
func (l *ListOrEmpty[T]) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte(`""`)) || bytes.Equal(data, []byte("null")) {
		*l = ListOrEmpty[T]{}
		return nil
	}

	var list []T
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	*l = list

	return nil
}

// ListFilter is a list's filter query: a group alone, such as issues, or a group and a value, written <group>.<value in base64>, such as a
// series by its id or a genre by its name. The groups include genres, tags, series, authors, narrators, languages, publishers,
// publishedDecades, progress, missing, ebooks, tracks and abridged.
func ListFilter(group, value string) string {
	if group == "" || value == "" {
		return group
	}

	return group + "." + PathName(value)
}

// PathName is a tag, genre or narrator as the server names one in a path, to rename or remove it: the value in base64, as a list's filter
// writes it.
func PathName(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}
