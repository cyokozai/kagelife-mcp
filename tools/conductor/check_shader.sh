#!/bin/sh
# check_shader.sh <file|-> [--json]
# Static checks for a kagelife shader before write_shader. See check_shader.py.
# Exit: 0 ok (warnings allowed), 1 errors, 2 usage / unreadable.
here=$(cd "$(dirname "$0")" && pwd)
if [ $# -eq 0 ]; then
  echo "usage: check_shader.sh <shader.kage|-> [--json]" >&2
  exit 2
fi
exec python3 "$here/check_shader.py" "$@"
