#!/bin/sh
# save_setlist.sh --name NAME --dest <kagelife-shaders> [--cache DIR] [--since ISO8601]
# Copy the shaders written during a live set into <dest>/presets/<NAME>/ (NN_<slug>.kage + index.json). See save_setlist.py.
# Exit: 0 saved (may warn), 1 nothing to save, 2 usage / already exists / cannot write.
here=$(cd "$(dirname "$0")" && pwd)
exec python3 "$here/save_setlist.py" "$@"
