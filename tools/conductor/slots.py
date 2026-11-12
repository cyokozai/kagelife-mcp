#!/usr/bin/env python3
"""slots: 最後に書いた予約名を conductor_state.json の last_slot に残し、次の書く先を決める。

  slots.py note <name>    書けた予約名を記録する（note_written.sh）
  slots.py back <active>  get_state の active から書く先を 1 行で出す（back_slot.sh）

決め方: active が予約名の一方なら、もう一方。それ以外なら last_slot でない方。記録が無ければ最初の予約名。
wait_next.py と同じ状態のファイルを使い、他のキーは消さない。
終了コード: 0 = 成功、2 = 使い方の誤り。
"""

import argparse
import json
import os
import sys

STATE_NAME = "conductor_state.json"
DEFAULT_SLOTS = "llm_a,llm_b"


def read_state(path):
    try:
        with open(path, encoding="utf-8") as f:
            obj = json.load(f)
    except (OSError, ValueError):
        return {}
    return obj if isinstance(obj, dict) else {}


def write_state(path, obj):
    tmp = "%s.tmp.%d" % (path, os.getpid())
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(obj, f, ensure_ascii=False, indent=2)
        f.write("\n")
    os.replace(tmp, path)


def back_slot(active, slots, last):
    if active in slots:
        others = [s for s in slots if s != active]
        return others[0]
    if last in slots:
        others = [s for s in slots if s != last]
        return others[0]
    return slots[0]


def main(argv=None):
    default_dir = os.environ.get("KAGELIFE_CACHE") or os.path.join(os.path.expanduser("~"), ".cache", "kagelife")
    ap = argparse.ArgumentParser(description="Record the last written reserved slot / pick the back slot.")
    ap.add_argument("cmd", choices=["note", "back"])
    ap.add_argument("name", help="note: the slot just written / back: active from get_state")
    ap.add_argument("--dir", default=default_dir, help="folder of conductor_state.json (default: %(default)s)")
    ap.add_argument("--slots", default=DEFAULT_SLOTS, help="reserved names, comma separated (default: %(default)s)")
    args = ap.parse_args(argv)

    slots = [s.strip() for s in args.slots.split(",") if s.strip()]
    if len(slots) != 2:
        ap.error("--slots needs exactly two names")
    path = os.path.join(args.dir, STATE_NAME)

    if args.cmd == "note":
        if args.name not in slots:
            sys.stderr.write("note_written: %s is not a reserved slot (%s); nothing recorded\n" % (args.name, ", ".join(slots)))
            return 2
        os.makedirs(args.dir, exist_ok=True)
        state = read_state(path)
        state["last_slot"] = args.name
        write_state(path, state)
        print(args.name)
        return 0

    print(back_slot(args.name, slots, read_state(path).get("last_slot")))
    return 0


if __name__ == "__main__":
    sys.exit(main())
