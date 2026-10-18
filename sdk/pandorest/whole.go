package main

import (
	"maps"
	"slices"
)

// wholeNumbers are the fields whose numbers are whole by what they are: counts, sizes in bytes, positions, codes, and times in milliseconds.
// The answers saved from a server mark a number it only ever sent whole, but a small test library sending no fraction proves nothing, so a
// marked number is written as a whole number only under one of these names.
var wholeNumbers = slices.Concat(unixTimes, []string{
	"bitRate", "channels", "count", "coverAspectRatio", "discNumFromFilename", "discNumFromMeta", "expiresAt", "fileSize", "id", "index",
	"lengthMs", "limit", "maxEpisodesToKeep", "maxNewEpisodesToDownload", "numAudioFiles", "numAuthorsAdded", "numBooks", "numChapters",
	"numEpisodes", "numFiles", "numItems", "numTracks", "page", "playMethod", "size", "startOffsetMs", "total", "trackCount",
	"trackNumFromFilename", "trackNumFromMeta",
})

// settleWhole writes each number the answers marked whole, under one of names, as a whole number, through every record and operation of the
// document, and returns how many it settled.
func settleWhole(doc *document, names []string) int {
	settled := 0
	var walk func(s *schema)
	walk = func(s *schema) {
		if s == nil {
			return
		}
		for _, n := range slices.Sorted(maps.Keys(s.Properties)) {
			p := s.Properties[n]
			if p.whole && p.Type == "number" && slices.Contains(names, n) {
				p.Type, p.whole = "integer", false
				settled++
			}
			walk(p)
		}
		walk(s.Items)
		walk(s.Values)
		for _, o := range s.OneOf {
			walk(o)
		}
	}

	for _, n := range slices.Sorted(maps.Keys(doc.Components.Schemas)) {
		walk(doc.Components.Schemas[n])
	}
	for _, p := range slices.Sorted(maps.Keys(doc.Paths)) {
		for _, m := range slices.Sorted(maps.Keys(doc.Paths[p])) {
			op := doc.Paths[p][m]
			if op.RequestBody != nil {
				for _, c := range op.RequestBody.Content {
					walk(c.Schema)
				}
			}
			for _, r := range op.Responses {
				for _, c := range r.Content {
					walk(c.Schema)
				}
			}
		}
	}

	return settled
}
