#!/bin/sh
# start.sh [--shaders DIR] [--kagelife PATH] [--cache DIR] [--config DIR] [--no-nowplaying] [-- KAGELIFE_ARGS...]
# Start kagelife (window, -llm-slots llm_a,llm_b) in the background on this Mac, and the
# nowplaying-spotify container that writes <cache>/now_playing.json. stop.sh stops both.
# Installs nothing. Exit: 0 started, 1 not started (reason on stderr), 2 usage.
set -u
here=$(cd "$(dirname "$0")" && pwd)
. "$here/lib.sh"

usage() {
  cat >&2 <<'EOF'
usage: start.sh [--shaders DIR] [--kagelife PATH] [--cache DIR] [--config DIR] [--no-nowplaying] [-- KAGELIFE_ARGS...]
  --shaders DIR    shader folder for kagelife -shaders (env KAGELIFE_SHADERS; required)
  --kagelife PATH  kagelife binary (env KAGELIFE_BIN; default: kagelife on PATH)
  --cache DIR      now_playing.json, kagelife.pid, kagelife.log (env KAGELIFE_CACHE; default ~/.cache/kagelife)
  --config DIR     folder with spotify-token.json from login (env KAGELIFE_CONFIG; default ~/.config/kagelife)
  --no-nowplaying  start kagelife only (no Spotify, no Docker)
  after --          extra kagelife flags, e.g. -- -display 2 -fullscreen
env: KAGELIFE_SPOTIFY_CLIENT_ID (required unless --no-nowplaying), KAGELIFE_NOWPLAYING_IMAGE,
     KAGELIFE_NOWPLAYING_NAME, KAGELIFE_NOWPLAYING_INTERVAL, KAGELIFE_CONTROL_FILE (passed to kagelife)
EOF
}

bin=${KAGELIFE_BIN:-kagelife}
shaders=${KAGELIFE_SHADERS:-}
cache=${KAGELIFE_CACHE:-$HOME/.cache/kagelife}
config=${KAGELIFE_CONFIG:-$HOME/.config/kagelife}
docker=${KAGELIFE_DOCKER:-docker}
image=${KAGELIFE_NOWPLAYING_IMAGE:-kagelife-nowplaying-spotify}
name=${KAGELIFE_NOWPLAYING_NAME:-kagelife-nowplaying-spotify}
interval=${KAGELIFE_NOWPLAYING_INTERVAL:-3}
check=${KAGELIFE_START_CHECK:-3}
nowplaying=1

need_arg() {
  [ "$2" -ge 2 ] || { echo "start.sh: $1 needs a value" >&2; usage; exit 2; }
}

while [ $# -gt 0 ]; do
  case $1 in
    --shaders) need_arg "$1" $#; shaders=$2; shift 2 ;;
    --kagelife) need_arg "$1" $#; bin=$2; shift 2 ;;
    --cache) need_arg "$1" $#; cache=$2; shift 2 ;;
    --config) need_arg "$1" $#; config=$2; shift 2 ;;
    --no-nowplaying) nowplaying=0; shift ;;
    -h|--help) usage; exit 0 ;;
    --) shift; break ;;
    *) echo "start.sh: unknown option: $1" >&2; usage; exit 2 ;;
  esac
done

[ -n "$shaders" ] || die "start.sh: give the shader folder with --shaders DIR or KAGELIFE_SHADERS"
[ -d "$shaders" ] || die "start.sh: shader folder not found: $shaders"
command -v "$bin" >/dev/null 2>&1 || die "start.sh: kagelife not found: $bin (use --kagelife PATH or KAGELIFE_BIN)"

if [ "$nowplaying" = 1 ]; then
  [ -n "${KAGELIFE_SPOTIFY_CLIENT_ID:-}" ] ||
    die "start.sh: KAGELIFE_SPOTIFY_CLIENT_ID is not set. export it first (see tools/nowplaying-spotify/README.md), or use --no-nowplaying"
  [ -d "$config" ] ||
    die "start.sh: $config not found. Do the Spotify login first (tools/nowplaying-spotify/README.md)"
fi

pid_file=$cache/kagelife.pid
log_file=$cache/kagelife.log

if [ -f "$pid_file" ]; then
  old=$(cat "$pid_file")
  if [ -n "$old" ] && alive "$old"; then
    die "start.sh: kagelife is already running (pid $old). Run stop.sh first"
  fi
  rm -f "$pid_file"
fi

if [ "$nowplaying" = 1 ]; then
  "$docker" image inspect "$image" >/dev/null 2>&1 ||
    die "start.sh: image $image not found. Build it: docker build -t $image tools/nowplaying-spotify"
  if "$docker" container inspect "$name" >/dev/null 2>&1; then
    die "start.sh: container $name already exists. Run stop.sh first"
  fi
fi

mkdir -p "$cache" || die "start.sh: cannot create $cache"

# kagelife keeps running after this script exits; its output goes to the log.
nohup "$bin" -shaders "$shaders" -llm-slots llm_a,llm_b "$@" >"$log_file" 2>&1 </dev/null &
pid=$!
sleep 1
if ! alive "$pid"; then
  echo "start.sh: kagelife exited at once. Last lines of $log_file:" >&2
  tail -n 20 "$log_file" >&2
  exit 1
fi
echo "$pid" >"$pid_file"
echo "kagelife: pid $pid, log $log_file"

[ "$nowplaying" = 1 ] || exit 0

# Pass the Client ID by name only (-e NAME), so its value is never on a command line or printed.
if ! "$docker" run -d --name "$name" -e KAGELIFE_SPOTIFY_CLIENT_ID \
  -v "$config:/config" -v "$cache:/out" "$image" \
  --token /config/spotify-token.json --out /out/now_playing.json --interval "$interval" >/dev/null; then
  die "start.sh: docker run failed. kagelife is still running; stop.sh stops it"
fi

if [ "$check" -gt 0 ]; then
  sleep "$check"
  running=$("$docker" container inspect -f '{{.State.Running}}' "$name" 2>/dev/null)
  if [ "$running" != true ]; then
    echo "start.sh: nowplaying-spotify stopped at once. Its log:" >&2
    "$docker" logs --tail 20 "$name" >&2
    die "start.sh: kagelife is still running; stop.sh stops both"
  fi
fi
echo "nowplaying-spotify: container $name writes $cache/now_playing.json (docker logs -f $name)"
