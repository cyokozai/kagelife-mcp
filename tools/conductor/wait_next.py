#!/usr/bin/env python3
"""wait_next: 次の曲か「次の曲」の合図を待ち、1 行の JSON を出して終わる。

見るもの（既定は ~/.cache/kagelife/）:
  - now_playing.json の history_id が変わった      -> "song"
  - cue.json の更新時刻が変わった（中身は問わない） -> "cue"
  - 前回の状態が無く、今の曲が読めた               -> "start"
前回の状態は conductor_state.json に持つ。出力の例:
  {"changed": ["song"], "now_playing": {...}, "previous_history_id": "h1", "cue_mtime": null, "at": "..."}

macOS の /usr/bin/python3（3.9）で動くよう、標準ライブラリだけを使う。
終了コード: 0 = 変化あり、3 = --timeout で打ち切り、2 = 使い方の誤り。
"""

import argparse
import json
import os
import sys
import time
from datetime import datetime, timezone

STATE_NAME = "conductor_state.json"
NOW_NAME = "now_playing.json"
CUE_NAME = "cue.json"


def now_iso():
    return datetime.now(timezone.utc).isoformat()


def read_json(path):
    """読めない・壊れている・dict でないときは None。書き手は rename するので、壊れているのは一時的なものとみなす。"""
    try:
        with open(path, encoding="utf-8") as f:
            obj = json.load(f)
    except (OSError, ValueError):
        return None
    return obj if isinstance(obj, dict) else None


def write_json_atomic(path, obj):
    tmp = "%s.tmp.%d" % (path, os.getpid())
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(obj, f, ensure_ascii=False, indent=2)
        f.write("\n")
    os.replace(tmp, path)


def cue_mtime_ns(path):
    try:
        return os.stat(path).st_mtime_ns
    except OSError:
        return None


def current_song(path):
    obj = read_json(path)
    if obj is None:
        return None
    hid = obj.get("history_id")
    if hid is None or hid == "":
        return None
    return obj


class Watcher(object):
    def __init__(self, cache_dir):
        self.dir = cache_dir
        self.now_path = os.path.join(cache_dir, NOW_NAME)
        self.cue_path = os.path.join(cache_dir, CUE_NAME)
        self.state_path = os.path.join(cache_dir, STATE_NAME)

    def load_state(self):
        return read_json(self.state_path)

    def save_state(self, history_id, cue_ns):
        write_json_atomic(
            self.state_path,
            {"history_id": history_id, "cue_mtime_ns": cue_ns, "updated_at": now_iso()},
        )

    def event(self, changed, song, prev_hid, cue_ns):
        return {
            "changed": changed,
            "now_playing": song,
            "previous_history_id": prev_hid,
            "cue_mtime": None if cue_ns is None else cue_ns / 1e9,
            "at": now_iso(),
        }

    def first(self, reset):
        """前回の状態を読む。無ければ今を基準に記録し、曲が読めれば start のイベントを返す。"""
        state = None if reset else self.load_state()
        if state is not None:
            return state, None
        song = current_song(self.now_path)
        cue_ns = cue_mtime_ns(self.cue_path)
        hid = song.get("history_id") if song else None
        self.save_state(hid, cue_ns)
        state = {"history_id": hid, "cue_mtime_ns": cue_ns}
        if song is not None:
            return state, self.event(["start"], song, None, cue_ns)
        return state, None

    def poll(self, state):
        """変化があればイベントを返し、state を更新して保存する。無ければ None。"""
        song = current_song(self.now_path)
        cue_ns = cue_mtime_ns(self.cue_path)
        changed = []
        prev_hid = state.get("history_id")
        if song is not None and song.get("history_id") != prev_hid:
            changed.append("song")
        if cue_ns is not None and cue_ns != state.get("cue_mtime_ns"):
            changed.append("cue")
        if not changed:
            return None
        hid = song.get("history_id") if song is not None else prev_hid
        state["history_id"] = hid
        state["cue_mtime_ns"] = cue_ns if cue_ns is not None else state.get("cue_mtime_ns")
        self.save_state(state["history_id"], state["cue_mtime_ns"])
        return self.event(changed, song, prev_hid, cue_ns)


def emit(ev):
    sys.stdout.write(json.dumps(ev, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def main(argv=None):
    default_dir = os.environ.get("KAGELIFE_CACHE") or os.path.join(os.path.expanduser("~"), ".cache", "kagelife")
    ap = argparse.ArgumentParser(description="Wait for the next song (now_playing.json) or cue (cue.json).")
    ap.add_argument("--dir", default=default_dir, help="folder of now_playing.json / cue.json (default: %(default)s)")
    ap.add_argument("--interval", type=float, default=1.0, help="poll interval in seconds (default 1)")
    ap.add_argument("--timeout", type=float, default=0.0, help="give up after this many seconds (0 = never)")
    ap.add_argument("--follow", action="store_true", help="keep running and print one line per change")
    ap.add_argument("--reset", action="store_true", help="forget the saved state and start over")
    args = ap.parse_args(argv)
    if args.interval <= 0 or args.timeout < 0:
        ap.error("--interval must be > 0 and --timeout >= 0")

    try:
        os.makedirs(args.dir, exist_ok=True)
    except OSError as e:
        sys.stderr.write("wait_next: cannot use %s: %s\n" % (args.dir, e))
        return 2

    w = Watcher(args.dir)
    deadline = time.monotonic() + args.timeout if args.timeout > 0 else None
    emitted = 0

    state, ev = w.first(args.reset)
    if ev is not None:
        emit(ev)
        emitted += 1
        if not args.follow:
            return 0

    try:
        while True:
            if deadline is not None and time.monotonic() >= deadline:
                return 0 if emitted else 3
            time.sleep(args.interval)
            ev = w.poll(state)
            if ev is not None:
                emit(ev)
                emitted += 1
                if not args.follow:
                    return 0
    except KeyboardInterrupt:
        return 0 if emitted else 3


if __name__ == "__main__":
    sys.exit(main())
