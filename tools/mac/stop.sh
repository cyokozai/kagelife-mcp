#!/bin/sh
# stop.sh [--cache DIR] [--no-nowplaying]
# Stop what start.sh started: kagelife (<cache>/kagelife.pid), nowplaying-spotify (<cache>/nowplaying.pid
# or the Docker container), and remove <cache>/kagelife_mode.
# Doing nothing is fine when nothing runs. Exit: 0 done, 1 kagelife would not stop, 2 usage.
set -u
here=$(cd "$(dirname "$0")" && pwd)
. "$here/lib.sh"

usage() {
  cat >&2 <<'EOF'
usage: stop.sh [--cache DIR] [--no-nowplaying]
  --cache DIR      folder holding kagelife.pid (env KAGELIFE_CACHE; default ~/.cache/kagelife)
  --no-nowplaying  leave nowplaying-spotify (process and Docker) alone
env: KAGELIFE_NOWPLAYING_NAME (container name; default kagelife-nowplaying-spotify)
EOF
}

cache=${KAGELIFE_CACHE:-$HOME/.cache/kagelife}
docker=${KAGELIFE_DOCKER:-docker}
name=${KAGELIFE_NOWPLAYING_NAME:-kagelife-nowplaying-spotify}
nowplaying=1

while [ $# -gt 0 ]; do
  case $1 in
    --cache) [ $# -ge 2 ] || { usage; exit 2; }; cache=$2; shift 2 ;;
    --no-nowplaying) nowplaying=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "stop.sh: unknown option: $1" >&2; usage; exit 2 ;;
  esac
done

rc=0

# stop_pid LABEL PIDFILE: TERM, wait up to 5 s, drop the pid file when it is gone.
stop_pid() {
  [ -f "$2" ] || return 0
  pid=$(cat "$2")
  if [ -n "$pid" ] && alive "$pid"; then
    kill -TERM "$pid" 2>/dev/null
    i=0
    while alive "$pid" && [ $i -lt 50 ]; do
      sleep 0.1
      i=$((i + 1))
    done
    if alive "$pid"; then
      echo "stop.sh: $1 (pid $pid) did not stop in 5 s; try: kill -KILL $pid" >&2
      rc=1
      return 0
    fi
    echo "$1: stopped (pid $pid)"
  fi
  rm -f "$2"
}

stop_pid kagelife "$cache/kagelife.pid"
[ "$nowplaying" = 0 ] || stop_pid nowplaying-spotify "$cache/nowplaying.pid"
rm -f "$cache/kagelife_mode"

if [ "$nowplaying" = 1 ] && "$docker" container inspect "$name" >/dev/null 2>&1; then
  "$docker" stop "$name" >/dev/null 2>&1
  "$docker" rm "$name" >/dev/null 2>&1 && echo "nowplaying-spotify: removed container $name"
fi
exit $rc
