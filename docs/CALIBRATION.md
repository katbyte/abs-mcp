# Calibrating the by-ear check

`item_compare_audio` decides whether two books are one recording by listening: the rise and fall of the voice says where two copies line up and how alike their rhythm is, and the spectrum at that spot says whether it is the same voice. Its thresholds are not guesses. They are read off a corpus of real speech, and the test that reads them off runs again whenever the method changes.

## The corpus

The corpus is LibriVox's readings of *Alice's Adventures in Wonderland* by Lewis Carroll, with one reader's *Treasure Island* by Robert Louis Stevenson beside them. Alice has more complete solo readings on LibriVox than any other text, which makes it the hardest test the check can face: the same words, in six different voices.

| Recordings | Why they are here |
|---|---|
| six complete solo readings, six readers | every pair is the same words in another voice; the check must call each pair different |
| two abridged readings | the same text cut; neither the same as a full reading nor as each other |
| a full-cast dramatised reading | many voices on one text |
| the 2006 group reading, a reader per chapter | one of its readers recorded the whole book alone in 2010, so her chapter against her own later chapter is the same voice, the same words, another recording |
| the same reader on *Treasure Island* | her voice without her Alice |

From the first hour of two of the solo readings, the test makes eleven copies with ffmpeg, the ways a library collects one recording: mp3 at 32 kbps, one m4b in AAC, one Opus file, 3% faster with the pitch kept, 2% slower with the pitch dropped, 12 dB quieter, heavily compressed, phone-band at 8 kHz, pink noise mixed in, the first five minutes cut off, and every chapter split in two. Each copy must come out the same as its original, and copy against copy too.

## Copyright

Everything in the corpus is in the public domain.

- **The texts.** Carroll died in 1898 and Stevenson in 1894. Both books are out of copyright in every country.
- **The recordings.** LibriVox accepts only recordings its volunteers release into the public domain; that is the project's founding rule, stated at <https://librivox.org/pages/public-domain/>. Each recording's page on archive.org carries the dedication in its metadata, as the Creative Commons public-domain dedication or the Public Domain Mark. LibriVox adds that its recordings are public domain in the USA and that listeners elsewhere should check their own country; for texts this old there is nothing to check.

The repo holds none of the audio. `scripts/calibration-fetch.py` holds the LibriVox ids and fetches the files into a cache outside the repo, by default `~/.cache/abs-mcp/calibration`, and writes a manifest there naming every reader against their chapters. Nothing is copied into the repo or redistributed.

## Running it

```bash
scripts/calibration-fetch.py            # about 925 MB, once; run again if archive.org drops a file
ABS_CALIBRATION_DIR=~/.cache/abs-mcp/calibration go test -tags calibration -run Calibration -v -timeout 2h ./tools/
```

The test makes the copies on its first run, a few minutes of ffmpeg, then compares every pair and logs each one: both scores at every point, the speed it found, the verdict, and whether the verdict was right. It ends with each kind's distribution and the gap between them, and fails if any pair is judged wrong. `ABS_CALIBRATION_POINTS`, `_STRETCH`, `_REACH` and `_SPEED` try another configuration; `ABS_CALIBRATION_ONLY=same,different,abridged` runs some kinds, and `ABS_CALIBRATION_PARTIAL=1` leaves out a recording not all fetched yet.

## What it found

Run of 2026-10-03, every pair judged right.

| Pairs | Count | Envelope points | Envelope medians | Spectral points | Spectral medians | Found |
|---|---|---|---|---|---|---|
| the same recording: copies against their original and each other | 28 | 0.85 to 1.00 | 0.88 to 0.99 | 0.85 to 1.00 | 0.88 to 1.00 | 5 of 5, every pair |
| other readings of the same words, the full cast and the group among them, and another book | 41 | 0.30 to 0.66 | 0.33 to 0.50 | 0.12 to 0.53 | 0.19 to 0.42 | none, every pair |
| the two abridged readings against the rest and each other | 19 | 0.28 to 0.53 | 0.31 to 0.43 | 0.14 to 0.44 | 0.20 to 0.34 | none, every pair |

The thresholds sit in the gap with room on both sides: a stretch counts as found at an envelope score of 0.7 and a spectral score of 0.6, and a pair is the same recording when four in five stretches are found. The lowest same-recording scores belong to the copies with pink noise mixed in. The 3% faster and 2% slower copies were read at those speeds.

The cases built to break it:

- **The same reader twice.** Kara Shallenberg's chapter 7 in the 2006 group reading against her chapter 7 recorded alone in 2010: envelope median 0.40, spectral 0.30. The same voice saying the same words in another take is nowhere near one recording.
- **The same reader on another book.** Her Alice against her Treasure Island: envelope median 0.50, spectral 0.41, the highest of any pair that is not one recording, and still far under the line. A voice alone does not match; it has to be the same voice at the same moment.
- **The two readers closest in pace.** Shallenberg against Eric Leach reached 0.66 and 0.53 at one point in the middle of the book, the closest any other reading came, and nothing at the other four.
- **A copy with the first five minutes cut off** was still found at every point, as the search reaches ten minutes either side.
