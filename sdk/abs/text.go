package abs

import (
	"bytes"
	"encoding/json"
)

// This file is written by hand, beside the generated ones.

// TextOrNumber is text the server passes on from elsewhere as it came, which may be a number: FantLab's year for a book in a provider search.
// It reads either as text, a number as it was written, and writes as text.
type TextOrNumber string

// UnmarshalJSON reads text, a number as its digits, and null as empty.
func (t *TextOrNumber) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		*t = ""
		return nil
	}

	if bytes.HasPrefix(data, []byte(`"`)) {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*t = TextOrNumber(s)
		return nil
	}

	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*t = TextOrNumber(n)

	return nil
}
