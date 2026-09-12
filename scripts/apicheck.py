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
import glob
import re
import sys
import urllib.request

ROUTER = "https://raw.githubusercontent.com/advplyr/audiobookshelf/{ref}/server/routers/ApiRouter.js"

# lib/abs is a general Audiobookshelf client, so it covers the whole API. The
# "not wrapping, ever" list in docs/ROADMAP.md is about which endpoints get an
# MCP *tool* - an AI curating a library has no business changing auth settings
# or streaming audio - which is a separate question from what the SDK exposes.


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

    print(f"Audiobookshelf {args.ref}: {len(routes)} routes")
    print(f"  covered by lib/abs   {len(covered):3}   {round(100 * len(covered) / len(routes))}%")
    print(f"  not implemented      {len(uncovered):3}")

    if uncovered and args.list:
        print("\nnot implemented:")
        for verb, path in uncovered:
            print(f"  {verb:6} {path}")

    # a gap is a regression: the client is meant to cover the whole API
    return 1 if uncovered else 0


if __name__ == "__main__":
    sys.exit(main())
