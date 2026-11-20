# Shared helpers for start.sh / stop.sh. Sourced, not run.

die() {
  printf '%s\n' "$*" >&2
  exit 1
}

# alive PID: true while the process runs. A zombie (exited, not yet reaped) counts as gone.
alive() {
  kill -0 "$1" 2>/dev/null || return 1
  if [ -r "/proc/$1/stat" ]; then
    # Field 3 is the state; the name in field 2 has no spaces for our processes.
    set -- $(cat "/proc/$1/stat" 2>/dev/null)
    [ "${3:-Z}" != Z ]
    return
  fi
  if command -v ps >/dev/null 2>&1; then
    st=$(ps -o stat= -p "$1" 2>/dev/null) || return 1
    case $st in
      Z*|'') return 1 ;;
    esac
  fi
  return 0
}
