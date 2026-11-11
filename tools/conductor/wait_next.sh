#!/bin/sh
# wait_next.sh [--dir DIR] [--interval SEC] [--timeout SEC] [--follow] [--reset]
# Wait until now_playing.json gets a new history_id or cue.json gets a new mtime,
# then print one JSON line and exit. See wait_next.py.
# Exit: 0 changed, 3 timed out, 2 usage.
here=$(cd "$(dirname "$0")" && pwd)
exec python3 "$here/wait_next.py" "$@"
