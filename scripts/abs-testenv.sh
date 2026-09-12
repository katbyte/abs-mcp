#!/usr/bin/env bash
#
# Bring up a throwaway Audiobookshelf in Docker, seed it with a known catalogue,
# and print the environment the integration tests need.
#
#   eval "$(scripts/abs-testenv.sh up)"   # start, seed, export ABS_SERVER/ABS_TOKEN
#   scripts/abs-testenv.sh down           # stop and remove everything
#
# This script only does what abs-mcp cannot: run the container, create the root
# user, mint an API key, and lay the audio out on disk. The libraries, the scan
# and the metadata are all the integration tests' job, through library_create,
# library_scan and item_edit, so those tools are exercised rather than bypassed.
#
# The container is pointed at the record/replay proxy the tests run (see
# internal/providerproxy): Audiobookshelf, not abs-mcp, is what calls Audible,
# Audnexus and iTunes, so intercepting those calls has to happen at its edge.
# Disabling TLS verification is what lets the proxy present its own certificate
# for those hosts; this container is a throwaway that exists for one test run.
# EXP_PROXY_SUPPORT is Audiobookshelf's own flag for running behind a proxy: it
# turns off the SSRF request filter, which otherwise pins an explicit agent onto
# the feed and image fetches and bypasses HTTPS_PROXY entirely.

set -euo pipefail

NAME="${ABS_TEST_CONTAINER:-abs-mcp-test}"
IMAGE="${ABS_TEST_IMAGE:-ghcr.io/advplyr/audiobookshelf:latest}"
PORT="${ABS_TEST_PORT:-13378}"
# the port the tests' provider proxy listens on, reached from inside the
# container via host.docker.internal
PROXY_PORT="${ABS_TEST_PROXY_PORT:-18080}"
# not TMPDIR: on macOS that is /var/folders/..., which Docker Desktop does not
# share by default, and the bind mounts silently come up empty
DATA="${ABS_TEST_DATA:-${HOME}/.cache/abs-mcp/testenv}"
USERNAME="root"
PASSWORD="abs-mcp-integration"
URL="http://127.0.0.1:${PORT}"

log() { echo "==> $*" >&2; }

# api METHOD PATH [BODY] [TOKEN] - curl against the test server, failing loudly.
api() {
  local method=$1 path=$2 body=${3:-} token=${4:-}
  local args=(-fsS -X "$method" "${URL}${path}" -H 'Content-Type: application/json')
  [ -n "$token" ] && args+=(-H "Authorization: Bearer ${token}")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}"
}

# title|author - the on-disk layout only. Series, tags and genres are seeded by
# the integration tests through item_edit, and the scan is driven by
# library_scan, so those tools are exercised rather than bypassed.
FICTION='Foundation|Isaac Asimov
Foundation and Empire|Isaac Asimov
Second Foundation|Isaac Asimov
City of Golden Shadow|Tad Williams
Sea of Silver Light|Tad Williams
Leviathan Wakes|James S. A. Corey
Abaddon'\''s Gate|James S. A. Corey'

NONFICTION='The Arms of Krupp|William Manchester
A Brief History of Vice|Robert Evans
War Is a Racket|Smedley D. Butler'

PODCASTS="Well There's Your Problem
Behind the Bastards"

# silent_mp3 PATH - a one-second mp3 so the scanner has something to probe.
silent_mp3() {
  mkdir -p "$(dirname "$1")"
  # -nostdin matters: without it ffmpeg reads the while-read loop's stdin
  # looking for interactive keys and swallows a character of the next line,
  # which silently truncates the titles that follow
  ffmpeg -nostdin -loglevel error -y -f lavfi -i anullsrc=r=44100:cl=mono -t 1 -q:a 9 "$1"
}

fixtures() {
  log "generating audio fixtures under ${DATA}"
  rm -rf "${DATA}"

  # books are "<author>/<title>/"; the scanner takes author and title from the
  # path, and seed_metadata then sets everything the tests actually assert on
  while IFS='|' read -r title author; do
    [ -n "$title" ] && silent_mp3 "${DATA}/fiction/${author}/${title}/01.mp3"
  done <<<"$FICTION"

  while IFS='|' read -r title author; do
    [ -n "$title" ] && silent_mp3 "${DATA}/nonfiction/${author}/${title}/01.mp3"
  done <<<"$NONFICTION"

  # podcasts are "<podcast>/<episode file>"
  while read -r show; do
    [ -n "$show" ] || continue
    silent_mp3 "${DATA}/podcasts/${show}/Episode 1.mp3"
    silent_mp3 "${DATA}/podcasts/${show}/Episode 2.mp3"
  done <<<"$PODCASTS"

  mkdir -p "${DATA}/metadata" "${DATA}/config"
  chmod -R 777 "${DATA}"
}

# wait_for WHAT TRIES COMMAND
wait_for() {
  local what=$1 tries=$2 cmd=$3
  log "waiting for ${what}"
  for _ in $(seq "$tries"); do
    if eval "$cmd" >/dev/null 2>&1; then return 0; fi
    sleep 2
  done
  echo "timed out waiting for ${what}" >&2
  docker logs "$NAME" 2>&1 | tail -40 >&2
  return 1
}

up() {
  command -v ffmpeg >/dev/null || { echo "ffmpeg is required to generate fixtures" >&2; exit 1; }
  command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
  command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }

  down >/dev/null 2>&1 || true
  fixtures

  log "starting ${IMAGE} as ${NAME} on ${PORT} (providers proxied via host.docker.internal:${PROXY_PORT})"
  docker run -d --name "$NAME" \
    -p "${PORT}:80" \
    --add-host "host.docker.internal:host-gateway" \
    -e "HTTP_PROXY=http://host.docker.internal:${PROXY_PORT}" \
    -e "HTTPS_PROXY=http://host.docker.internal:${PROXY_PORT}" \
    -e "http_proxy=http://host.docker.internal:${PROXY_PORT}" \
    -e "https_proxy=http://host.docker.internal:${PROXY_PORT}" \
    -e "NO_PROXY=localhost,127.0.0.1" \
    -e "NODE_TLS_REJECT_UNAUTHORIZED=0" \
    -e "EXP_PROXY_SUPPORT=1" \
    -v "${DATA}/fiction:/fiction" \
    -v "${DATA}/nonfiction:/nonfiction" \
    -v "${DATA}/podcasts:/podcasts" \
    -v "${DATA}/metadata:/metadata" \
    -v "${DATA}/config:/config" \
    "$IMAGE" >/dev/null

  wait_for "the server to answer /status" 60 "curl -fsS ${URL}/status"

  log "creating the root user"
  api POST /init "$(jq -n --arg u "$USERNAME" --arg p "$PASSWORD" '{newRoot: {username: $u, password: $p}}')" >/dev/null

  log "logging in"
  local login access user_id
  login=$(curl -fsS -X POST "${URL}/login" \
    -H 'Content-Type: application/json' -H 'x-return-tokens: true' \
    -d "$(jq -n --arg u "$USERNAME" --arg p "$PASSWORD" '{username: $u, password: $p}')")
  access=$(echo "$login" | jq -r '.user.accessToken')
  user_id=$(echo "$login" | jq -r '.user.id')
  [ "$access" != "null" ] || { echo "no accessToken in the login response" >&2; exit 1; }

  # an API key is what abs-mcp is meant to run on, so the tests authenticate
  # through the same path as production rather than with a session token
  log "creating an API key"
  local key
  key=$(api POST /api/api-keys \
    "$(jq -n --arg u "$user_id" '{name: "abs-mcp-integration", userId: $u, isActive: true}')" \
    "$access" | jq -r '.apiKey.apiKey')
  [ "$key" != "null" ] || { echo "no apiKey in the response" >&2; exit 1; }

  # consumed with eval "$(scripts/abs-testenv.sh up)"
  echo "export ABS_SERVER='${URL}'"
  echo "export ABS_TOKEN='${key}'"
  # the tests create the libraries themselves with library_create, over these
  # server-side paths, and add a folder under ABS_TEST_DATA to prove
  # library_scan picks it up
  echo "export ABS_TEST_DATA='${DATA}'"
  echo "export ABS_TEST_PROXY_PORT='${PROXY_PORT}'"
}

down() {
  log "removing ${NAME}"
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  rm -rf "${DATA}"
}

case "${1:-up}" in
  up) up ;;
  down) down ;;
  fixtures) fixtures ;;  # generate the audio tree only, for inspecting the layout
  *) echo "usage: $0 [up|down|fixtures]" >&2; exit 1 ;;
esac
