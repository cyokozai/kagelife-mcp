"""conductor_state.json の排他のテスト。note_written（slots.py）と wait_next.py が重なって書いても、両方の更新が残る。

片方の「読む」を遅らせ、その間にもう片方が書くように並べる。鍵が無ければ、遅らせた側が古い中身で書き戻して
相手の更新を消す。2 つは別のスレッドで動かし、それぞれが鍵のファイルを別に開く（flock は開いたファイルごとに効く）。
"""

import contextlib
import io
import json
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))

import slots  # noqa: E402
import wait_next  # noqa: E402

HOLD = 0.3


class StateLock(unittest.TestCase):
    def setUp(self):
        self._td = tempfile.TemporaryDirectory()
        self.dir = Path(self._td.name)
        self.state = self.dir / "conductor_state.json"
        self.state.write_text(json.dumps({"history_id": "h1", "cue_mtime_ns": None, "last_slot": "llm_b"}),
                              encoding="utf-8")

    def tearDown(self):
        self._td.cleanup()

    def note(self, name):
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(slots.main(["note", name, "--dir", str(self.dir)]), 0)

    def read(self):
        return json.loads(self.state.read_text(encoding="utf-8"))

    def run_pair(self, first, second, read_done):
        """first を走らせ、それが状態を読んだところで second を走らせる。"""
        errors = []

        def wrap(fn):
            def run():
                try:
                    fn()
                except BaseException as e:  # surface failures from the thread
                    errors.append(e)
            return run

        t1 = threading.Thread(target=wrap(first))
        t1.start()
        self.assertTrue(read_done.wait(5))
        t2 = threading.Thread(target=wrap(second))
        t2.start()
        t1.join(10)
        t2.join(10)
        self.assertEqual(errors, [])

    def test_wait_next_write_during_note_is_kept(self):
        read_done = threading.Event()
        real = slots.read_state

        def slow_read(path):
            s = real(path)
            read_done.set()
            time.sleep(HOLD)
            return s

        orig = slots.read_state
        slots.read_state = slow_read
        try:
            self.run_pair(lambda: self.note("llm_a"),
                          lambda: wait_next.Watcher(str(self.dir)).save_state("h2", 7),
                          read_done)
        finally:
            slots.read_state = orig
        st = self.read()
        self.assertEqual((st["last_slot"], st["history_id"], st["cue_mtime_ns"]), ("llm_a", "h2", 7))

    def test_note_during_wait_next_write_is_kept(self):
        read_done = threading.Event()
        w = wait_next.Watcher(str(self.dir))
        real = w.load_state

        def slow_load():
            s = real()
            read_done.set()
            time.sleep(HOLD)
            return s

        w.load_state = slow_load
        self.run_pair(lambda: w.save_state("h2", 7), lambda: self.note("llm_a"), read_done)
        st = self.read()
        self.assertEqual((st["last_slot"], st["history_id"], st["cue_mtime_ns"]), ("llm_a", "h2", 7))

    def test_alternating_writes_keep_both(self):
        # 2 つが交互に何度も書いても、最後にはどちらの最後の値も残る。
        def notes():
            for i in range(40):
                self.note("llm_a" if i % 2 else "llm_b")

        def waits():
            w = wait_next.Watcher(str(self.dir))
            for i in range(40):
                w.save_state("w%d" % i, i)

        t1 = threading.Thread(target=notes)
        t2 = threading.Thread(target=waits)
        t1.start()
        t2.start()
        t1.join(30)
        t2.join(30)
        st = self.read()
        self.assertEqual((st["last_slot"], st["history_id"]), ("llm_a", "w39"))


if __name__ == "__main__":
    unittest.main()
