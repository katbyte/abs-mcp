#!/usr/bin/env python3
"""Fetch the recordings item_compare_audio is calibrated on.

The by-ear check tells one recording from another by its loudness over time
and its spectrum at the aligned spot, and its thresholds have to come from
real speech, not made-up syllables. LibriVox holds several complete readings
of one public-domain text by different readers, which is the hardest case
the check meets - the same words, another voice - and the same readers on
other books. This fetches them, one 64 kbps mp3 per chapter as a library
would hold them, into a cache outside the repo, and writes a manifest the
calibration test reads. The same-recording copies - re-encoded, faster,
quieter, split - are made from these by the test itself with ffmpeg.

Everything fetched is in the public domain: the texts (Carroll died in 1898,
Stevenson in 1894) and the recordings, which LibriVox's volunteers release
into the public domain as the project's rule (librivox.org/pages/public-domain),
each archive.org page carrying the dedication in its metadata. Nothing is
copied into the repo. docs/CALIBRATION.md has the detail.

    scripts/calibration-fetch.py                 # into ~/.cache/abs-mcp/calibration
    scripts/calibration-fetch.py --dir /some/dir
    scripts/calibration-fetch.py --list          # say what would be fetched, fetch nothing
    scripts/calibration-fetch.py --manifest-only # write the manifest, fetch nothing
    scripts/calibration-fetch.py --only 4511,200  # fetch some of the recordings

Then: ABS_CALIBRATION_DIR=~/.cache/abs-mcp/calibration go test -tags calibration ./lib/audiosample/...
"""

import argparse
import json
import os
import sys
import time
import urllib.request

LIBRIVOX = "https://librivox.org/api/feed/audiobooks/?id={id}&format=json&extended=1"
METADATA = "https://archive.org/metadata/{item}"
DOWNLOAD = "https://archive.org/download/{item}/{name}"
AGENT = "abs-mcp-calibration (https://github.com/katbyte/abs-mcp)"

# LibriVox book ids. Alice's Adventures in Wonderland has more complete solo
# readings than any other text, and its readers recorded other books too.
RECORDINGS = [
    # one text, six readers, unabridged: every pair is two readings
    {"id": 900, "work": "alice", "kind": "solo", "reader": "Peter Yearsley"},
    {"id": 4139, "work": "alice", "kind": "solo", "reader": "Kara Shallenberg"},
    {"id": 4240, "work": "alice", "kind": "solo", "reader": "Eric Leach"},
    {"id": 13477, "work": "alice", "kind": "solo", "reader": "StudioMike"},
    {"id": 15200, "work": "alice", "kind": "solo", "reader": "Craig Franklin"},
    {"id": 16702, "work": "alice", "kind": "solo", "reader": "Vin Cramer"},
    # the same text cut: abridged readings, which the check should call
    # neither the same as a full reading nor the same as each other
    {"id": 1122, "work": "alice", "kind": "abridged", "reader": "Kirsten Ferreri"},
    {"id": 6898, "work": "alice", "kind": "abridged", "reader": "ashleighjane"},
    # a full cast
    {"id": 4511, "work": "alice", "kind": "cast", "reader": "full cast"},
    # the group reading of 2006: several readers, Kara Shallenberg among them
    # reading chapters she recorded again alone in 2010 - the same voice, the
    # same words, another recording
    {"id": 200, "work": "alice", "kind": "group", "reader": "various"},
    # the same reader on another book: her voice without her Alice
    {"id": 10043, "work": "treasure-island", "kind": "solo", "reader": "Kara Shallenberg"},
]


def fetch_json(url):
    req = urllib.request.Request(url, headers={"User-Agent": AGENT})
    for attempt in range(4):
        try:
            with urllib.request.urlopen(req, timeout=120) as r:  # noqa: S310 - https to two known hosts
                return json.load(r)
        except Exception as e:  # noqa: BLE001 - any failure is retried
            if attempt == 3:
                raise
            print(f"  {url}: {e}, retrying", file=sys.stderr)
            time.sleep(5 * (attempt + 1))
    return None


def download(url, dest, size):
    """Fetch url to dest unless dest is already there at the size archive.org gives.

    archive.org answers 500 now and then and recovers in a minute, so a file
    is tried for a while before it is given up on; the caller goes on to the
    next and reports what is missing at the end.
    """
    if os.path.exists(dest) and (size is None or os.path.getsize(dest) == size):
        return False
    req = urllib.request.Request(url, headers={"User-Agent": AGENT})
    tmp = dest + ".part"
    for attempt in range(8):
        try:
            with urllib.request.urlopen(req, timeout=300) as r, open(tmp, "wb") as f:  # noqa: S310
                while chunk := r.read(1 << 20):
                    f.write(chunk)
            os.replace(tmp, dest)
            return True
        except Exception as e:  # noqa: BLE001 - any failure is retried
            if attempt == 7:
                raise
            print(f"  {url}: {e}, retrying", file=sys.stderr)
            time.sleep(min(10 * 2**attempt, 120))
    return True


def seconds(length):
    """archive.org writes a file's length as seconds, or as mm:ss or h:mm:ss."""
    if not length:
        return 0.0
    if ":" not in str(length):
        return float(length)
    total = 0.0
    for part in str(length).split(":"):
        total = total * 60 + float(part)
    return total


def librivox(book_id):
    d = fetch_json(LIBRIVOX.format(id=book_id))
    books = d.get("books") or []
    if not books:
        raise SystemExit(f"LibriVox has no book {book_id}")
    return books[0]


def archive_item(url):
    # https://www.archive.org/details/<item> or https://archive.org/details/<item>
    return url.rstrip("/").rsplit("/", 1)[-1]


def describe(rec, root):
    """One recording's manifest entry: what LibriVox and archive.org say of it."""
    book = librivox(rec["id"])
    item = archive_item(book["url_iarchive"])
    meta = fetch_json(METADATA.format(item=item))
    files = [f for f in meta.get("files", []) if f.get("format") == "64Kbps MP3"]
    files.sort(key=lambda f: f["name"])
    if not files:
        raise SystemExit(f"{item} has no 64 kbps mp3s")
    # who read each section, from LibriVox: a group reading names a reader
    # per chapter
    sections = []
    for s in book.get("sections") or []:
        sections.append({
            "number": int(s.get("section_number") or len(sections) + 1),
            "title": s.get("title"),
            "readers": [r.get("display_name") for r in (s.get("readers") or [])],
            "seconds": int(s.get("playtime") or 0),
        })
    folder = os.path.join(root, rec["work"], f"{rec['id']}-{rec['kind']}")
    return {
        **rec,
        "title": book["title"],
        "archive": item,
        "folder": os.path.relpath(folder, root),
        "seconds": int(book.get("totaltimesecs") or 0),
        "sections": sections,
        "files": [{"name": f["name"], "seconds": seconds(f.get("length")), "bytes": int(f["size"])} for f in files],
    }


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--dir", default=os.path.expanduser("~/.cache/abs-mcp/calibration"))
    ap.add_argument("--list", action="store_true", help="say what would be fetched, fetch nothing")
    ap.add_argument("--manifest-only", action="store_true", help="write the manifest, fetch nothing")
    ap.add_argument("--only", help="LibriVox ids to fetch, comma-separated; the manifest still lists them all")
    args = ap.parse_args()
    only = {int(x) for x in args.only.split(",")} if args.only else None

    # what an earlier run learned from LibriVox and archive.org is kept, so a
    # rerun for the files one of them dropped does not need either to answer
    known = {}
    try:
        with open(os.path.join(args.dir, "manifest.json")) as f:
            known = {r["id"]: r for r in json.load(f)["recordings"]}
    except (OSError, ValueError, KeyError):
        pass

    manifest = {"recordings": []}
    total = 0
    to_fetch = []
    for rec in RECORDINGS:
        entry = known.get(rec["id"])
        if entry is None or entry.get("kind") != rec["kind"] or entry.get("work") != rec["work"]:
            entry = describe(rec, args.dir)
        folder = os.path.join(args.dir, entry["folder"])
        item = entry["archive"]
        files = entry["files"]
        manifest["recordings"].append(entry)
        size = sum(f["bytes"] for f in files)
        total += size
        print(f"{entry['title']} ({rec['reader']}): {len(files)} files, {size / 1e6:.0f} MB, {item}")
        if only is None or rec["id"] in only:
            to_fetch.append((item, folder, files))

    print(f"{len(manifest['recordings'])} recordings, {total / 1e6:.0f} MB")
    if args.list:
        return
    # the manifest first, so a partial fetch can be read as far as it got
    os.makedirs(args.dir, exist_ok=True)
    with open(os.path.join(args.dir, "manifest.json"), "w") as f:
        json.dump(manifest, f, indent=1)
    print(f"manifest: {os.path.join(args.dir, 'manifest.json')}")
    if args.manifest_only:
        return
    missing = []
    for item, folder, files in to_fetch:
        os.makedirs(folder, exist_ok=True)
        for f in files:
            try:
                if download(DOWNLOAD.format(item=item, name=f["name"]), os.path.join(folder, f["name"]), f["bytes"]):
                    print(f"  fetched {f['name']}")
            except Exception as e:  # noqa: BLE001 - one file failing does not stop the rest
                print(f"  {item}/{f['name']}: {e}", file=sys.stderr)
                missing.append(f"{item}/{f['name']}")
    if missing:
        print(f"{len(missing)} files could not be fetched; run again for them:", file=sys.stderr)
        for m in missing:
            print(f"  {m}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
