"""check_shader のテスト。正しい例・壊れた例のシェーダを作り、終了コードと指摘を確かめる。

標準ライブラリの unittest だけで動く（コンテナに pip で何も入れない）。
"""

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
TOOL = HERE.parent
SH = TOOL / "check_shader.sh"

GOOD = """//kage:unit pixels

package main

var Time float
var Beat float
var Resolution vec2

// one ring that pulses on the beat
func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	p := (dstPos.xy - 0.5*Resolution) / Resolution.y
	pulse := 1.0 - Beat
	d := length(p)
	ring := smoothstep(0.02, 0.0, abs(d-0.25-0.1*pulse))
	col := 0.5 + 0.5*cos(Time+vec3(0, 2, 4)+d*6.0)
	return vec4(col*(0.3+ring), 1)
}
"""


def with_body(decls="", body="return vec4(0, 0, 0, 1)", head="//kage:unit pixels"):
    return (
        head
        + "\n\npackage main\n\n"
        + decls
        + "\nfunc Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {\n\t"
        + body
        + "\n}\n"
    )


def run(src=None, args=(), stdin=None):
    with tempfile.TemporaryDirectory() as d:
        argv = ["sh", str(SH), *args]
        if src is not None:
            path = Path(d) / "u3cd-test.kage"
            if isinstance(src, bytes):
                path.write_bytes(src)
            else:
                path.write_text(src, encoding="utf-8")
            argv.append(str(path))
        p = subprocess.run(
            argv,
            input=stdin,
            capture_output=True,
            text=True,
            timeout=20,
        )
        return p


def run_json(src):
    p = run(src, args=("--json",))
    return p.returncode, json.loads(p.stdout)


def codes(report, level):
    return [f["code"] for f in report["findings"] if f["level"] == level]


class GoodShader(unittest.TestCase):
    def test_good_shader_passes(self):
        p = run(GOOD)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("OK", p.stdout)

    def test_json_report(self):
        rc, rep = run_json(GOOD)
        self.assertEqual(rc, 0)
        self.assertTrue(rep["ok"])
        self.assertEqual(codes(rep, "error"), [])
        self.assertEqual(rep["loop_iterations"], 0)

    def test_reads_stdin(self):
        p = run(None, args=("-",), stdin=GOOD)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_crlf_first_line_is_accepted(self):
        p = run(GOOD.replace("\n", "\r\n").encode("ascii"))
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)


class FirstLine(unittest.TestCase):
    def test_missing_directive(self):
        rc, rep = run_json(with_body(head="package main // no directive"))
        self.assertEqual(rc, 1)
        self.assertIn("first_line", codes(rep, "error"))

    def test_directive_not_first(self):
        rc, rep = run_json("\n" + with_body())
        self.assertEqual(rc, 1)
        self.assertIn("first_line", codes(rep, "error"))


class Ascii(unittest.TestCase):
    def test_non_ascii_comment_is_error_with_line(self):
        src = with_body(decls="// 光る輪\n")  # Japanese comment
        rc, rep = run_json(src)
        self.assertEqual(rc, 1)
        errs = [f for f in rep["findings"] if f["code"] == "non_ascii"]
        self.assertEqual(len(errs), 1)
        self.assertEqual(errs[0]["line"], 5)

    def test_bom_is_error(self):
        rc, rep = run_json(b"\xef\xbb\xbf" + with_body().encode("ascii"))
        self.assertEqual(rc, 1)
        self.assertIn("non_ascii", codes(rep, "error"))


class Size(unittest.TestCase):
    def test_over_64kib_is_error(self):
        pad = "// " + "x" * 76 + "\n"
        src = with_body(decls=pad * 900)  # about 72 KB
        self.assertGreater(len(src.encode()), 65536)
        rc, rep = run_json(src)
        self.assertEqual(rc, 1)
        self.assertIn("too_large", codes(rep, "error"))

    def test_exactly_64kib_is_ok(self):
        base = with_body()
        pad_len = 65536 - len(base.encode())
        src = base + "//" + "x" * (pad_len - 3) + "\n"
        self.assertEqual(len(src.encode()), 65536)
        rc, rep = run_json(src)
        self.assertNotIn("too_large", codes(rep, "error"))


class Uniforms(unittest.TestCase):
    def test_all_known_uniforms_ok(self):
        decls = (
            "var Time float\nvar Beat float\nvar Frame int\nvar Random float\n"
            "var Resolution vec2\nvar Cursor vec2\n"
        )
        rc, rep = run_json(with_body(decls=decls))
        self.assertEqual(codes(rep, "error"), [])
        self.assertEqual(
            sorted(rep["uniforms"]),
            ["Beat", "Cursor", "Frame", "Random", "Resolution", "Time"],
        )

    def test_unknown_uniform(self):
        rc, rep = run_json(with_body(decls="var AudioBass float\n"))
        self.assertEqual(rc, 1)
        self.assertIn("unknown_uniform", codes(rep, "error"))

    def test_wrong_type(self):
        rc, rep = run_json(with_body(decls="var Frame float\n"))
        self.assertEqual(rc, 1)
        self.assertIn("uniform_type", codes(rep, "error"))

    def test_grouped_var_block(self):
        decls = "var (\n\tTime float\n\tLevel float\n)\n"
        rc, rep = run_json(with_body(decls=decls))
        self.assertEqual(rc, 1)
        bad = [f for f in rep["findings"] if f["code"] == "unknown_uniform"]
        self.assertEqual(len(bad), 1)
        self.assertIn("Level", bad[0]["message"])

    def test_local_var_inside_function_is_not_uniform(self):
        body = "var x float\n\tx = 0.5\n\treturn vec4(x, x, x, 1)"
        rc, rep = run_json(with_body(body=body))
        self.assertEqual(rc, 0, rep)
        self.assertEqual(rep["uniforms"], [])

    def test_uniform_in_comment_is_ignored(self):
        rc, rep = run_json(with_body(decls="// var Level float\n/* var X vec2 */\n"))
        self.assertEqual(rc, 0, rep)

    def test_random_gets_a_warning(self):
        rc, rep = run_json(with_body(decls="var Random float\n"))
        self.assertEqual(rc, 0)
        self.assertIn("random_flash", codes(rep, "warn"))


class Structure(unittest.TestCase):
    def test_missing_fragment(self):
        src = "//kage:unit pixels\n\npackage main\n\nfunc main() {}\n"
        rc, rep = run_json(src)
        self.assertEqual(rc, 1)
        self.assertIn("no_fragment", codes(rep, "error"))


class Loops(unittest.TestCase):
    def loop(self, n, inner=None):
        if inner is None:
            body = "c := 0.0\n\tfor i := 0; i < %s; i++ {\n\t\tc += 0.01\n\t}\n\treturn vec4(c, c, c, 1)" % n
        else:
            body = (
                "c := 0.0\n\tfor i := 0; i < %s; i++ {\n\t\tfor j := 0; j < %s; j++ {\n"
                "\t\t\tc += 0.01\n\t\t}\n\t}\n\treturn vec4(c, c, c, 1)" % (n, inner)
            )
        return body

    def test_small_loop_ok(self):
        rc, rep = run_json(with_body(body=self.loop(8)))
        self.assertEqual(rc, 0)
        self.assertEqual(rep["loop_iterations"], 8)
        self.assertEqual(codes(rep, "warn"), [])

    def test_const_bound_is_resolved_and_warns(self):
        rc, rep = run_json(with_body(decls="const LayerCount = 20\n", body=self.loop("LayerCount")))
        self.assertEqual(rc, 0)
        self.assertEqual(rep["loop_iterations"], 20)
        self.assertIn("loop_heavy", codes(rep, "warn"))

    def test_nested_loops_multiply(self):
        rc, rep = run_json(with_body(body=self.loop(8, inner=8)))
        self.assertEqual(rep["loop_iterations"], 64)
        self.assertEqual(rc, 0)
        self.assertIn("loop_heavy", codes(rep, "warn"))

    def test_too_many_iterations_is_error(self):
        rc, rep = run_json(with_body(body=self.loop(100)))
        self.assertEqual(rc, 1)
        self.assertIn("loop_too_heavy", codes(rep, "error"))

    def test_le_bound_and_nonzero_start(self):
        body = "c := 0.0\n\tfor i := 2; i <= 9; i++ {\n\t\tc += 0.01\n\t}\n\treturn vec4(c, c, c, 1)"
        rc, rep = run_json(with_body(body=body))
        self.assertEqual(rep["loop_iterations"], 8)

    def test_unknown_bound_warns(self):
        body = "c := 0.0\n\tfor i := 0; i < len(x); i++ {\n\t\tc += 0.01\n\t}\n\treturn vec4(c, c, c, 1)"
        rc, rep = run_json(with_body(body=body))
        self.assertIn("loop_unknown", codes(rep, "warn"))


class Usage(unittest.TestCase):
    def test_no_args_is_usage_error(self):
        p = subprocess.run(["sh", str(SH)], capture_output=True, text=True, timeout=20)
        self.assertEqual(p.returncode, 2)

    def test_missing_file(self):
        p = subprocess.run(
            ["sh", str(SH), "/nonexistent/u3cd-none.kage"], capture_output=True, text=True, timeout=20
        )
        self.assertEqual(p.returncode, 2)


class Py39Syntax(unittest.TestCase):
    """macOS の /usr/bin/python3 は 3.9。3.10 以降の構文を使っていないことを確かめる。"""

    def test_scripts_parse_as_39(self):
        import ast

        for name in ("check_shader.py", "wait_next.py"):
            src = (TOOL / name).read_text(encoding="utf-8")
            ast.parse(src, filename=name, feature_version=(3, 9))


if __name__ == "__main__":
    unittest.main()
