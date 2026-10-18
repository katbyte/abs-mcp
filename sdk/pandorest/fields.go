package main

import (
	"fmt"
	"slices"
	"strings"
)

// requiredAt is a record a request sends and some of its fields: the ones the server refuses the request without, or the ones it keeps as
// given.
type requiredAt struct {
	// at is a place as recordsAt names one, an operation's body or a record: "POST /api/share/mediaitem body ."
	at     string
	fields []string
}

// requiredFields are the fields a request must carry even when they are zero, which the generated client then always sends rather than leave
// out as empty.
var requiredFields = []requiredAt{
	// ShareController.createMediaItemShare: 400 without a slug, a type or an item, and for an expiry that is not a number, which one left out
	// is not; 0 is no expiry
	{"POST /api/share/mediaitem body .", []string{"expiresAt", "mediaItemId", "mediaItemType", "slug"}},
	// MeController.createBookmark and updateBookmark: 400 without a time or a title, and a bookmark at the very start is at 0
	{"POST /api/me/item/{id}/bookmark body .", []string{"time", "title"}},
	{"PATCH /api/me/item/{id}/bookmark body .", []string{"time", "title"}},
	// LibraryItemController.updateMediaChapters: 400 for a chapter without a title, a start or an end, and the first starts at 0
	{"Chapter .", []string{"end", "start", "title"}},
}

// keptAsGiven are the fields of a request the server stores as they are given, null included, and leaves as they were when left out: there
// zero is a value, which the generated client tells from a field left out by a pointer (KeepNull in the service), and the document says null
// so the importer allows one.
var keptAsGiven = []requiredAt{
	// MediaProgress.applyProgressUpdate: this.set(progressPayload), whatever the payload holds
	{"ProgressUpdatePayload .", []string{"currentTime", "duration", "ebookProgress", "progress"}},
}

// zeroMeant are the numbers of a request whose zero is a setting the server keeps, and which it takes only when one is sent: the document says
// they may be null, which the server reads as no change, so the generated client holds them by pointer (KeepNull in the service) and sends 0.
var zeroMeant = []requiredAt{
	// Podcast.updateFromRequest takes each only when it is a number: 0 keeps every episode, and downloads every new one
	{"MediaUpdate .", []string{"maxEpisodesToKeep", "maxNewEpisodesToDownload"}},
}

// queryAt is a route and the query parameters it reads where the scan cannot follow them.
type queryAt struct {
	// at is an operation as a place names one, without a path: "POST /api/tools/item/{id}/encode-m4b"
	at    string
	names []string
}

// queriesAt are the query parameters a route reads beyond what its handler does, all of them text.
var queriesAt = []queryAt{
	// ToolsController.encodeM4b hands the whole query to AbMergeManager, which keeps it with the merge's task until ffmpegHelpers.mergeAudioFiles
	// reads it as an AbMergeEncodeOptions
	{"POST /api/tools/item/{id}/encode-m4b", []string{"bitrate", "channels", "codec"}},
}

// rangedAt are the routes that answer part of a file for a Range header, which Express's sendFile does on its own where the scan cannot see
// it: each is given the header to send and a 206 answer, carrying the part, beside its 200.
var rangedAt = []string{
	// LibraryItemController.getLibraryFile: res.sendFile(libraryFile.metadata.path)
	"GET /api/items/{id}/file/{fileid}",
}

// textAt are the routes whose JSON answer is sent only for a request the generated client never makes, and which answer every other with
// text: each 200 is stated as the text the client is sent.
var textAt = []string{
	// MeController.updatePassword: JSON with fresh tokens only for a request carrying x-refresh-token, which a client signed in with an API key
	// does not send; res.sendStatus(200) otherwise
	"PATCH /api/me/password",
}

// applyText states each route's 200 as text, and returns the routes the document does not have, or has with no 200.
func applyText(doc *document, all []string) (missing []string) {
	for _, at := range all {
		verb, path, _ := strings.Cut(at, " ")
		op := doc.Paths[path][strings.ToLower(verb)]
		if op == nil {
			missing = append(missing, at)
			continue
		}
		if _, ok := op.Responses["200"]; !ok {
			missing = append(missing, at)
			continue
		}

		op.Responses["200"] = response{Description: "text", Content: map[string]media{"text/plain": {Schema: &schema{Type: "string"}}}}
	}

	return missing
}

// applyRanges gives each route a Range header and a 206 answer like its 200, and returns the routes the document does not have, or has
// with no 200 to copy.
func applyRanges(doc *document, all []string) (missing []string) {
	for _, at := range all {
		verb, path, _ := strings.Cut(at, " ")
		op := doc.Paths[path][strings.ToLower(verb)]
		if op == nil {
			missing = append(missing, at)
			continue
		}
		whole, ok := op.Responses["200"]
		if !ok {
			missing = append(missing, at)
			continue
		}

		if !slices.ContainsFunc(op.Parameters, func(p parameter) bool { return p.In == "header" && strings.EqualFold(p.Name, "Range") }) {
			op.Parameters = append(op.Parameters, parameter{Name: "Range", In: "header", Schema: &schema{Type: "string"}})
		}
		op.Responses["206"] = response{Description: "part of the file, the bytes the Range header asked for", Content: whole.Content}
	}

	return missing
}

// applyQueries adds each route's stated query parameters it does not have yet, and returns the routes the document does not have.
func applyQueries(doc *document, all []queryAt) (missing []string) {
	for _, q := range all {
		verb, path, _ := strings.Cut(q.at, " ")
		op := doc.Paths[path][strings.ToLower(verb)]
		if op == nil {
			missing = append(missing, q.at)
			continue
		}

		for _, n := range q.names {
			if !slices.ContainsFunc(op.Parameters, func(p parameter) bool { return p.In == "query" && p.Name == n }) {
				op.Parameters = append(op.Parameters, parameter{Name: n, In: "query", Schema: &schema{Type: "string"}})
			}
		}
	}

	return missing
}

// applyNullable marks the fields of each record nullable, and returns the places whose record or operation the document does not have. A
// field the record does not have is an error.
func applyNullable(doc *document, all []requiredAt) (missing []string, err error) {
	for _, r := range all {
		root, path, err := placeRoot(doc, r.at)
		if err != nil {
			return nil, err
		}
		if root == nil {
			missing = append(missing, r.at)
			continue
		}

		s, err := schemaAt(*root, path)
		if err != nil {
			return nil, fmt.Errorf("nullable at %s: %w", r.at, err)
		}
		for _, f := range r.fields {
			p := s.Properties[f]
			if p == nil {
				return nil, fmt.Errorf("nullable at %s: the record has no %s", r.at, f)
			}
			p.Nullable = true
		}
	}

	return missing, nil
}

// applyRequired marks the fields of each record required, and returns the places whose operation the document does not have, for the report. A
// field the record does not have is an error, so the statement is mended when the source changes.
func applyRequired(doc *document, all []requiredAt) (missing []string, err error) {
	for _, r := range all {
		root, path, err := placeRoot(doc, r.at)
		if err != nil {
			return nil, err
		}
		if root == nil {
			missing = append(missing, r.at)
			continue
		}

		s, err := schemaAt(*root, path)
		if err != nil {
			return nil, fmt.Errorf("required at %s: %w", r.at, err)
		}
		for _, f := range r.fields {
			if s.Properties[f] == nil {
				return nil, fmt.Errorf("required at %s: the record has no %s", r.at, f)
			}
			if !slices.Contains(s.Required, f) {
				s.Required = append(s.Required, f)
			}
		}
		slices.Sort(s.Required)
	}

	return missing, nil
}
