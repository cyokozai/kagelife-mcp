#!/bin/sh
# mode.sh [--dir DIR]
# Print the setup kagelife was started in: human / ai / full, from DIR/kagelife_mode (one word written by tools/mac/start.sh).
# A missing, unreadable or unknown file prints full (the person switches the screen).
# Exit: 0 printed, 2 usage.
dir=${KAGELIFE_CACHE:-$HOME/.cache/kagelife}
case $# in
  0) ;;
  2)
    if [ "$1" != "--dir" ]; then echo "usage: mode.sh [--dir DIR]" >&2; exit 2; fi
    dir=$2
    ;;
  *) echo "usage: mode.sh [--dir DIR]" >&2; exit 2 ;;
esac
m=$(head -n 1 "$dir/kagelife_mode" 2>/dev/null | tr -d ' \t\r')
case $m in
  human|ai|full) echo "$m" ;;
  *) echo full ;;
esac
