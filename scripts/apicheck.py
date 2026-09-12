#!/usr/bin/env python3
"""Report how much of the Audiobookshelf API lib/abs covers.

Audiobookshelf publishes no OpenAPI spec and its public docs say outright that
they are unmaintained, so the route table in the server source is the only
reference there is. This reads it, reads the paths lib/abs builds, and prints
what is covered, what is deliberately out of scope, and what is simply missing.

    scripts/apicheck.py              # against the pinned ABS version
    scripts/apicheck.py --list       # also list every uncovered route
    scripts/apicheck.py --ref master # check against a different ABS ref
"""

import argparse
import collections
import glob
import re
import sys
import urllib.request

ROUTER = "https://raw.githubusercontent.com/advplyr/audiobookshelf/{ref}/server/routers/ApiRouter.js"

# Deliberately not wrapped, per docs/ROADMAP.md. These are not gaps: streaming,
# byte delivery, uploads and server administration are outside what an AI needs
# to curate a library.
OUT_OF_SCOPE = {
    "/api/notifications": "notifications",
    "/api/notificationdata": "notifications",
    "/api/emails": "email / send-to-ereader",
    "/api/me/ereader-devices": "email / send-to-ereader",
    "/api/cache": "cache purges",
    "/api/filesystem": "filesystem browser",
    "/api/api-keys": "API key management",
    "/api/auth-settings": "auth settings",
    "/api/settings": "server settings",
    "/api/share": "media item sharing",
    "/api/upload": "uploads",
    "/api/backups/upload": "uploads",
    "/api/session": "playback sessions",
    "/api/sessions": "playback sessions",
    "/api/me/sessions": "playback sessions",
    "/api/custom-metadata-providers": "custom metadata providers",
    "/api/logger-data": "logs",
    "/api/watcher": "watcher",
    "/api/validate-cron": "cron validation",
    "/api/sorting-prefixes": "sorting prefixes",
    "/api/authorize": "auth",
    "/api/me/password": "auth",
    "/api/users/:id/openid-unlink": "auth",
    "/api/backups/:id/apply": "restore (would replace the database)",
}

# Routes that only move bytes: covers, downloads, probes, ebook files, opml.
BYTES = re.compile(r"/(cover|image|download|ffprobe|file|ebook|opml|play)(/|$)")


def abs_routes(ref):
    url = ROUTER.format(ref=ref)
    with urllib.request.urlopen(url, timeout=30) as r:  # noqa: S310 - a pinned https url
        src = r.read().decode()
    routes = set()
    for m in re.finditer(r"router\.(get|post|patch|delete|put)\(\s*'([^']+)'", src):
        routes.add((m.group(1).upper(), "/api" + m.group(2)))
    if not routes:
        sys.exit(f"apicheck: no routes found at {url}; did the file move?")
    return routes


def build_path(expr):
    """"/api/items/" + url.PathEscape(id) + "/cover"  ->  /api/items/:id/cover"""
    parts = []
    for tok in re.split(r"\s*\+\s*", expr.strip()):
        tok = tok.strip()
        parts.append(tok[1:-1] if tok.startswith('"') and tok.endswith('"') else ":id")
    return re.sub(r"/+", "/", "".join(parts)).rstrip("/") or "/"


CALL = r'((?:"[^"]*"|[\w.()]+)(?:\s*\+\s*(?:"[^"]*"|[\w.()]+))*)'
VERBS = {"get": "GET", "post": "POST", "patch": "PATCH", "del": "DELETE"}


def our_paths():
    ours = set()
    for f in glob.glob("lib/abs/*.go"):
        if f.endswith("_test.go"):
            continue
        s = open(f).read()
        for m in re.finditer(r"c\.(get|post|patch|del)\(ctx,\s*" + CALL + r"\s*,", s):
            ours.add((VERBS[m.group(1)], build_path(m.group(2))))
        for m in re.finditer(r"c\.do(?:Raw)?\(ctx,\s*http\.Method(\w+),\s*" + CALL + r"\s*,", s):
            ours.add((m.group(1).upper(), build_path(m.group(2))))
        # paths assembled into a variable first, or handed to an internal
        # helper: the verb is not visible at the call site, so any /api/ path
        # expression in the package counts as touched. This can only produce a
        # false "covered", never a false gap, and the package is ours.
        for m in re.finditer(r'("/api/[^"]*"(?:\s*\+\s*(?:"[^"]*"|[\w.()]+))*)', s):
            ours.add(("*", build_path(m.group(1))))
    return {(v, re.sub(r":\w+", ":id", p)) for v, p in ours}


def variants(path):
    """A route with trailing optional params also matches without them."""
    p = re.sub(r":\w+\?", "\x00", path)
    p = re.sub(r":\w+", ":id", p)
    out, cur = set(), p
    out.add(cur.replace("\x00", ":id"))
    while cur.endswith("/\x00"):
        cur = cur[:-2]
        out.add(cur.replace("\x00", ":id"))
    return {x.rstrip("/") or "/" for x in out}


def classify(verb, path):
    for prefix, label in OUT_OF_SCOPE.items():
        if path == prefix or path.startswith(prefix + "/"):
            return label
    if BYTES.search(path):
        return "byte delivery / playback"
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ref", default="master", help="Audiobookshelf git ref to check against")
    ap.add_argument("--list", action="store_true", help="list every uncovered route")
    args = ap.parse_args()

    routes = abs_routes(args.ref)
    ours = our_paths()
    any_verb = {p for v, p in ours if v == "*"}

    covered, uncovered = [], []
    for verb, path in sorted(routes, key=lambda r: (r[1], r[0])):
        hit = any((verb, x) in ours or x in any_verb for x in variants(path))
        (covered if hit else uncovered).append((verb, path))

    excluded = collections.defaultdict(list)
    gaps = []
    for verb, path in uncovered:
        label = classify(verb, path)
        (excluded[label].append((verb, path)) if label else gaps.append((verb, path)))

    in_scope = len(routes) - sum(len(v) for v in excluded.values())
    print(f"Audiobookshelf {args.ref}: {len(routes)} routes")
    print(f"  covered by lib/abs   {len(covered):3}   {round(100 * len(covered) / len(routes))}% of all, "
          f"{round(100 * len(covered) / in_scope)}% of the {in_scope} in scope")
    print(f"  out of scope         {sum(len(v) for v in excluded.values()):3}   {len(excluded)} categories, see docs/ROADMAP.md")
    print(f"  not implemented      {len(gaps):3}")

    if args.list and gaps:
        print("\nnot implemented:")
        for verb, path in gaps:
            print(f"  {verb:6} {path}")
    if args.list and excluded:
        print("\nout of scope:")
        for label in sorted(excluded, key=lambda k: -len(excluded[k])):
            print(f"  {len(excluded[label]):3}  {label}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
