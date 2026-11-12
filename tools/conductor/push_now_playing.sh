#!/bin/sh
# push_now_playing.sh [--once] [host]   (Mac side; default host vj)
# Push ~/.cache/kagelife/now_playing.json to the same place on the VJ machine when it changes.
# See push_now_playing.py. Exit: 0 ok, 1 --once failed, 2 usage.
here=$(cd "$(dirname "$0")" && pwd)
exec python3 "$here/push_now_playing.py" "$@"
