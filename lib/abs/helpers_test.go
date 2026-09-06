package abs

import (
	"net/url"
	"strings"
)

func parseQuery(raw string) (url.Values, error) { return url.ParseQuery(raw) }

func contains(s, sub string) bool { return strings.Contains(s, sub) }
