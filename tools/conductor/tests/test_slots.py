"""note_written / back_slot のテスト。最後に書いた予約名を conductor_state.json に残し、次の書く先を決める。"""

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
TOOL = HERE.parent
NOTE = TOOL / "note_written.sh"
BACK = TOOL / "back_slot.sh"
WAIT = TOOL / "wait_next.sh"


class Base(unittest.TestCase):
    def setUp(self):
        self._td = tempfile.TemporaryDirectory()
        self.dir = Path(self._td.name)
        self.state = self.dir / "conductor_state.json"
        self.np = self.dir / "now_playing.json"

    def tearDown(self):
        self._td.cleanup()

    def sh(self, script, *args):
        return subprocess.run(
            ["sh", str(script), "--dir", str(self.dir), *args],
            capture_output=True,
            text=True,
            timeout=20,
        )

    def write_song(self, hid):
        self.np.write_text(json.dumps({"title": "T", "history_id": hid}), encoding="utf-8")

    def wait(self, *args):
        return subprocess.run(
            ["sh", str(WAIT), "--dir", str(self.dir), "--interval", "0.05", "--timeout", "0.3", *args],
            capture_output=True,
            text=True,
            timeout=20,
        )


class NoteWritten(Base):
    def test_records_last_slot(self):
        p = self.sh(NOTE, "llm_a")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(json.loads(self.state.read_text())["last_slot"], "llm_a")

    def test_keeps_other_state_keys(self):
        self.state.write_text(json.dumps({"history_id": "h1", "cue_mtime_ns": 5}), encoding="utf-8")
        self.sh(NOTE, "llm_b")
        st = json.loads(self.state.read_text())
        self.assertEqual(st["history_id"], "h1")
        self.assertEqual(st["cue_mtime_ns"], 5)
        self.assertEqual(st["last_slot"], "llm_b")

    def test_rejects_non_reserved_name(self):
        p = self.sh(NOTE, "04_warp")
        self.assertEqual(p.returncode, 2)
        self.assertFalse(self.state.exists())

    def test_requires_a_name(self):
        p = self.sh(NOTE)
        self.assertEqual(p.returncode, 2)


class BackSlot(Base):
    def test_active_llm_a_gives_llm_b(self):
        self.sh(NOTE, "llm_b")  # even if llm_b was written last
        p = self.sh(BACK, "llm_a")
        self.assertEqual((p.returncode, p.stdout.strip()), (0, "llm_b"))

    def test_active_llm_b_gives_llm_a(self):
        p = self.sh(BACK, "llm_b")
        self.assertEqual(p.stdout.strip(), "llm_a")

    def test_other_active_without_record_gives_llm_a(self):
        p = self.sh(BACK, "04_warp")
        self.assertEqual((p.returncode, p.stdout.strip()), (0, "llm_a"))

    def test_other_active_uses_the_one_not_written_last(self):
        self.sh(NOTE, "llm_a")
        self.assertEqual(self.sh(BACK, "04_warp").stdout.strip(), "llm_b")
        self.sh(NOTE, "llm_b")
        self.assertEqual(self.sh(BACK, "04_warp").stdout.strip(), "llm_a")

    def test_requires_active(self):
        p = self.sh(BACK)
        self.assertEqual(p.returncode, 2)


class StateSharedWithWaitNext(Base):
    def test_wait_next_keeps_last_slot(self):
        self.sh(NOTE, "llm_a")
        self.write_song("h1")
        self.assertEqual(self.wait().returncode, 0)  # start
        self.write_song("h2")
        self.assertEqual(self.wait().returncode, 0)  # song
        st = json.loads(self.state.read_text())
        self.assertEqual(st["history_id"], "h2")
        self.assertEqual(st["last_slot"], "llm_a")

    def test_reset_keeps_last_slot(self):
        self.sh(NOTE, "llm_b")
        self.write_song("h1")
        self.wait()
        p = self.wait("--reset")
        self.assertEqual(json.loads(p.stdout)["changed"], ["start"])
        self.assertEqual(json.loads(self.state.read_text())["last_slot"], "llm_b")

    def test_wait_next_creates_live_dir(self):
        self.wait()
        self.assertTrue((self.dir / "live").is_dir())


if __name__ == "__main__":
    unittest.main()
