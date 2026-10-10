package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"

	"github.com/katbyte/abs-mcp/sdk/abs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A folder renamed or moved on disk is the same book to whoever moved it. A
// server that can follow the folder keeps its record. One that cannot - its
// library on storage that hands a moved folder a new identity - scans a new
// record for the folder where it is now and leaves the old one flagged
// missing, with everything people did to the book still hanging on it: who
// has listened and how far, their bookmarks, the collections and playlists it
// is in, a cover fetched from a store, and on a server that keeps no
// metadata file beside the audio, the whole match. library_issues_remove
// would throw all of that away, and a duplicates audit sees two books.
//
// library_issues_merge puts the two back together: it finds, for each
// missing record, the one record that now holds the same audio files, carries
// across what the old one has and the new one lacks, reads the new record
// back to see that it arrived, and only then deletes the old record.

const (
	// mergeLengthSlack is how far apart two scans may put one book's length:
	// the server measures a file again when it scans it as new, and a
	// 38-hour book came back three seconds shorter
	mergeLengthSlack = 30.0
	// mergeFreshSlack is how long before a record went missing its
	// replacement may have been made, in milliseconds: the server's file
	// watcher can add the folder under its new name a moment before it
	// misses the old one. A record older than that was already in the
	// library - a second copy of the book, not the folder moved
	mergeFreshSlack = 10 * 60 * 1000
	// mergeKeyLife is how long a key made to act as another account lasts,
	// in seconds, if deleting it at the end of the call should fail
	mergeKeyLife = 900
	// mergeKeyName names such a key, for an admin who finds one
	mergeKeyName = "abs-mcp library_issues_merge (temporary)"
	// mergeCoverCap is the largest cover carried: a cover is a picture
	mergeCoverCap = 64 << 20
)

type issuesMergeIn struct {
	Library string   `json:"library,omitempty" jsonschema:"library name or id; default every library"`
	Items   []string `json:"items,omitempty"   jsonschema:"only these missing records, each by its id or its path inside the library; default every one"`
	Into    string   `json:"into,omitempty"    jsonschema:"with exactly one of items: the record to merge it into, by id or path, where the tool will not choose for itself - several records hold the same audio, or the one that does was already in the library before the folder went missing. It still has to hold the same audio files"`
	Confirm bool     `json:"confirm,omitempty" jsonschema:"true to carry everything across and delete the old records; without it the call only reports what it would do"`
}

type mergeRecord struct {
	ID    string `json:"id"`
	Path  string `json:"path"            jsonschema:"inside the library"`
	Title string `json:"title"`
	Added string `json:"added,omitempty" jsonschema:"the day the record was made"`
}

type mergeCarry struct {
	Details     []string `json:"details,omitempty"     jsonschema:"what the old record says that the new one does not: title, authors, series, asin, tags and the like"`
	Chapters    bool     `json:"chapters,omitempty"    jsonschema:"the old record's chapters, where the new record's differ"`
	Cover       bool     `json:"cover,omitempty"       jsonschema:"the old record's cover, where the server keeps it outside the book's folder; carried only if the two pictures differ"`
	Progress    []string `json:"progress,omitempty"    jsonschema:"the accounts with listening progress on the old record that the new one lacks"`
	Bookmarks   []string `json:"bookmarks,omitempty"   jsonschema:"the accounts with bookmarks on it that the new one lacks"`
	Collections []string `json:"collections,omitempty" jsonschema:"the collections holding the old record and not the new"`
	Playlists   []string `json:"playlists,omitempty"   jsonschema:"the playlists holding the old record and not the new, each with its account; without confirm only this key's own are seen"`
}

func (c *mergeCarry) nothing() bool {
	return len(c.Details)+len(c.Progress)+len(c.Bookmarks)+len(c.Collections)+len(c.Playlists) == 0 && !c.Chapters
}

// left is what a carry still has to do, for the message when a record read
// back is not what was sent.
func (c *mergeCarry) left() string {
	var parts []string
	for name, vals := range map[string][]string{"details": c.Details, "progress": c.Progress, "bookmarks": c.Bookmarks, "collections": c.Collections, "playlists": c.Playlists} {
		if len(vals) > 0 {
			parts = append(parts, name+" ("+strings.Join(vals, ", ")+")")
		}
	}
	if c.Chapters {
		parts = append(parts, "chapters")
	}
	slices.Sort(parts)

	return strings.Join(parts, "; ")
}

type mergePair struct {
	From   mergeRecord `json:"from"           jsonschema:"the record whose folder is gone"`
	Into   mergeRecord `json:"into"           jsonschema:"the record the server made for the same audio where it is now"`
	Same   string      `json:"same"           jsonschema:"what shows the two are one book"`
	Carry  mergeCarry  `json:"carry"          jsonschema:"what goes from the old record to the new; empty when the new record already has everything"`
	Lost   []string    `json:"lost,omitempty" jsonschema:"what the server gives no way to carry"`
	Merged bool        `json:"merged"         jsonschema:"carried, read back and the old record deleted; always false without confirm"`
	Kept   string      `json:"kept,omitempty" jsonschema:"why the old record was left in place and nothing more was done to the pair"`
}

type mergeUnpaired struct {
	mergeRecord
	Why        string        `json:"why"`
	Candidates []mergeRecord `json:"candidates,omitempty" jsonschema:"the records that hold the same audio, when there is more than one"`
}

type issuesMergeOut struct {
	Found     int             `json:"found"                        jsonschema:"records whose folder is missing, before this call"`
	Pairs     []mergePair     `json:"pairs"                        jsonschema:"each missing record with the one record that holds the same audio files"`
	Unpaired  []mergeUnpaired `json:"unpaired"                     jsonschema:"missing records with no one record to merge into: left alone. library_issues_remove deletes them, losing what hangs on them"`
	Merged    int             `json:"merged"                       jsonschema:"old records carried across and deleted; 0 without confirm"`
	Remaining int             `json:"remaining"                    jsonschema:"records still flagged missing after this call, read back from the server"`
	Unreached []string        `json:"accounts_unreached,omitempty" jsonschema:"accounts the server would not let a key act as - never logged in, or switched off - so their playlists could not be looked at; a pair with their progress or bookmarks on it was kept"`
	KeysLeft  []string        `json:"keys_left,omitempty"          jsonschema:"keys made to act as another account that could not be deleted afterwards, by id: delete them; each expires on its own within fifteen minutes"`
}

func registerIssuesMerge(r *registry) {
	client := r.client

	add(r, deleteTool, &mcp.Tool{
		Name: "library_issues_merge",
		Description: "Put a book back together after its folder was renamed or moved on a server that did not follow it: the scan made a new record for the folder and left the old one flagged missing (audit_issues), still holding the listening progress, bookmarks, collections and playlists, while audit_duplicates sees two books. " +
			"For each missing record this finds the record made since for the same audio files (same names and sizes), carries across what the old record has and the new lacks - details, tags, chapters, a cover the server kept outside the folder, every account's progress and bookmarks, its place in collections and playlists - reads the new record back, and then deletes the old record. Files are never touched. " +
			"Without confirm=true nothing changes: it lists each pair with what would be carried and what cannot be (the day added, the day each account started it). Left alone, under unpaired: a record with no match, with several, or whose match was in the library before it went missing (a second copy, not the folder moved), unless into names the one. Left alone, with kept saying why: a pair whose old record has an open RSS feed or share link, or something that could not be carried. " +
			"Another account's progress, bookmarks and playlists are written as that account, with a key made for the call and deleted after it. Books only. Admin only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in issuesMergeIn) (*mcp.CallToolResult, issuesMergeOut, error) {
		libs, err := resolveLibraries(ctx, client, in.Library)
		if err != nil {
			return nil, issuesMergeOut{}, err
		}
		// one call at a time, and no edit under it: a record read here is
		// written back whole a moment later
		defer r.locks.holdAll()()

		if in.Into != "" && len(in.Items) != 1 {
			return nil, issuesMergeOut{}, errors.New("into names where one record goes: give that one record in items")
		}
		m := issuesMerger{client: client, confirm: in.Confirm, into: strings.TrimSpace(in.Into)}
		defer func() { _ = m.close(ctx) }()
		out := issuesMergeOut{Pairs: []mergePair{}, Unpaired: []mergeUnpaired{}}
		if err := m.run(ctx, libs, in.Items, &out); err != nil {
			return nil, issuesMergeOut{}, err
		}
		out.Unreached, out.KeysLeft = m.unreached, m.close(ctx)

		return nil, out, nil
	})
}

// issuesMerger is one library_issues_merge call.
type issuesMerger struct {
	client  *abs.Client
	confirm bool
	into    string // the record the caller chose for the one missing record asked for

	me    *abs.User
	users []abs.User // every account, each with its progress and bookmarks
	feeds map[string]bool
	// acting holds a client for each account written to, the caller's own
	// among them; keys are the ones made for the others
	acting    map[string]*abs.Client
	keys      []string
	unreached []string
	closed    bool
}

// close deletes the keys made to act as other accounts and returns the ids
// of any it could not, which expire on their own.
func (m *issuesMerger) close(ctx context.Context) []string {
	if m.closed {
		return nil
	}
	m.closed = true

	var left []string
	for _, id := range m.keys {
		if err := m.client.DeleteAPIKey(context.WithoutCancel(ctx), id); err != nil {
			left = append(left, id)
		}
	}

	return left
}

func (m *issuesMerger) run(ctx context.Context, libs []abs.Library, only []string, out *issuesMergeOut) error {
	var missing []abs.Item
	present := map[int][]abs.Item{} // by how many audio files a record has
	libOf := map[string]*abs.Library{}
	for i := range libs {
		lib := &libs[i]
		libOf[lib.ID] = lib
		if err := m.client.ItemsAll(ctx, lib.ID, abs.ItemsOptions{}, func(items []abs.Item) bool {
			for j := range items {
				switch it := &items[j]; {
				case it.IsMissing:
					missing = append(missing, *it)
				case !it.IsInvalid && !it.IsPodcast():
					present[it.Media.NumAudioFiles] = append(present[it.Media.NumAudioFiles], *it)
				}
			}
			return true
		}); err != nil {
			return err
		}
	}
	out.Found, out.Remaining = len(missing), len(missing)
	missing, err := mergeOnly(missing, only)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}

	type pair struct{ old, fresh abs.Item }
	var pairs []pair
	for i := range missing {
		old := &missing[i]
		if old.IsPodcast() {
			out.Unpaired = append(out.Unpaired, mergeUnpaired{mergeRecord: mergeRecordOf(old), Why: "a podcast: its progress hangs on its episodes, which this does not carry"})
			continue
		}
		fresh, others, why, err := m.sameAudio(ctx, old, present[old.Media.NumAudioFiles])
		if err != nil {
			return err
		}
		if fresh == nil {
			row := mergeUnpaired{mergeRecord: mergeRecordOf(old), Why: why}
			for k := range others {
				row.Candidates = append(row.Candidates, mergeRecordOf(&others[k]))
			}
			out.Unpaired = append(out.Unpaired, row)
			continue
		}
		pairs = append(pairs, pair{*old, *fresh})
		out.Pairs = append(out.Pairs, mergePair{From: mergeRecordOf(old), Into: mergeRecordOf(fresh), Same: why})
	}
	if len(pairs) == 0 {
		return nil
	}

	if err := m.readAccounts(ctx); err != nil {
		return err
	}
	for i := range pairs {
		row := &out.Pairs[i]
		lib := libOf[pairs[i].old.LibraryID]
		plan, err := m.plan(ctx, lib, &pairs[i].old, &pairs[i].fresh)
		if err != nil {
			return err
		}
		row.Carry, row.Lost, row.Kept = plan.carry, plan.lost, plan.blocked
		if !m.confirm || plan.blocked != "" {
			continue
		}

		if err := m.carry(ctx, lib, plan); err != nil {
			row.Kept = "carrying it across failed part way, so the old record is kept; run it again once this is put right: " + err.Error()
			continue
		}
		// what arrived is read back, not taken from what was sent
		if err := m.readAccounts(ctx); err != nil {
			return err
		}
		after, err := m.plan(ctx, lib, &pairs[i].old, &pairs[i].fresh)
		if err != nil {
			return err
		}
		if !after.carry.nothing() {
			row.Kept = "after carrying, the new record still lacks: " + after.carry.left()
			continue
		}
		if err := m.client.DeleteItem(ctx, pairs[i].old.ID, false); err != nil {
			row.Kept = "everything was carried across, but deleting the old record failed: " + err.Error()
			continue
		}
		row.Merged = true
		out.Merged++
	}
	if out.Merged == 0 {
		return nil
	}

	// the count is read back rather than taken from what was deleted
	left := 0
	for i := range libs {
		page, err := m.client.Items(ctx, libs[i].ID, abs.ItemsOptions{Limit: 1, Filter: "issues", Minified: true})
		if err != nil {
			return fmt.Errorf("%d records were merged, but reading the library back failed: %w", out.Merged, err)
		}
		left += page.Total
	}
	out.Remaining = left

	return nil
}

// mergeOnly narrows the missing records to the ones asked for, each named by
// its id or its path inside the library.
func mergeOnly(missing []abs.Item, only []string) ([]abs.Item, error) {
	if len(only) == 0 {
		return missing, nil
	}
	var out []abs.Item
	for _, want := range only {
		want = strings.TrimSpace(want)
		i := slices.IndexFunc(missing, func(it abs.Item) bool {
			return it.ID == want || strings.Trim(it.RelPath, "/") == strings.Trim(want, "/")
		})
		if i < 0 {
			return nil, fmt.Errorf("%q is not a record whose folder is missing: give its id or its path inside the library, as audit_issues lists them", want)
		}
		if !slices.ContainsFunc(out, func(it abs.Item) bool { return it.ID == missing[i].ID }) {
			out = append(out, missing[i])
		}
	}

	return out, nil
}

func mergeRecordOf(it *abs.Item) mergeRecord {
	return mergeRecord{ID: it.ID, Path: it.RelPath, Title: it.Title(), Added: fmtDate(it.AddedAt)}
}

// sameAudio finds the record that took old's place: the one holding its
// audio files that was made when old went missing, or after. It answers with
// that record and what was compared, or with no record, why, and the records
// that hold the same audio without being it - several made since, or copies
// that were in the library all along. Candidates have as many audio files as
// old; the listing's lengths narrow them before any is read in full.
func (m *issuesMerger) sameAudio(ctx context.Context, old *abs.Item, candidates []abs.Item) (fresh *abs.Item, others []abs.Item, why string, err error) {
	if old.Media.NumAudioFiles == 0 {
		return nil, nil, "it holds no audio to tell it by", nil
	}
	ids := []string{old.ID}
	for i := range candidates {
		if math.Abs(candidates[i].Media.Duration-old.Media.Duration) <= mergeLengthSlack {
			ids = append(ids, candidates[i].ID)
		}
	}
	var whole []abs.Item
	for chunk := range slices.Chunk(ids, embedBatchSize) {
		items, err := m.client.ItemsBatch(ctx, chunk)
		if err != nil {
			return nil, nil, "", err
		}
		whole = append(whole, items...)
	}
	at := slices.IndexFunc(whole, func(it abs.Item) bool { return it.ID == old.ID })
	if at < 0 {
		return nil, nil, "", fmt.Errorf("the server did not answer with the missing record %s (%s) when asked for it by id", old.ID, old.RelPath)
	}
	*old = whole[at]

	// a scan stamps the record it finds missing; the file watcher only
	// saves it
	since := old.LastScan
	if since == 0 {
		since = old.UpdatedAt
	}
	var made, standing []abs.Item
	how := ""
	for i := range whole {
		same, by := sameAudioFiles(old.Media.AudioFiles, whole[i].Media.AudioFiles)
		if i == at || !same {
			continue
		}
		how = by
		switch {
		case m.into != "":
			if whole[i].ID == m.into || strings.Trim(whole[i].RelPath, "/") == strings.Trim(m.into, "/") {
				made = append(made, whole[i])
			}
		case whole[i].AddedAt+mergeFreshSlack >= since:
			made = append(made, whole[i])
		default:
			standing = append(standing, whole[i])
		}
	}

	switch {
	case len(made) == 1:
		return &made[0], nil, how, nil
	case m.into != "":
		return nil, nil, fmt.Sprintf("%q does not hold the same audio files, so it is not this book's record", m.into), nil
	case len(made) > 1:
		return nil, made, fmt.Sprintf("%d records hold the same audio files and were made since it went missing, and nothing says which took its place: name one with into", len(made)), nil
	case len(standing) > 0:
		return nil, standing, "the same audio files are in a record that was already in the library before this one went missing: a second copy of the book, not its folder moved. To carry this record's progress and details onto it anyway, name it with into", nil
	}

	return nil, nil, "no record holds the same audio files: the folder was deleted, or its share was not there when the library was scanned", nil
}

// sameAudioFiles reports whether two records hold the same audio: as many
// files, each with one of the same size and the same name. A book that is one
// file may have been renamed itself, so there the size and the length stand
// for the name.
func sameAudioFiles(old, fresh []abs.AudioFile) (same bool, how string) {
	if len(old) == 0 || len(old) != len(fresh) {
		return false, ""
	}
	if len(old) == 1 && old[0].Metadata.Filename != fresh[0].Metadata.Filename {
		if old[0].Metadata.Size == fresh[0].Metadata.Size && math.Abs(old[0].Duration-fresh[0].Duration) <= 1 {
			return true, "one audio file of the same size and length, under another name"
		}
		return false, ""
	}

	taken := make([]bool, len(fresh))
	for i := range old {
		k := slices.IndexFunc(fresh, func(f abs.AudioFile) bool {
			return f.Metadata.Size == old[i].Metadata.Size && f.Metadata.Filename == old[i].Metadata.Filename
		})
		// two files of one name and size in a book would each be counted once
		for k >= 0 && taken[k] {
			next := slices.IndexFunc(fresh[k+1:], func(f abs.AudioFile) bool {
				return f.Metadata.Size == old[i].Metadata.Size && f.Metadata.Filename == old[i].Metadata.Filename
			})
			if next < 0 {
				k = -1
				break
			}
			k += next + 1
		}
		if k < 0 {
			return false, ""
		}
		taken[k] = true
	}
	if len(old) == 1 {
		return true, "one audio file of the same name and size"
	}

	return true, fmt.Sprintf("%d audio files of the same names and sizes", len(old))
}

// readAccounts reads every account with its progress and bookmarks, and the
// open feeds. Only an admin can.
func (m *issuesMerger) readAccounts(ctx context.Context) error {
	if m.me == nil {
		me, err := m.client.Me(ctx)
		if err != nil {
			return err
		}
		m.me = me
		feeds, err := m.client.Feeds(ctx)
		if err != nil {
			return fmt.Errorf("reading the open RSS feeds, which only an admin key can: %w", err)
		}
		m.feeds = map[string]bool{}
		for i := range feeds {
			m.feeds[feeds[i].EntityID] = true
		}
	}

	list, err := m.client.Users(ctx, false)
	if err != nil {
		return fmt.Errorf("reading the accounts, which only an admin key can: %w", err)
	}
	m.users = m.users[:0]
	for i := range list {
		u, err := m.client.User(ctx, list[i].ID)
		if err != nil {
			return fmt.Errorf("reading the account %s: %w", list[i].Username, err)
		}
		m.users = append(m.users, *u)
	}

	return nil
}

// mergePlan is what one pair needs: read from the server, and read again
// after carrying to see nothing is left.
type mergePlan struct {
	old, fresh *abs.Item
	carry      mergeCarry
	lost       []string
	blocked    string

	details     *abs.MediaUpdate
	progress    []mergeProgress
	bookmarks   []mergeBookmarks
	collections []abs.Collection
	playlists   []abs.Playlist // the caller's own; another account's are read as that account
}

type mergeProgress struct {
	user *abs.User
	was  abs.MediaProgress
}

type mergeBookmarks struct {
	user *abs.User
	add  []abs.Bookmark
}

func (m *issuesMerger) plan(ctx context.Context, lib *abs.Library, old, stale *abs.Item) (*mergePlan, error) {
	fresh, err := m.client.Item(ctx, stale.ID)
	if err != nil {
		return nil, err
	}
	p := &mergePlan{old: old, fresh: fresh}

	if m.feeds[old.ID] {
		p.blocked = "an RSS feed is open on the old record, and deleting the record would close it: close it, or open one on the new record first (feed_edit)"
	}
	share, err := m.client.ItemShare(ctx, old.ID)
	if err != nil {
		return nil, err
	}
	if share != nil && p.blocked == "" {
		p.blocked = "a share link is open on the old record, and deleting the record would end it: end it, or share the new record first (item_share)"
	}

	p.details, p.carry.Details = mergeDetails(old, fresh)
	p.carry.Chapters = !slices.EqualFunc(old.Media.Chapters, fresh.Media.Chapters, func(a, b abs.Chapter) bool {
		return a.Title == b.Title && a.Start == b.Start && a.End == b.End
	}) && len(old.Media.Chapters) > 0
	// a cover inside the old folder went with the folder; one the server
	// kept for the record is the record's alone
	p.carry.Cover = old.Media.CoverPath != "" && !strings.HasPrefix(old.Media.CoverPath, strings.TrimRight(old.Path, "/")+"/") && old.Media.CoverPath != fresh.Media.CoverPath

	for i := range m.users {
		u := &m.users[i]
		was, has := mergeProgressOn(u, old.ID), mergeProgressOn(u, fresh.ID)
		if was != nil && (has == nil || !sameProgress(was, has)) {
			p.progress = append(p.progress, mergeProgress{u, *was})
			p.carry.Progress = append(p.carry.Progress, u.Username)
			if was.StartedAt > 0 {
				p.lost = append(p.lost, fmt.Sprintf("the day %s started it (%s)", u.Username, fmtDate(was.StartedAt)))
			}
		}
		var add []abs.Bookmark
		for _, b := range u.Bookmarks {
			if b.LibraryItemID == old.ID && !slices.ContainsFunc(u.Bookmarks, func(o abs.Bookmark) bool { return o.LibraryItemID == fresh.ID && o.Time == b.Time }) {
				add = append(add, b)
			}
		}
		if len(add) > 0 {
			p.bookmarks = append(p.bookmarks, mergeBookmarks{u, add})
			p.carry.Bookmarks = append(p.carry.Bookmarks, u.Username)
		}
	}

	collections, err := m.client.Collections(ctx, lib.ID)
	if err != nil {
		return nil, err
	}
	for i := range collections {
		if holdsBook(collections[i].Books, old.ID) && !holdsBook(collections[i].Books, fresh.ID) {
			p.collections = append(p.collections, collections[i])
			p.carry.Collections = append(p.carry.Collections, collections[i].Name)
		}
	}
	playlists, err := m.client.Playlists(ctx, lib.ID)
	if err != nil {
		return nil, err
	}
	for i := range playlists {
		if holdsEntry(playlists[i].Items, old.ID) && !holdsEntry(playlists[i].Items, fresh.ID) {
			p.playlists = append(p.playlists, playlists[i])
			p.carry.Playlists = append(p.carry.Playlists, playlists[i].Name+" ("+m.me.Username+")")
		}
	}

	if was, now := fmtDate(old.AddedAt), fmtDate(fresh.AddedAt); was != "" && was != now {
		p.lost = append(p.lost, fmt.Sprintf("the day it was added (%s): the new record says %s", was, now))
	}

	return p, nil
}

func holdsBook(books []abs.Item, id string) bool {
	return slices.ContainsFunc(books, func(b abs.Item) bool { return b.ID == id })
}

func holdsEntry(entries []abs.PlaylistItem, id string) bool {
	return slices.ContainsFunc(entries, func(e abs.PlaylistItem) bool { return e.LibraryItemID == id && e.EpisodeID == "" })
}

func mergeProgressOn(u *abs.User, itemID string) *abs.MediaProgress {
	for i := range u.MediaProgress {
		if p := &u.MediaProgress[i]; p.LibraryItemID == itemID && p.EpisodeID == "" {
			return p
		}
	}

	return nil
}

// sameProgress reports whether the progress a record has says what it was on
// another: where the listener is, whether they finished and when, and
// whether they took it off the continue shelf. A finish with no day recorded
// has none to keep: the server stamps the day it is carried.
func sameProgress(was, has *abs.MediaProgress) bool {
	return math.Abs(was.CurrentTime-has.CurrentTime) < 1 && was.IsFinished == has.IsFinished && (was.FinishedAt == 0 || was.FinishedAt == has.FinishedAt) &&
		was.HideFromContinueListening == has.HideFromContinueListening && was.EbookLocation == has.EbookLocation
}

// mergeDetails is the update that makes fresh say what old says, with the
// names of what differs, or nil when nothing does.
func mergeDetails(old, fresh *abs.Item) (update *abs.MediaUpdate, differing []string) {
	o, f := old.Media.Metadata, fresh.Media.Metadata
	upd := abs.MetadataUpdate{}
	var names []string
	text := func(name, a, b string, dst **string) {
		if a != b {
			*dst, names = &a, append(names, name)
		}
	}
	text("title", o.Title, f.Title, &upd.Title)
	text("subtitle", o.Subtitle, f.Subtitle, &upd.Subtitle)
	text("year", o.PublishedYear.String(), f.PublishedYear.String(), &upd.PublishedYear)
	text("published date", o.PublishedDate, f.PublishedDate, &upd.PublishedDate)
	text("publisher", o.Publisher, f.Publisher, &upd.Publisher)
	text("description", o.Description, f.Description, &upd.Description)
	text("isbn", o.ISBN, f.ISBN, &upd.ISBN)
	text("asin", o.ASIN, f.ASIN, &upd.ASIN)
	text("language", o.Language, f.Language, &upd.Language)
	if o.Explicit != f.Explicit {
		upd.Explicit, names = &o.Explicit, append(names, "explicit")
	}
	if o.Abridged != f.Abridged {
		upd.Abridged, names = &o.Abridged, append(names, "abridged")
	}
	if !slices.Equal(o.Narrators, f.Narrators) {
		upd.Narrators, names = append([]string{}, o.Narrators...), append(names, "narrators")
	}
	if !slices.Equal(o.Genres, f.Genres) {
		upd.Genres, names = append([]string{}, o.Genres...), append(names, "genres")
	}
	media := abs.MediaUpdate{}
	if len(names) > 0 {
		media.Metadata = &upd
	}
	if !slices.Equal(old.Media.Tags, fresh.Media.Tags) {
		media.Tags, names = append([]string{}, old.Media.Tags...), append(names, "tags")
	}
	if len(names) > 0 {
		update = &media
	}

	// the authors and the series are named with the rest, and written apart
	// from it, in their order: see carryInOrder
	if !slices.Equal(authorNames(o.Authors), authorNames(f.Authors)) {
		names = append(names, "authors")
	}
	if !slices.Equal(o.SeriesDisplay(), f.SeriesDisplay()) {
		names = append(names, "series")
	}

	return update, names
}

func authorNames(refs []abs.NameRef) []string {
	names := make([]string, 0, len(refs))
	for _, a := range refs {
		names = append(names, a.Name)
	}

	return names
}

// carryInOrder ties a book to the old record's authors and series in the
// old record's order. The server lists them in the order they were tied, and
// an update leaves a tie that is already there where it is: sent the same
// ones another way round it changes nothing, which is how a scan that ties
// two series in one moment comes to list them either way. So the list is
// sent one longer each time: the first call unties all but the first, and
// each after it ties the next, last.
func (m *issuesMerger) carryInOrder(ctx context.Context, p *mergePlan) error {
	o, f := p.old.Media.Metadata, p.fresh.Media.Metadata
	if !slices.Equal(authorNames(o.Authors), authorNames(f.Authors)) {
		authors := make([]abs.NameRef, 0, len(o.Authors))
		for i := 0; i == 0 || i < len(o.Authors); i++ {
			if i < len(o.Authors) {
				authors = append(authors, abs.NameRef{Name: o.Authors[i].Name})
			}
			if _, err := m.client.UpdateMedia(ctx, p.fresh.ID, abs.MediaUpdate{Metadata: &abs.MetadataUpdate{Authors: authors}}); err != nil {
				return fmt.Errorf("the authors: %w", err)
			}
		}
	}
	if !slices.Equal(o.SeriesDisplay(), f.SeriesDisplay()) {
		series := make([]abs.SeriesRef, 0, len(o.Series))
		for i := 0; i == 0 || i < len(o.Series); i++ {
			if i < len(o.Series) {
				series = append(series, abs.SeriesRef{Name: o.Series[i].Name, Sequence: o.Series[i].Sequence})
			}
			if _, err := m.client.UpdateMedia(ctx, p.fresh.ID, abs.MediaUpdate{Metadata: &abs.MetadataUpdate{Series: series}}); err != nil {
				return fmt.Errorf("the series: %w", err)
			}
		}
	}

	return nil
}

// carry writes what a plan found onto the new record, the record first and
// the accounts after.
func (m *issuesMerger) carry(ctx context.Context, lib *abs.Library, p *mergePlan) error {
	if p.details != nil {
		if _, err := m.client.UpdateMedia(ctx, p.fresh.ID, *p.details); err != nil {
			return fmt.Errorf("the details: %w", err)
		}
	}
	if err := m.carryInOrder(ctx, p); err != nil {
		return err
	}
	if p.carry.Chapters {
		if _, err := m.client.SetChapters(ctx, p.fresh.ID, p.old.Media.Chapters); err != nil {
			return fmt.Errorf("the chapters: %w", err)
		}
	}
	if p.carry.Cover {
		if err := m.carryCover(ctx, p.old.ID, p.fresh.ID); err != nil {
			return fmt.Errorf("the cover: %w", err)
		}
	}
	for i := range p.collections {
		c := &p.collections[i]
		if _, err := m.client.AddBookToCollection(ctx, c.ID, p.fresh.ID); err != nil {
			return fmt.Errorf("the collection %q: %w", c.Name, err)
		}
		// where the old record is, not at the end where the server adds it
		order := make([]string, 0, len(c.Books)+1)
		for k := range c.Books {
			if order = append(order, c.Books[k].ID); c.Books[k].ID == p.old.ID {
				order = append(order, p.fresh.ID)
			}
		}
		if _, err := m.client.OrderCollection(ctx, c.ID, order); err != nil {
			return fmt.Errorf("the book's place in the collection %q: %w", c.Name, err)
		}
	}

	for _, pr := range p.progress {
		as, err := m.actingAs(ctx, pr.user)
		if err != nil {
			return err
		}
		if err := carryProgress(ctx, as, p.fresh.ID, &pr.was); err != nil {
			return fmt.Errorf("%s's progress: %w", pr.user.Username, err)
		}
	}
	for _, bm := range p.bookmarks {
		as, err := m.actingAs(ctx, bm.user)
		if err != nil {
			return err
		}
		for _, b := range bm.add {
			if _, err := as.CreateBookmark(ctx, p.fresh.ID, b.Time, b.Title); err != nil {
				return fmt.Errorf("%s's bookmark at %s: %w", bm.user.Username, fmtDuration(b.Time), err)
			}
		}
	}
	// a playlist is its account's alone, so each account's are read as it.
	// One that cannot be acted as is named in the answer; had it progress or
	// bookmarks here, the pair has already stopped above
	for i := range m.users {
		u := &m.users[i]
		as, err := m.actingAs(ctx, u)
		if err != nil {
			continue
		}
		playlists := p.playlists
		if u.ID != m.me.ID {
			if playlists, err = as.Playlists(ctx, lib.ID); err != nil {
				return fmt.Errorf("%s's playlists: %w", u.Username, err)
			}
		}
		for k := range playlists {
			if !holdsEntry(playlists[k].Items, p.old.ID) || holdsEntry(playlists[k].Items, p.fresh.ID) {
				continue
			}
			if _, err := as.AddItemToPlaylist(ctx, playlists[k].ID, abs.PlaylistEntry{LibraryItemID: p.fresh.ID}); err != nil {
				return fmt.Errorf("%s's playlist %q: %w", u.Username, playlists[k].Name, err)
			}
			// a playlist is an order to listen in: the book keeps its turn
			order := make([]abs.PlaylistEntry, 0, len(playlists[k].Items)+1)
			for _, e := range playlists[k].Items {
				if order = append(order, abs.PlaylistEntry{LibraryItemID: e.LibraryItemID, EpisodeID: e.EpisodeID}); e.LibraryItemID == p.old.ID && e.EpisodeID == "" {
					order = append(order, abs.PlaylistEntry{LibraryItemID: p.fresh.ID})
				}
			}
			if _, err := as.OrderPlaylist(ctx, playlists[k].ID, order); err != nil {
				return fmt.Errorf("the book's turn in %s's playlist %q: %w", u.Username, playlists[k].Name, err)
			}
		}
	}

	return nil
}

// carryProgress sets one account's progress on a record to what it was on
// another. Where the listener is goes first and that they finished after it:
// sent together, the server reads a new position on a finished book as the
// book begun again. The dates go with the last call, which is what keeps
// "finished last March" from becoming "finished today".
func carryProgress(ctx context.Context, as *abs.Client, itemID string, was *abs.MediaProgress) error {
	where := abs.ProgressUpdate{
		CurrentTime: &was.CurrentTime, Duration: &was.Duration, Progress: &was.Progress,
		HideFromContinueListening: &was.HideFromContinueListening,
	}
	if was.EbookLocation != "" {
		where.EbookLocation, where.EbookProgress = &was.EbookLocation, &was.EbookProgress
	}
	if err := as.SetProgress(ctx, itemID, "", where); err != nil {
		return err
	}

	// the server finishes a book for the listener within ten seconds of its
	// end, whatever is sent: unfinished there means unfinished here too
	finished := abs.ProgressUpdate{IsFinished: &was.IsFinished, HideFromContinueListening: &was.HideFromContinueListening}
	if was.IsFinished && was.FinishedAt > 0 {
		finished.FinishedAt = &was.FinishedAt
	}
	if was.LastUpdate > 0 {
		finished.LastUpdate = &was.LastUpdate
	}

	return as.SetProgress(ctx, itemID, "", finished)
}

// carryCover gives the new record the old record's cover when the two
// pictures differ.
func (m *issuesMerger) carryCover(ctx context.Context, oldID, freshID string) error {
	was, err := m.coverOf(ctx, oldID)
	if err != nil || was == nil {
		return err
	}
	has, err := m.coverOf(ctx, freshID)
	if err != nil || bytes.Equal(was, has) {
		return err
	}
	ext := ".jpg"
	switch http.DetectContentType(was) {
	case "image/png":
		ext = ".png"
	case "image/webp":
		ext = ".webp"
	}

	return m.client.UploadCover(ctx, freshID, "cover"+ext, bytes.NewReader(was))
}

// coverOf is a record's cover file, or nil when it has none or the file is
// gone with the folder it was in.
func (m *issuesMerger) coverOf(ctx context.Context, id string) ([]byte, error) {
	body, err := m.client.CoverFile(ctx, id)
	if errors.Is(err, abs.ErrNoCover) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()

	img, err := io.ReadAll(io.LimitReader(body, mergeCoverCap+1))
	if err != nil {
		return nil, err
	}
	if len(img) > mergeCoverCap {
		return nil, fmt.Errorf("the cover of %s is over %d MiB, more than is carried", id, mergeCoverCap>>20)
	}

	return img, nil
}

// actingAs is a client that writes as an account: the caller's own for the
// caller, and for anyone else one with a key made for the purpose, deleted
// when the call ends.
func (m *issuesMerger) actingAs(ctx context.Context, u *abs.User) (*abs.Client, error) {
	if u.ID == m.me.ID {
		return m.client, nil
	}
	refused := fmt.Errorf("the server will not let a key act as %s (an account that has never logged in, or is switched off), and only that account can write its progress, bookmarks and playlists", u.Username)
	if as, ok := m.acting[u.ID]; ok {
		if as == nil {
			return nil, refused
		}
		return as, nil
	}
	if m.acting == nil {
		m.acting = map[string]*abs.Client{}
	}
	m.acting[u.ID] = nil

	key, err := m.client.CreateAPIKey(ctx, mergeKeyName, u.ID, mergeKeyLife, true)
	if err != nil {
		return nil, fmt.Errorf("making a key to act as %s, whose progress, bookmarks and playlists only that account can write: %w", u.Username, err)
	}
	m.keys = append(m.keys, key.ID)
	as, err := abs.New(m.client.BaseURL(), key.Key)
	if err != nil {
		return nil, err
	}
	if _, err := as.Me(ctx); err != nil {
		if !abs.IsForbidden(err) {
			return nil, fmt.Errorf("acting as %s: %w", u.Username, err)
		}
		m.unreached = append(m.unreached, u.Username)
		return nil, refused
	}
	m.acting[u.ID] = as

	return as, nil
}
