#!/bin/sh
# back_slot.sh [--dir DIR] <active>
# Print the reserved slot to write next, from get_state's active and last_slot. See slots.py.
# Exit: 0 printed, 2 usage.
here=$(cd "$(dirname "$0")" && pwd)
exec python3 "$here/slots.py" back "$@"
