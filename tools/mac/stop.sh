#!/bin/sh
# stop.sh [--cache DIR] [--no-nowplaying]
# Stop what start.sh started: kagelife (from <cache>/kagelife.pid) and the nowplaying-spotify container.
# Doing nothing is fine when nothing runs. Exit: 0 done, 1 kagelife would not stop, 2 usage.
set -u
here=$(cd "$(dirname "$0")" && pwd)
. "$here/lib.sh"

usage() {
  cat >&2 <<'EOF'
usage: stop.sh [--cache DIR] [--no-nowplaying]
  --cache DIR      folder holding kagelife.pid (env KAGELIFE_CACHE; default ~/.cache/kagelife)
  --no-nowplaying  leave Docker alone
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
pid_file=$cache/kagelife.pid
if [ -f "$pid_file" ]; then
  pid=$(cat "$pid_file")
  if [ -n "$pid" ] && alive "$pid"; then
    kill -TERM "$pid" 2>/dev/null
    i=0
    while alive "$pid" && [ $i -lt 50 ]; do
      sleep 0.1
      i=$((i + 1))
    done
    if alive "$pid"; then
      echo "stop.sh: kagelife (pid $pid) did not stop in 5 s; try: kill -KILL $pid" >&2
      rc=1
    else
      echo "kagelife: stopped (pid $pid)"
    fi
  fi
  [ $rc -ne 0 ] || rm -f "$pid_file"
fi

if [ "$nowplaying" = 1 ] && "$docker" container inspect "$name" >/dev/null 2>&1; then
  "$docker" stop "$name" >/dev/null 2>&1
  "$docker" rm "$name" >/dev/null 2>&1 && echo "nowplaying-spotify: removed container $name"
fi
exit $rc
