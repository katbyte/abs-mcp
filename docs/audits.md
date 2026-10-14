# The audits

Each audit sweeps the whole library for one kind of mistake and returns a worklist. This page holds the full rules for each one. The short table is in the [README](../README.md#the-audits).

## audit_all

Runs every audit in one call and returns only the counts, so one call says where a library needs work. Start here after a scan.

- `deep` adds the five audits that fetch something for every item.
- `deep` also counts `audit_chapters` and `audit_whitespace` whole.

## audit_unmatched

Books never matched to a metadata provider. They have no asin and no isbn, so nothing else can be filled in automatically.

- A book tagged `zz-provider:none` was checked and found on no provider. It is not reported.

Fix with `item_match`, then `item_match_apply`.

## audit_missing

Items with one metadata field left empty.

- `field` is cover, description, narrator, series, author, genres, year, publisher, language or chapters.
- For description it also catches a stub, a credit line such as "Read by Paul Heck", or a bare url.

Fix with `item_match_apply`, `item_cover_search` and `item_cover_edit`, `item_chapters_set`, or `item_edit`.

## audit_issues

Items whose folder is missing from disk or holds no playable media. These are broken records, not metadata gaps.

Fix with `library_issues_merge` when the folder was renamed or moved, or `library_issues_remove` when it is gone.

## audit_no_audio

Items with no audio tracks at all. This is usually an ebook-only folder that landed in an audiobook library.

## audit_path

Items whose folder name disagrees with their record. Subtitles, series prefixes and edition markers are set aside first.

- **Title or author.** A wrong match, a chapter tag left as the title, a pen name, or a book filed under another author.
- **Year.** A book dated earlier than the year its folder carries: `Dune (1965)` holding a book dated 1959. A recording is never older than the book.
- **Series.** A book the record places in a series whose folder does not say so: `Salvation Lost` for book 2 of the Salvation Sequence, `Ringworld` for book 1 of Ringworld. This is one way of filing, and `--audit-skip path-series` leaves it out.
- With `files`, the audio filenames are compared too.

Fix with `item_edit`.

## audit_chapters

Everything wrong with a book's chapters.

- Chapters that start past the end of the audio. A file taken out of a book leaves them behind.
- Chapters out of order.
- Chapters that end well before the audio does. A track was added.
- One chapter over a long book. It is as unnavigable as none, but invisible to `audit_missing`.

Fix with `item_chapters_set`. With `fit=true` it fits a book's own chapters back to its audio.

## audit_duplicates

Items that appear to be the same work.

**Groups.** Copies are joined by any key they share: asin, isbn, or title and author. So a matched copy and an unmatched copy of the same book are one group. Two different asins are never joined.

**What keeps copies apart.** A title, or an isbn, groups no copies that anything tells apart. The isbn is the print edition's, which every recording of it carries. A false group costs a deletion and a false split only a look. Copies are kept apart by:

- readers sharing no name. A reader comes from the narrator field or from a folder's brackets, `(Weiner)` or `[John Lee]`, unless the brackets hold a year, an edition, a format, a source, a language, a note on the copy or the author
- a number in their names: `Vol. 17`, `CD1`, `Book One`
- a place in one series: `Honor - 01 - Above All` and `Honor - 02 - Honor Bound`, both titled `Honor`
- folders naming different books
- one copy holding no audio
- lengths more than 15% apart

A title that over 50 copies share is a placeholder and joins none.

**`split`.** Nothing is dropped. `split` lists every title and isbn whose copies were kept apart, with how many copies, the first twenty parts and why. So a part that proves to be one recording is still in sight.

**`candidates`.** Pairs no key joins that are probably one recording:

- an edition label on the same audio: `Ender's Game (20th Anniversary)`
- the same title filed under another author
- one copy's album tag naming the other
- split files beside a single file
- one title's copies kept apart only by their folders, naming no different readers, within 4%: `Treason` and `A Planet Called Treason`

Confirm each with `item_compare_audio`.

**`incomplete`.** A copy that shares a title, an asin or an isbn with another copy, is more than 15% shorter, and holds only some of its tracks in play order. It is not a second copy but a bad one. It is reported apart from the groups, with the first five files it lacks.

## audit_abridged

Books that are probably abridged but not marked so. There are two signs:

- the book is the length of the store's abridged edition, far shorter than every unabridged one
- for two readings of one book in the library, chapter lengths are cut unevenly, where two unabridged readings keep a steady ratio

**`shorter_work`.** A book under an hour long and under a fifth of the shortest unabridged edition, when the store sells no abridged edition under twice its length. It is probably a story sharing a collection's title, and not abridged.

**Productions.** Productions are set aside on both sides: dramatised, full cast, a radio or audio drama, a play or theatre, a Hörspiel. The word is looked for in the names or the reader credit, and does not count if they also say unabridged. A copy the length of the store's production is that production.

It makes one store search per book, so it pages, and `audit_all` runs it only with `deep`.

Fix with `item_edit`.

## audit_series

Everything wrong with series.

- **Gaps.** A book missing between the lowest and highest number the library has. Interior gaps only. Each comes with the unlinked book on the shelf that fills it, when there is one, and with what is still missing once two spellings are read as one series.
- **Two spellings.** Two series that are one series spelled two ways, `The Chronicles of Amber` and `Chronicles of Amber`, each with its author. Fix with `series_merge`.
- **Numbers.** A book whose folder says ` - 1 - ` while its series says #4, a book whose folder puts it in a series it is not linked to, or two titles at one number.
- **Names.** Names that are not series names: ending in the word Series, called what the author is called, carrying a book number, or with a stray ™ or a space before a colon.
- **Titles.** Titles that are the series name with a number, or that carry it beside the real title, where the folder says what the title is.
- **Folders.** One series' folders written two ways on disk: `Pandora's Star` beside `Commonwealth Saga - 02 - Judas Unchained`, a double space, `- 3 -` beside `- 03 -`. Each comes with the folder name that would match.
- With `articles=true`, the names that open with The, A or An.

Fix with `series_merge`, `series_edit` or `item_edit`.

## audit_spelling

The same value spelled several ways across genres, tags, languages and publishers.

- `Sci-Fi` and `sci fi`
- `en`, `eng` and `English`
- `HarperAudio` and `Harper Audio`
- spellings a letter apart, such as `Romance` and `Romances`

Fix with `metadata_rename`.

## audit_authors

Everything wrong with authors.

- A book whose author field holds its title.
- Records never matched, with no photo, or with no books.
- Records whose biography opens with someone else's name. That is a wrong match: `Sarah Diemer` carrying `Sarah Miller began writing...`.
- Records that are one name spelled two ways: `C Z Dunn` and `Christian Dunn`.

Fix with `author_match`, `author_edit` or `item_edit`.

## audit_narrators

Everything wrong with the narrator field.

- **Roles.** Names that wrote some books and read others. One volume written and the rest of the series read is author and narrator swapped on import. One written and many read is a narrator credited as co-author.
- **Spelling.** The spelling checks, run on narrators: `Read by Jim Dale`, `Narrator....Jim Dale`, `Fajer Al` beside `Fajer Al-Kaisi`, two names in one value, a stray `Ph.D.`.

Fix with `item_edit` or `metadata_rename`.

## audit_genres

Genres and tags held to one rule: genres broad and few, tags fine and many.

- Placeholders like `Audiobook` in either field.
- A category path written as one value, `Science Fiction & Fantasy, Fantasy`, shown with its parts.
- Genres on too few books to be a genre.
- Tags that repeat the book's genre.
- Books with no genre.

Each finding comes with the `metadata_rename` call that fixes it.

## audit_whitespace

Spaces where a name should not have them.

**What counts.**

- a double space
- a space at either end, or before the extension: `End Credits .m4b`
- a space before a colon or the look-alike `꞉`
- a tab or a non-breaking space

The ideographic space Japanese titles use is left alone.

**Where it looks.**

- titles and subtitles
- author, narrator and series names. These are read from the library's own lists, so `Jane Doe, Ph.D.` stays whole
- every folder of the path, disc folders too
- every file in a book's folder

**What a row shows.** The spaces, and the name put right. Beside a folder or file it shows the item's title, since a double space often marks a dropped colon. For an author or series it shows the record id to fix it by. On a folder or file row, `taken` lists any suggestion already used beside it, which a plain rename would overwrite.

Fix with `item_edit`, `author_edit`, `series_edit` or `metadata_rename`.

## audit_unembedded

Books whose audio files do not carry the library's metadata in their tags. They were never embedded, or have gone stale since the last edit.

Fix with `item_embed_metadata`.

## audit_unplayable

Books with audio that will not play, though the server lists them like any other.

- Files locked to a store's player. An iTunes FairPlay book reads as plain AAC everywhere but its container.
- A book whose only audio is the locked Audible original.
- Files the server could not read.
- Files cut short by a download or copy that broke off.
- Marked `plays`: files whose extension names another format, such as an `.m4b` holding mp3.

It reads the start of each file that can be locked, a few kilobytes by byte range, so `audit_all` runs it only with `deep`.

`decode` also plays five seconds at the start, middle and end of every file to find damage partway. That needs ffmpeg.

## audit_covers

Everything wrong with cover art.

- Missing, not square, or too small. This reads every cover's header, so `audit_all` runs it only with `deep`.
- With `banner=true`, the "Only from Audible" ribbon across the bottom-right corner, found by colour and angle.
- With `store=true`, each cover is compared by perceptual hash with the store the book was matched to. The same picture bigger is an `upgrade`, with the full-size url. Another picture is `differs`, with both images to look at. It checks a window of matched books per call, paged by offset.

Fix with `item_cover_search`, `item_cover_edit` or `item_cover_upgrade`.

## audit_matched

Matched books whose asin is not the recording on disk.

- The provider's record for the asin is compared on title, duration and narrator. So a match applied by title alone shows up as `duration_off` or `narrator_differs`.
- With `fields`, every field is compared and the differences are listed with both values. That is what `override_details` would change.

It makes one provider request per book, so it pages, and `audit_all` runs it only with `deep`.

Fix with `item_match`, then `item_match_apply`.

## audit_podcasts

Podcasts that need a look. Each finding names its problem.

- `no_episodes`: nothing downloaded.
- `stale_feed`: the newest episode is over 90 days old, the feed was never checked, or there is no feed url. The show ended, or the feed url is dead.

Fix with `podcast_feed_episodes` and `podcast_episode_download`, or `podcast_check_new`.
