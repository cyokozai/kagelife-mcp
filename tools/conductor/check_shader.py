#!/usr/bin/env python3
"""check_shader: write_shader の前に Kage のシェーダを静的に検査する。

検査すること:
  - 1 行目が `//kage:unit pixels`
  - ASCII だけ（コメントも。日本語は VJ 機で豆腐になる）
  - 64KiB 以下
  - パッケージの階層の var（= uniform）が kagelife の 6 つだけで、型が合っている
  - func Fragment がある
  - 1 画素あたりのループの回数の目安（定数の上限を掛け合わせて足す）

コンパイルの検査は write_shader の 422 に、明るさの検査は将来の cmd/render に任せる。
macOS の /usr/bin/python3（3.9）で動くよう、標準ライブラリだけを使う。

終了コード: 0 = 誤りなし（警告はあってもよい）、1 = 誤りあり、2 = 使い方の誤り・読めない。
"""

import argparse
import json
import re
import sys

MAX_BYTES = 64 * 1024
DIRECTIVE = "//kage:unit pixels"

# kagelife が毎フレーム渡す uniform。同じ名前で型が違うと描画時に panic する。
UNIFORMS = {
    "Time": "float",
    "Beat": "float",
    "Frame": "int",
    "Random": "float",
    "Resolution": "vec2",
    "Cursor": "vec2",
}

# 1 画素あたりのループの回数の目安。04_warp（20 回、本体が重い）は HD 630 で、重ね表示ありで 36fps 前後、なしで 45fps 前後。
LOOP_WARN = 16
LOOP_ERROR = 64


class Report(object):
    def __init__(self):
        self.findings = []
        self.uniforms = []
        self.loop_iterations = 0

    def add(self, level, code, message, line=None):
        self.findings.append({"level": level, "code": code, "line": line, "message": message})

    def errors(self):
        return [f for f in self.findings if f["level"] == "error"]

    def to_dict(self):
        return {
            "ok": not self.errors(),
            "findings": self.findings,
            "uniforms": self.uniforms,
            "loop_iterations": self.loop_iterations,
        }


def strip_comments(src):
    """コメントを空白に置き換える。改行は残すので行番号は変わらない。文字列リテラルは Kage に無い。"""
    out = []
    i = 0
    n = len(src)
    while i < n:
        if src.startswith("//", i):
            j = src.find("\n", i)
            if j < 0:
                j = n
            out.append(" " * (j - i))
            i = j
        elif src.startswith("/*", i):
            j = src.find("*/", i + 2)
            j = n if j < 0 else j + 2
            out.append("".join("\n" if c == "\n" else " " for c in src[i:j]))
            i = j
        else:
            out.append(src[i])
            i += 1
    return "".join(out)


def check_first_line(text, rep):
    first = text.split("\n", 1)[0].rstrip("\r").rstrip()
    if first != DIRECTIVE:
        rep.add("error", "first_line", "line 1 must be exactly '%s' (got %r)" % (DIRECTIVE, first[:60]), 1)


def check_ascii(raw, rep):
    bad_lines = []
    for no, line in enumerate(raw.split(b"\n"), 1):
        if any(b > 0x7F for b in line):
            bad_lines.append(no)
    if bad_lines:
        shown = ", ".join(str(n) for n in bad_lines[:10])
        more = "" if len(bad_lines) <= 10 else " (+%d more)" % (len(bad_lines) - 10)
        rep.add(
            "error",
            "non_ascii",
            "non-ASCII bytes on line %s%s; write comments in short ASCII English" % (shown, more),
            bad_lines[0],
        )


def check_size(raw, rep):
    if len(raw) > MAX_BYTES:
        rep.add("error", "too_large", "%d bytes is over the 64KiB limit (%d)" % (len(raw), MAX_BYTES))


VAR_ONE = re.compile(r"^var\s+(.+?)\s*$")
VAR_GROUP_OPEN = re.compile(r"^var\s*\(\s*$")
CONST_ONE = re.compile(r"^const\s+([A-Za-z_]\w*)\s*(?:[A-Za-z_]\w*\s*)?=\s*([0-9]+)\s*$")
FUNC_FRAGMENT = re.compile(r"^func\s+Fragment\s*\(")


def parse_var_spec(spec):
    """'Time float' や 'A, B vec2' から [(name, type)] を返す。読めなければ None。"""
    m = re.match(r"^([A-Za-z_]\w*(?:\s*,\s*[A-Za-z_]\w*)*)\s+([A-Za-z_][\w\[\]]*)\s*(=.*)?$", spec)
    if not m:
        return None
    names = [s.strip() for s in m.group(1).split(",")]
    return [(n, m.group(2)) for n in names], m.group(3) is not None


def check_uniform(name, typ, has_init, line, rep):
    if has_init:
        rep.add("error", "uniform_init", "package-level var %s has an initializer; uniforms cannot" % name, line)
    if name not in UNIFORMS:
        rep.add(
            "error",
            "unknown_uniform",
            "package-level var %s %s is not a kagelife uniform (allowed: %s)"
            % (name, typ, ", ".join("%s %s" % kv for kv in sorted(UNIFORMS.items()))),
            line,
        )
        return
    if UNIFORMS[name] != typ:
        rep.add(
            "error",
            "uniform_type",
            "uniform %s must be %s, not %s (a type mismatch panics at draw time)" % (name, UNIFORMS[name], typ),
            line,
        )
        return
    rep.uniforms.append(name)


FOR_RE = re.compile(
    r"\bfor\s+([A-Za-z_]\w*)\s*:=\s*(-?[0-9]+)\s*;\s*\1\s*(<=|<)\s*([^;]+?)\s*;\s*\1\s*(\+\+|\+=\s*([0-9]+))"
)


def resolve_bound(expr, consts):
    expr = expr.strip()
    if re.match(r"^-?[0-9]+$", expr):
        return int(expr)
    if expr in consts:
        return consts[expr]
    return None


def scan(code, rep):
    """コメントを除いたコードを行ごとに読み、uniform と for のループを数える。"""
    consts = {}
    lines = code.split("\n")
    # 1 周目: パッケージの階層の const（整数のもの）を集める。
    depth = 0
    for line in lines:
        s = line.strip()
        if depth == 0:
            m = CONST_ONE.match(s)
            if m:
                consts[m.group(1)] = int(m.group(2))
        depth += line.count("{") - line.count("}")

    depth = 0
    in_group = False
    has_fragment = False
    # ループの入れ子。stack は開いているループの {"close": 閉じる深さ, "product": 外側からの回数の積, "leaf": 内側にループが無いか}。
    # 目安の回数は「内側にループを持たないループの積」の合計（= いちばん内側の本体が 1 画素で回る回数）。
    stack = []
    loops = []
    for no, line in enumerate(lines, 1):
        s = line.strip()
        if depth == 0:
            if in_group:
                if s.startswith(")"):
                    in_group = False
                elif s:
                    parsed = parse_var_spec(s)
                    if parsed is None:
                        rep.add("warn", "var_unparsed", "could not read var spec %r" % s, no)
                    else:
                        for name, typ in parsed[0]:
                            check_uniform(name, typ, parsed[1], no, rep)
            elif VAR_GROUP_OPEN.match(s):
                in_group = True
            else:
                m = VAR_ONE.match(s)
                if m:
                    parsed = parse_var_spec(m.group(1))
                    if parsed is None:
                        rep.add("warn", "var_unparsed", "could not read var spec %r" % s, no)
                    else:
                        for name, typ in parsed[0]:
                            check_uniform(name, typ, parsed[1], no, rep)
            if FUNC_FRAGMENT.match(s):
                has_fragment = True

        # ループ
        pos = 0
        col_depth = depth
        for m in FOR_RE.finditer(line):
            seg = line[pos:m.start()]
            col_depth += seg.count("{") - seg.count("}")
            pos = m.start()
            while stack and stack[-1]["close"] > col_depth:
                stack.pop()
            start = int(m.group(2))
            bound = resolve_bound(m.group(4), consts)
            step = int(m.group(6)) if m.group(6) else 1
            if bound is None:
                rep.add("warn", "loop_unknown", "loop bound %r is not a known integer const" % m.group(4).strip(), no)
                count = 1
            else:
                if m.group(3) == "<=":
                    bound += 1
                count = max(0, (bound - start + step - 1) // step)
            outer = stack[-1]["product"] if stack else 1
            if stack:
                stack[-1]["leaf"] = False
            node = {"close": col_depth + 1, "product": outer * count, "leaf": True}
            stack.append(node)
            loops.append(node)
        if re.search(r"\bfor\b", line) and not FOR_RE.search(line):
            rep.add("warn", "loop_unknown", "loop form not recognized: %r" % s[:60], no)

        depth += line.count("{") - line.count("}")
        while stack and stack[-1]["close"] > depth:
            stack.pop()
    total = sum(n["product"] for n in loops if n["leaf"])

    if not has_fragment:
        rep.add("error", "no_fragment", "func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 is missing")

    rep.loop_iterations = total
    if total > LOOP_ERROR:
        rep.add(
            "error",
            "loop_too_heavy",
            "about %d loop iterations per pixel (> %d); the HD 630 will drop fps" % (total, LOOP_ERROR),
        )
    elif total >= LOOP_WARN:
        rep.add(
            "warn",
            "loop_heavy",
            "about %d loop iterations per pixel (>= %d); 04_warp runs 20 at about 36fps with the overlay, 45fps without; keep the body light"
            % (total, LOOP_WARN),
        )

    if "Random" in rep.uniforms:
        rep.add(
            "warn",
            "random_flash",
            "Random changes every frame: do not use it to flash the whole screen (light rules)",
        )


def check(raw):
    rep = Report()
    check_size(raw, rep)
    check_ascii(raw, rep)
    text = raw.decode("utf-8", errors="replace")
    if text.startswith("﻿"):
        text = text[1:]
        rep.add("error", "first_line", "file starts with a BOM", 1)
    else:
        check_first_line(text, rep)
    scan(strip_comments(text.replace("\r\n", "\n")), rep)
    return rep


def main(argv=None):
    ap = argparse.ArgumentParser(description="Static checks for a kagelife Kage shader before write_shader.")
    ap.add_argument("path", help="shader file, or - for stdin")
    ap.add_argument("--json", action="store_true", help="print the report as JSON")
    args = ap.parse_args(argv)

    try:
        if args.path == "-":
            raw = sys.stdin.buffer.read()
        else:
            with open(args.path, "rb") as f:
                raw = f.read()
    except OSError as e:
        sys.stderr.write("check_shader: cannot read %s: %s\n" % (args.path, e))
        return 2

    rep = check(raw)
    if args.json:
        print(json.dumps(rep.to_dict(), ensure_ascii=False))
    else:
        for f in rep.findings:
            where = " line %d" % f["line"] if f["line"] else ""
            print("%s %s%s: %s" % (f["level"].upper(), f["code"], where, f["message"]))
        print(
            "%s (uniforms: %s; loop iterations per pixel: %d)"
            % ("OK" if not rep.errors() else "NG", ", ".join(rep.uniforms) or "none", rep.loop_iterations)
        )
    return 1 if rep.errors() else 0


if __name__ == "__main__":
    sys.exit(main())
