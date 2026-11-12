#!/bin/sh
# note_written.sh [--dir DIR] <llm_a|llm_b>
# Record the reserved slot just written (last_slot in conductor_state.json). See slots.py.
# Exit: 0 recorded, 2 usage / not a reserved slot.
here=$(cd "$(dirname "$0")" && pwd)
exec python3 "$here/slots.py" note "$@"
