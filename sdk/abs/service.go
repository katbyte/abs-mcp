package abs

import (
	"cmp"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/pandorest/client"
)

// service is what is Audiobookshelf's own about being talked to, which the base client every generated method sends through is made from (see
// New): what it is called, how its refusals read, and what is known of them. This file is written by hand and lives beside the generated ones,
// as do the few other files that say so; everything else in the package is pandorest's.
var service = client.Service{
	Name:      "Audiobookshelf",
	UserAgent: "abs-mcp/" + version.Version,
	Example:   "nas:13378",
	// Audiobookshelf answers where it is asked, so a redirect means the address points at something in front of it: an http address a proxy
	// moves to https, or a login page
	RedirectAdvice: "set the server url to the address Audiobookshelf itself answers on (--server / ABS_SERVER)",
	WebPageAdvice:  "check the server url: it must be Audiobookshelf's own address, not a proxy's login page",
	Messages:       errorMessages,
	Note:           note,
}

// note says what is known of a failure on an Audiobookshelf.
func note(f client.Failure) string {
	switch {
	case f.StatusCode == http.StatusUnauthorized && f.Credentialed:
		return "API key rejected: check it under Settings → Users → API Keys, where it may have expired or been made inactive"
	case f.StatusCode == http.StatusForbidden:
		return "the API key's user lacks permission for this: most write operations need an admin account, and an account limited to some libraries or tags cannot open the rest"
	}

	return ""
}

// IsNotFound reports whether err is Audiobookshelf answering that what was asked for is not there.
func IsNotFound(err error) bool {
	return client.StatusCode(err) == http.StatusNotFound
}

// IsForbidden reports whether err is Audiobookshelf refusing the key: a 401, or the 403 a route for admins answers any other key.
func IsForbidden(err error) bool {
	code := client.StatusCode(err)

	return code == http.StatusForbidden || code == http.StatusUnauthorized
}

// maxMessage is the longest plain-text body read as what the server said, rather than as a page or a dump.
const maxMessage = 500

// errorMessages reads what Audiobookshelf said went wrong out of an error body: most of its refusals are a line of plain text, res.status(400).send
// ('Invalid request'), and a few are JSON with an error or a message. A web page, or anything else, is not one of them.
func errorMessages(body []byte) []string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil
	}

	if strings.HasPrefix(text, "{") {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &e); err != nil {
			return nil
		}
		if m := strings.TrimSpace(cmp.Or(e.Error, e.Message)); m != "" {
			return []string{m}
		}

		return nil
	}

	// a page, a proxy's or the web app's, starts with a tag
	if len(text) > maxMessage || !utf8.ValidString(text) || strings.HasPrefix(text, "<") {
		return nil
	}

	return []string{text}
}
