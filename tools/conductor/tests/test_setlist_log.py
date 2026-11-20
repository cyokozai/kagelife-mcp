"""note_written の曲の記録（setlist_log.jsonl）のテスト。

--history-id などを渡したときだけ、書けた曲を 1 行の JSON で足す。引数なしの今の使い方は変えない。
記録は conductor_state.json と同じ鍵（conductor_state.json.lock）の下で書く。
"""

import contextlib
import io
import json
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
TOOL = HERE.parent
NOTE = TOOL / "note_written.sh"
sys.path.insert(0, str(TOOL))

import slots  # noqa: E402


class Base(unittest.TestCase):
    def setUp(self):
        self._td = tempfile.TemporaryDirectory()
        self.dir = Path(self._td.name)
        self.state = self.dir / "conductor_state.json"
        self.log = self.dir / "setlist_log.jsonl"

    def tearDown(self):
        self._td.cleanup()

    def note(self, *args):
        return subprocess.run(
            ["sh", str(NOTE), "--dir", str(self.dir), *args],
            capture_output=True,
            text=True,
            timeout=20,
        )

    def lines(self):
        return [json.loads(s) for s in self.log.read_text(encoding="utf-8").splitlines()]


class SetlistLog(Base):
    def test_appends_one_line_with_song(self):
        p = self.note("llm_a", "--history-id", "h1", "--title", "夜に駆ける", "--artist", "YOASOBI",
                      "--idea", "blue rings")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(p.stdout.strip(), "llm_a")
        rows = self.lines()
        self.assertEqual(len(rows), 1)
        r = rows[0]
        self.assertEqual((r["slot"], r["history_id"], r["title"], r["artist"], r["idea"]),
                         ("llm_a", "h1", "夜に駆ける", "YOASOBI", "blue rings"))
        self.assertIn("T", r["at"])  # ISO 8601
        self.assertEqual(json.loads(self.state.read_text())["last_slot"], "llm_a")

    def test_appends_in_order(self):
        self.note("llm_a", "--history-id", "h1", "--title", "A", "--artist", "X")
        self.note("llm_b", "--history-id", "h2", "--title", "B", "--artist", "Y")
        rows = self.lines()
        self.assertEqual([r["history_id"] for r in rows], ["h1", "h2"])
        self.assertEqual(rows[1]["idea"], "")  # --idea is optional

    def test_line_is_one_line_json_with_non_ascii_kept(self):
        self.note("llm_a", "--history-id", "h1", "--title", "曲\n名", "--artist", "X")
        raw = self.log.read_text(encoding="utf-8")
        self.assertEqual(raw.count("\n"), 1)
        self.assertTrue(raw.endswith("\n"))
        self.assertIn("曲", raw)  # not \\u escaped
        self.assertEqual(self.lines()[0]["title"], "曲\n名")

    def test_without_song_args_writes_no_log(self):
        p = self.note("llm_b")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertFalse(self.log.exists())
        self.assertEqual(json.loads(self.state.read_text())["last_slot"], "llm_b")

    def test_song_args_without_history_id_is_usage_error(self):
        p = self.note("llm_a", "--title", "A")
        self.assertEqual(p.returncode, 2)
        self.assertFalse(self.log.exists())
        self.assertFalse(self.state.exists())

    def test_non_reserved_name_writes_no_log(self):
        p = self.note("04_warp", "--history-id", "h1", "--title", "A")
        self.assertEqual(p.returncode, 2)
        self.assertFalse(self.log.exists())


class SetlistLogLock(Base):
    def test_log_is_written_under_the_state_lock(self):
        # 鍵を持っている間は記録が増えない。鍵を放したら増える。
        held = threading.Event()
        release = threading.Event()

        def hold():
            with slots.state_lock(str(self.state)):
                held.set()
                release.wait(5)

        t = threading.Thread(target=hold)
        t.start()
        self.assertTrue(held.wait(5))
        done = []

        def write():
            with contextlib.redirect_stdout(io.StringIO()):
                done.append(slots.main(["note", "llm_a", "--dir", str(self.dir), "--history-id", "h1",
                                        "--title", "A"]))

        w = threading.Thread(target=write)
        w.start()
        time.sleep(0.3)
        self.assertFalse(self.log.exists())
        release.set()
        t.join(5)
        w.join(5)
        self.assertEqual(done, [0])
        self.assertEqual(self.lines()[0]["history_id"], "h1")


if __name__ == "__main__":
    unittest.main()
