#!/usr/bin/env python3
"""save_setlist: ライブで書いたシェーダの流れを、kagelife-shaders のプリセット 1 つとして写す。

読むもの（--cache、既定 ~/.cache/kagelife/）:
  - setlist_log.jsonl         note_written.sh --history-id ... が 1 曲 1 行で足した記録
  - live/<history_id>.kage    曲ごとの下書き（CONDUCTOR の「1 周」の 5）
書くもの（--dest は kagelife-shaders の checkout）:
  - presets/<name>/NN_<ascii-slug>.kage
  - presets/<name>/index.json  [{"title", "artist", "file", "idea"}]（CONDUCTOR の段 A と同じ形）

同じ history_id が何度もあれば、最後の記録を、その曲が最初に出た位置に置く。live/ に無い曲は飛ばして警告する。
presets/<name>/ が既にあれば何もせずに止まる。git の操作はしない。
写した後、kagelife-shaders の検査（先頭 5 行以内の //kage:unit pixels、Uniform は 6 つだけ）に合わないファイルを警告する。

macOS の /usr/bin/python3（3.9）で動くよう、標準ライブラリだけを使う。
終了コード: 0 = 写した（警告はあってもよい）、1 = 写す曲が無い、2 = 使い方の誤り・既にある・書けない。
"""

import argparse
import json
import os
import re
import shutil
import sys
import unicodedata
from datetime import datetime, timezone

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, "..", "conductor"))

import check_shader  # noqa: E402

LOG_NAME = "setlist_log.jsonl"
LIVE_NAME = "live"
INDEX_NAME = "index.json"
DIRECTIVE = "//kage:unit pixels"
DIRECTIVE_LINES = 5
SLUG_MAX = 40
NAME_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*$")
HID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]*$")
UNIFORM_CODES = ("unknown_uniform", "uniform_type", "uniform_init")


def warn(msg):
    sys.stderr.write("WARN %s\n" % msg)


def _slug1(text):
    s = unicodedata.normalize("NFKD", text or "").encode("ascii", "ignore").decode("ascii").lower()
    s = re.sub(r"[^a-z0-9]+", "_", s).strip("_")
    return s[:SLUG_MAX].rstrip("_")


def slug(title, fallback=""):
    """ASCII の小文字・数字・_ だけの名前。曲名から作れなければ fallback（history_id）から、それも無理なら song。"""
    return _slug1(title) or _slug1(fallback) or "song"


def parse_time(s):
    """ISO 8601 を UTC の aware な datetime に。末尾の Z と、時差の無い形（UTC とみなす）も受ける。"""
    s = s.strip()
    if s.endswith("Z") or s.endswith("z"):
        s = s[:-1] + "+00:00"
    t = datetime.fromisoformat(s)
    if t.tzinfo is None:
        t = t.replace(tzinfo=timezone.utc)
    return t


def read_log(path, since=None):
    """記録を読み、history_id ごとに最後の 1 件にする。並びは各曲が最初に出た順。"""
    order = []
    last = {}
    try:
        f = open(path, encoding="utf-8")
    except OSError:
        return []
    with f:
        for no, line in enumerate(f, 1):
            line = line.strip()
            if not line:
                continue
            try:
                row = json.loads(line)
                hid = row["history_id"]
                at = parse_time(row["at"])
            except (ValueError, KeyError, TypeError, AttributeError):
                warn("%s:%d: unreadable line, skipped" % (path, no))
                continue
            if not isinstance(hid, str) or not HID_RE.match(hid):
                warn("%s:%d: unsafe history_id %r, skipped" % (path, no, hid))
                continue
            if since is not None and at <= since:
                continue
            if hid not in last:
                order.append(hid)
            last[hid] = row
    return [last[h] for h in order]


def shaders_repo_problems(text):
    """kagelife-shaders の shaders_test.go が落としそうな点を 1 行ずつ返す（コンパイルは見ない）。"""
    problems = []
    head = text.lstrip("﻿").replace("\r\n", "\n").split("\n")[:DIRECTIVE_LINES]
    if not any(l.strip() == DIRECTIVE for l in head):
        problems.append("%s is not in the first %d lines" % (DIRECTIVE, DIRECTIVE_LINES))
    rep = check_shader.Report()
    # kagelife-shaders の shaders_test.go は今も 6 つだけを許す（音の 4 つは未対応）ので、ここでは 6 つで見る。
    check_shader.scan(check_shader.strip_comments(text.replace("\r\n", "\n")), rep, uniforms=check_shader.BASE_UNIFORMS)
    for f in rep.findings:
        if f["code"] in UNIFORM_CODES:
            problems.append("line %s: %s" % (f["line"], f["message"]))
    return problems


def main(argv=None):
    default_cache = os.environ.get("KAGELIFE_CACHE") or os.path.join(os.path.expanduser("~"), ".cache", "kagelife")
    ap = argparse.ArgumentParser(description="Copy the shaders written during a live set into kagelife-shaders/presets/<name>/.")
    ap.add_argument("--cache", default=default_cache, help="folder of setlist_log.jsonl and live/ (default: %(default)s)")
    ap.add_argument("--since", help="only records after this ISO 8601 time (default: all)")
    ap.add_argument("--name", required=True, help="preset name (folder under presets/)")
    ap.add_argument("--dest", required=True, help="checkout of kagelife-shaders")
    args = ap.parse_args(argv)

    if not NAME_RE.match(args.name):
        ap.error("--name must be letters, digits, '.', '_', '-' and not start with '.': %r" % args.name)
    since = None
    if args.since:
        try:
            since = parse_time(args.since)
        except ValueError:
            ap.error("--since is not an ISO 8601 time: %r" % args.since)
    if not os.path.isdir(args.dest):
        sys.stderr.write("save_setlist: --dest %s is not a folder\n" % args.dest)
        return 2
    presets = os.path.join(args.dest, "presets")
    out = os.path.join(presets, args.name)
    if os.path.lexists(out):
        sys.stderr.write("save_setlist: %s already exists; nothing written (pick another --name)\n" % out)
        return 2

    picked = []
    for row in read_log(os.path.join(args.cache, LOG_NAME), since):
        src = os.path.join(args.cache, LIVE_NAME, row["history_id"] + ".kage")
        if not os.path.isfile(src):
            warn("%s (%s): %s not found, skipped" % (row["history_id"], row.get("title", ""), src))
            continue
        picked.append((row, src))
    if not picked:
        sys.stderr.write("save_setlist: nothing to save (no record with a draft in %s)\n"
                         % os.path.join(args.cache, LIVE_NAME))
        return 1

    os.makedirs(presets, exist_ok=True)
    tmp = os.path.join(presets, ".%s.tmp.%d" % (args.name, os.getpid()))
    index = []
    try:
        os.mkdir(tmp)
        for i, (row, src) in enumerate(picked, 1):
            name = "%02d_%s.kage" % (i, slug(row.get("title", ""), row["history_id"]))
            shutil.copyfile(src, os.path.join(tmp, name))
            index.append({
                "title": row.get("title", ""),
                "artist": row.get("artist", ""),
                "file": name,
                "idea": row.get("idea", ""),
            })
        with open(os.path.join(tmp, INDEX_NAME), "w", encoding="utf-8") as f:
            json.dump(index, f, ensure_ascii=False, indent=2)
            f.write("\n")
        if os.path.lexists(out):
            raise OSError("%s appeared while copying" % out)
        os.rename(tmp, out)
    except OSError as e:
        shutil.rmtree(tmp, ignore_errors=True)
        sys.stderr.write("save_setlist: %s\n" % e)
        return 2

    for item in index:
        print(os.path.join(out, item["file"]))
    print(os.path.join(out, INDEX_NAME))

    bad = 0
    for item in index:
        with open(os.path.join(out, item["file"]), encoding="utf-8", errors="replace") as f:
            problems = shaders_repo_problems(f.read())
        for p in problems:
            warn("%s: %s" % (item["file"], p))
        bad += 1 if problems else 0
    if bad:
        warn("%d of %d files may fail the kagelife-shaders check (shaders_test.go); fix or drop them before the PR"
             % (bad, len(index)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
