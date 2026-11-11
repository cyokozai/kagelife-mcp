"""wait_next のテスト。一時フォルダに now_playing.json と cue.json の変化を作り、出力と終了コードを確かめる。

標準ライブラリの unittest だけで動く。
"""

import json
import os
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
SH = HERE.parent / "wait_next.sh"

FAST = ["--interval", "0.05"]


def song(history_id, title="Song A", content_id="c1"):
    return {
        "title": title,
        "artist": "Artist A",
        "bpm": 128.0,
        "key": "8A",
        "genre": "Techno",
        "played_at": "2026-11-21T21:00:00",
        "detected_at": "2026-11-21T12:00:03+00:00",
        "source": "rekordbox-history",
        "history_id": history_id,
        "content_id": content_id,
    }


def write_json(path, obj):
    tmp = path.with_name(path.name + ".u3cd-tmp")
    tmp.write_text(json.dumps(obj), encoding="utf-8")
    os.replace(tmp, path)


def bump_mtime(path, delta=5.0):
    st = path.stat()
    os.utime(path, (st.st_atime + delta, st.st_mtime + delta))


class Base(unittest.TestCase):
    def setUp(self):
        self._td = tempfile.TemporaryDirectory()
        self.dir = Path(self._td.name)
        self.np = self.dir / "now_playing.json"
        self.cue = self.dir / "cue.json"
        self.state = self.dir / "conductor_state.json"

    def tearDown(self):
        self._td.cleanup()

    def start(self, *extra, env=None):
        argv = ["sh", str(SH), "--dir", str(self.dir), *FAST, *extra]
        return subprocess.Popen(
            argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env
        )

    def run_once(self, *extra, timeout="0.4"):
        p = self.start("--timeout", timeout, *extra)
        out, err = p.communicate(timeout=20)
        return p.returncode, out, err

    def lines(self, out):
        return [json.loads(l) for l in out.splitlines() if l.strip()]


class FirstRun(Base):
    def test_no_state_with_song_emits_start(self):
        write_json(self.np, song("h1"))
        rc, out, err = self.run_once()
        self.assertEqual(rc, 0, err)
        [ev] = self.lines(out)
        self.assertEqual(ev["changed"], ["start"])
        self.assertEqual(ev["now_playing"]["history_id"], "h1")
        self.assertEqual(ev["now_playing"]["title"], "Song A")
        st = json.loads(self.state.read_text())
        self.assertEqual(st["history_id"], "h1")

    def test_no_state_no_song_waits_then_times_out(self):
        rc, out, err = self.run_once()
        self.assertEqual(rc, 3, err)
        self.assertEqual(out.strip(), "")

    def test_default_dir_is_home_cache(self):
        home = self.dir / "home"
        cache = home / ".cache" / "kagelife"
        cache.mkdir(parents=True)
        write_json(cache / "now_playing.json", song("h9"))
        env = dict(os.environ, HOME=str(home))
        argv = ["sh", str(SH), *FAST, "--timeout", "0.4"]
        p = subprocess.run(argv, capture_output=True, text=True, env=env, timeout=20)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertTrue((cache / "conductor_state.json").exists())


class NoChange(Base):
    def test_same_history_id_does_not_fire(self):
        write_json(self.np, song("h1"))
        self.run_once()  # start
        write_json(self.np, song("h1", title="retagged"))
        rc, out, err = self.run_once()
        self.assertEqual(rc, 3, err)
        self.assertEqual(out.strip(), "")

    def test_broken_json_is_ignored(self):
        write_json(self.np, song("h1"))
        self.run_once()
        self.np.write_text("{not json", encoding="utf-8")
        rc, out, err = self.run_once()
        self.assertEqual(rc, 3, err)
        self.assertEqual(out.strip(), "")


class Changes(Base):
    def test_song_change_while_waiting(self):
        write_json(self.np, song("h1"))
        self.run_once()
        p = self.start("--timeout", "5")
        time.sleep(0.3)
        write_json(self.np, song("h2", title="Song B"))
        out, err = p.communicate(timeout=20)
        self.assertEqual(p.returncode, 0, err)
        [ev] = self.lines(out)
        self.assertEqual(ev["changed"], ["song"])
        self.assertEqual(ev["now_playing"]["title"], "Song B")
        self.assertEqual(ev["previous_history_id"], "h1")

    def test_song_appears_after_empty_start(self):
        self.run_once()  # records empty state
        write_json(self.np, song("h1"))
        rc, out, err = self.run_once()
        self.assertEqual(rc, 0, err)
        self.assertEqual(self.lines(out)[0]["changed"], ["song"])

    def test_cue_mtime_change_fires_with_current_song(self):
        write_json(self.np, song("h1"))
        self.cue.write_text('{"at": 1}', encoding="utf-8")
        self.run_once()  # start; cue is baseline
        rc, out, err = self.run_once()
        self.assertEqual(rc, 3, "cue baseline must not fire: " + out)
        bump_mtime(self.cue)
        rc, out, err = self.run_once()
        self.assertEqual(rc, 0, err)
        [ev] = self.lines(out)
        self.assertEqual(ev["changed"], ["cue"])
        self.assertEqual(ev["now_playing"]["history_id"], "h1")

    def test_cue_content_does_not_matter(self):
        write_json(self.np, song("h1"))
        self.run_once()
        self.cue.write_text("anything, not json", encoding="utf-8")
        rc, out, err = self.run_once()
        self.assertEqual(rc, 0, err)
        self.assertEqual(self.lines(out)[0]["changed"], ["cue"])

    def test_song_and_cue_together(self):
        write_json(self.np, song("h1"))
        self.cue.write_text("{}", encoding="utf-8")
        self.run_once()
        write_json(self.np, song("h2"))
        bump_mtime(self.cue)
        rc, out, err = self.run_once()
        self.assertEqual(rc, 0, err)
        self.assertEqual(self.lines(out)[0]["changed"], ["song", "cue"])

    def test_reset_forgets_state(self):
        write_json(self.np, song("h1"))
        self.run_once()
        rc, out, err = self.run_once("--reset")
        self.assertEqual(rc, 0, err)
        self.assertEqual(self.lines(out)[0]["changed"], ["start"])


class Follow(Base):
    def test_follow_prints_one_line_per_change(self):
        write_json(self.np, song("h1"))
        p = self.start("--follow", "--timeout", "2")
        time.sleep(0.3)
        write_json(self.np, song("h2"))
        time.sleep(0.3)
        write_json(self.np, song("h3"))
        out, err = p.communicate(timeout=20)
        evs = self.lines(out)
        self.assertEqual([e["changed"] for e in evs], [["start"], ["song"], ["song"]])
        self.assertEqual(evs[-1]["now_playing"]["history_id"], "h3")


class Usage(Base):
    def test_bad_option(self):
        rc, out, err = self.run_once("--nope")
        self.assertEqual(rc, 2)


if __name__ == "__main__":
    unittest.main()
