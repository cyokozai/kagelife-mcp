#!/usr/bin/env python3
"""push_now_playing: Mac の now_playing.json が変わったら、ssh で VJ 機の同じ場所へ原子的に書く。

  push_now_playing.sh [host]            既定の host は vj。変化を見張り続ける
  push_now_playing.sh --once [host]     1 回だけ送って終わる

- 送り先では「同じフォルダの一時ファイルに書く -> 長さを確かめる -> mv」で置き換える。
  読み手が書きかけを読むことは無く、途中で切れた中身で置き換えることも無い
- 書きかけ・壊れた JSON は送らない。同じ中身は 2 度送らない（覚えているのは動いている間だけ）
- 送れなかったら、中身が変わったときか --retry 秒後に送り直す。送れなかったあとは、前に送れた中身も送り直す
- ssh は環境変数 KAGELIFE_PUSH_SSH で差し替えられる（テスト用）

macOS の /usr/bin/python3（3.9）で動くよう、標準ライブラリだけを使う。
終了コード: 0 = 正常（--once なら送れたか送るものが無い）、1 = --once で送れなかった、2 = 使い方の誤り。
"""

import argparse
import json
import os
import shlex
import subprocess
import sys
import time
from datetime import datetime

SSH_OPTS = ["-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5"]
NAME = "now_playing.json"


def log(msg):
    sys.stderr.write("%s push_now_playing: %s\n" % (datetime.now().strftime("%H:%M:%S"), msg))
    sys.stderr.flush()


def remote_dir_expr(d):
    """リモートの sh に渡すフォルダの式。~/ は送り先の $HOME に展開させる。"""
    if d == "~":
        return '"$HOME"'
    if d.startswith("~/"):
        return '"$HOME"/' + shlex.quote(d[2:])
    return shlex.quote(d)


def remote_script(d, size):
    """一時ファイルに受け、届いたバイト数が size と一致したときだけ置き換える。

    途中で切れたら一時ファイルを消し、元のファイルを残して失敗を返す。
    BSD の wc -c は数の前に空白を付けるので、$n を引用符で囲まずに数として比べる。
    """
    q = remote_dir_expr(d)
    return (
        "d=%s && mkdir -p \"$d\" && t=\"$d/.%s.tmp.$$\" && "
        "{ cat > \"$t\" && n=$(wc -c < \"$t\") && [ $n -eq %d ] && mv -f \"$t\" \"$d/%s\" || "
        "{ rm -f \"$t\"; echo \"incomplete transfer (expected %d bytes)\" >&2; exit 1; }; }"
        % (q, NAME, size, NAME, size)
    )


def read_valid(path):
    """読めて JSON の dict なら中身のバイト列、そうでなければ None。"""
    try:
        with open(path, "rb") as f:
            raw = f.read()
        obj = json.loads(raw.decode("utf-8"))
    except (OSError, ValueError):
        return None
    return raw if isinstance(obj, dict) else None


def send(raw, host, rdir, timeout):
    ssh = os.environ.get("KAGELIFE_PUSH_SSH") or "ssh"
    argv = [ssh] + SSH_OPTS + [host, remote_script(rdir, len(raw))]
    try:
        p = subprocess.run(argv, input=raw, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired) as e:
        log("send to %s failed: %s" % (host, e))
        return False
    if p.returncode != 0:
        err = p.stderr.decode("utf-8", "replace").strip().splitlines()
        log("send to %s failed: rc=%d %s" % (host, p.returncode, err[-1] if err else ""))
        return False
    return True


def main(argv=None):
    home = os.path.expanduser("~")
    ap = argparse.ArgumentParser(description="Push ~/.cache/kagelife/now_playing.json to the VJ machine over ssh.")
    ap.add_argument("host", nargs="?", default="vj", help="ssh host of the VJ machine (default: vj)")
    ap.add_argument("--src", default=os.path.join(home, ".cache", "kagelife", NAME), help="local file (default: %(default)s)")
    ap.add_argument("--remote-dir", default="~/.cache/kagelife", help="folder on the VJ machine (default: %(default)s)")
    ap.add_argument("--interval", type=float, default=1.0, help="poll interval in seconds (default 1)")
    ap.add_argument("--retry", type=float, default=10.0, help="retry a failed send after this many seconds (default 10)")
    ap.add_argument("--timeout", type=float, default=0.0, help="stop after this many seconds (0 = never; for tests)")
    ap.add_argument("--once", action="store_true", help="send once and exit")
    args = ap.parse_args(argv)
    if args.interval <= 0 or args.retry <= 0 or args.timeout < 0:
        ap.error("--interval and --retry must be > 0, --timeout >= 0")

    if args.once:
        raw = read_valid(args.src)
        if raw is None:
            log("nothing to send (%s missing or not valid JSON)" % args.src)
            return 0
        if send(raw, args.host, args.remote_dir, 30):
            log("sent %d bytes to %s:%s" % (len(raw), args.host, args.remote_dir))
            return 0
        return 1

    deadline = time.monotonic() + args.timeout if args.timeout > 0 else None
    sent = None
    failed = None
    next_retry = 0.0
    log("watching %s -> %s:%s" % (args.src, args.host, args.remote_dir))
    try:
        while deadline is None or time.monotonic() < deadline:
            raw = read_valid(args.src)
            now = time.monotonic()
            if raw is not None and raw != sent and (raw != failed or now >= next_retry):
                if send(raw, args.host, args.remote_dir, 30):
                    sent, failed = raw, None
                    log("sent %d bytes" % len(raw))
                else:
                    # 先方に届いたかは分からない（書けたあとで ssh が切れることもある）。
                    # 覚えている sent を消し、手元が前の中身に戻ったときも送り直す。
                    sent = None
                    failed, next_retry = raw, now + args.retry
            time.sleep(args.interval)
    except KeyboardInterrupt:
        pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
