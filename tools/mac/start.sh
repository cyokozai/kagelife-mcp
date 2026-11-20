#!/bin/sh
# start.sh [--mode human|ai|full] [--shaders DIR] [--kagelife PATH] [--cache DIR] [--config DIR]
#          [--no-nowplaying] [--no-llm-slots] [-- KAGELIFE_ARGS...]
# Start kagelife (window, -llm-slots llm_a,llm_b) in the background on this Mac, and nowplaying-spotify
# (a kagelife-mcp executable, or a Docker container) that writes <cache>/now_playing.json. stop.sh stops both.
# Does not start the Docker engine. Installs nothing. Exit: 0 started, 1 not started (reason on stderr), 2 usage.
set -u
here=$(cd "$(dirname "$0")" && pwd)
. "$here/lib.sh"

usage() {
  cat >&2 <<'EOF'
usage: start.sh [--mode human|ai|full] [--shaders DIR] [--kagelife PATH] [--cache DIR] [--config DIR]
                [--no-nowplaying] [--no-llm-slots] [-- KAGELIFE_ARGS...]
  --mode MODE      human: kagelife only, no -llm-slots (1-9 are all normal shaders)
                   ai:    kagelife -llm-slots llm_a,llm_b + nowplaying
                   full:  same as ai, and warns when kagelife-deck is not running
                   (writes <cache>/kagelife_mode; without --mode nothing is written)
  --shaders DIR    shader folder for kagelife -shaders (env KAGELIFE_SHADERS; required)
  --kagelife PATH  kagelife binary (env KAGELIFE_BIN; default: kagelife on PATH)
  --cache DIR      now_playing.json, kagelife.pid, kagelife.log (env KAGELIFE_CACHE; default ~/.cache/kagelife)
  --config DIR     folder with spotify-token.json from login (env KAGELIFE_CONFIG; default ~/.config/kagelife)
  --no-nowplaying  start kagelife only (no Spotify, no Docker); -llm-slots stays
  --no-llm-slots   start kagelife without -llm-slots
  after --          extra kagelife flags, e.g. -- -display 2 -fullscreen
env: KAGELIFE_SPOTIFY_CLIENT_ID (required unless nowplaying is off),
     KAGELIFE_NOWPLAYING_RUNNER (binary [default] | docker), KAGELIFE_MCP_BIN (default kagelife-mcp on PATH),
     KAGELIFE_NOWPLAYING_IMAGE, KAGELIFE_NOWPLAYING_NAME (docker runner), KAGELIFE_NOWPLAYING_INTERVAL, KAGELIFE_CONTROL_FILE (passed to kagelife)
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
runner=${KAGELIFE_NOWPLAYING_RUNNER:-binary}
mcp_bin=${KAGELIFE_MCP_BIN:-kagelife-mcp}
pgrep_cmd=${KAGELIFE_PGREP:-pgrep}
mode=
nowplaying=1
llm_slots=1
no_np_flag=0
no_llm_flag=0

need_arg() {
  [ "$2" -ge 2 ] || { echo "start.sh: $1 needs a value" >&2; usage; exit 2; }
}

while [ $# -gt 0 ]; do
  case $1 in
    --shaders) need_arg "$1" $#; shaders=$2; shift 2 ;;
    --kagelife) need_arg "$1" $#; bin=$2; shift 2 ;;
    --cache) need_arg "$1" $#; cache=$2; shift 2 ;;
    --config) need_arg "$1" $#; config=$2; shift 2 ;;
    --mode) need_arg "$1" $#; mode=$2; shift 2 ;;
    --no-nowplaying) no_np_flag=1; shift ;;
    --no-llm-slots) no_llm_flag=1; shift ;;
    -h|--help) usage; exit 0 ;;
    --) shift; break ;;
    *) echo "start.sh: unknown option: $1" >&2; usage; exit 2 ;;
  esac
done

case $mode in
  '') ;;
  human) nowplaying=0; llm_slots=0 ;;
  ai|full) ;;
  *) echo "start.sh: --mode must be human, ai or full (got: $mode)" >&2; usage; exit 2 ;;
esac
[ "$no_np_flag" = 0 ] || nowplaying=0
[ "$no_llm_flag" = 0 ] || llm_slots=0
case $runner in
  binary|docker) ;;
  *) die "start.sh: KAGELIFE_NOWPLAYING_RUNNER must be binary or docker (got: $runner)" ;;
esac

[ -n "$shaders" ] || die "start.sh: give the shader folder with --shaders DIR or KAGELIFE_SHADERS"
[ -d "$shaders" ] || die "start.sh: shader folder not found: $shaders"
command -v "$bin" >/dev/null 2>&1 || die "start.sh: kagelife not found: $bin (use --kagelife PATH or KAGELIFE_BIN)"

if [ "$nowplaying" = 1 ]; then
  [ -n "${KAGELIFE_SPOTIFY_CLIENT_ID:-}" ] ||
    die "start.sh: KAGELIFE_SPOTIFY_CLIENT_ID is not set. export it first (see tools/nowplaying-spotify/README.md), or use --no-nowplaying"
  [ -f "$config/spotify-token.json" ] ||
    die "start.sh: $config/spotify-token.json not found. Do the Spotify login first: kagelife-mcp nowplaying-spotify login --token $config/spotify-token.json"
  if [ "$runner" = binary ]; then
    command -v "$mcp_bin" >/dev/null 2>&1 ||
      die "start.sh: kagelife-mcp not found: $mcp_bin (set KAGELIFE_MCP_BIN, or KAGELIFE_NOWPLAYING_RUNNER=docker to use the Docker container)"
  fi
fi

pid_file=$cache/kagelife.pid
log_file=$cache/kagelife.log
np_pid_file=$cache/nowplaying.pid
np_log_file=$cache/nowplaying.log
mode_file=$cache/kagelife_mode

if [ -f "$pid_file" ]; then
  old=$(cat "$pid_file")
  if [ -n "$old" ] && alive "$old"; then
    die "start.sh: kagelife is already running (pid $old). Run stop.sh first"
  fi
  rm -f "$pid_file"
fi

if [ "$nowplaying" = 1 ] && [ "$runner" = binary ] && [ -f "$np_pid_file" ]; then
  old=$(cat "$np_pid_file")
  if [ -n "$old" ] && alive "$old"; then
    die "start.sh: nowplaying-spotify is already running (pid $old). Run stop.sh first"
  fi
  rm -f "$np_pid_file"
fi

if [ "$nowplaying" = 1 ] && [ "$runner" = docker ]; then
  "$docker" image inspect "$image" >/dev/null 2>&1 ||
    die "start.sh: image $image not found. Build it: docker build -t $image tools/nowplaying-spotify"
  if "$docker" container inspect "$name" >/dev/null 2>&1; then
    die "start.sh: container $name already exists. Run stop.sh first"
  fi
fi

mkdir -p "$cache" || die "start.sh: cannot create $cache"
rm -f "$mode_file"

slots_arg=
[ "$llm_slots" = 0 ] || slots_arg=1

# kagelife keeps running after this script exits; its output goes to the log.
if [ -n "$slots_arg" ]; then
  nohup "$bin" -shaders "$shaders" -llm-slots llm_a,llm_b "$@" >"$log_file" 2>&1 </dev/null &
else
  nohup "$bin" -shaders "$shaders" "$@" >"$log_file" 2>&1 </dev/null &
fi
pid=$!
sleep 1
if ! alive "$pid"; then
  echo "start.sh: kagelife exited at once. Last lines of $log_file:" >&2
  tail -n 20 "$log_file" >&2
  exit 1
fi
echo "$pid" >"$pid_file"
echo "kagelife: pid $pid, log $log_file"

if [ -n "$mode" ]; then
  printf '%s\n' "$mode" >"$mode_file"
  echo "mode: $mode"
  if [ "$mode" = full ] && ! "$pgrep_cmd" -x kagelife-deck >/dev/null 2>&1; then
    echo "start.sh: warning: kagelife-deck is not running (mode full)" >&2
  fi
fi

[ "$nowplaying" = 1 ] || exit 0

if [ "$runner" = binary ]; then
  # The Client ID reaches the process through the environment only, never the command line.
  nohup "$mcp_bin" nowplaying-spotify --token "$config/spotify-token.json" \
    --out "$cache/now_playing.json" --interval "$interval" >"$np_log_file" 2>&1 </dev/null &
  npid=$!
  sleep 1
  if ! alive "$npid"; then
    echo "start.sh: nowplaying-spotify exited at once. Last lines of $np_log_file:" >&2
    tail -n 20 "$np_log_file" >&2
    die "start.sh: kagelife is still running; stop.sh stops it"
  fi
  echo "$npid" >"$np_pid_file"
  echo "nowplaying-spotify: pid $npid writes $cache/now_playing.json, log $np_log_file"
  exit 0
fi

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
