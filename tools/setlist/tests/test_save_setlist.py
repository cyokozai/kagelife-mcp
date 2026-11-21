"""save_setlist のテスト。setlist_log.jsonl と live/<history_id>.kage から、kagelife-shaders の presets/<name>/ を作る。"""

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
TOOL = HERE.parent
SAVE = TOOL / "save_setlist.sh"
sys.path.insert(0, str(TOOL))

import save_setlist  # noqa: E402

GOOD = """//kage:unit pixels

package main

var Time float
var Beat float

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
\treturn vec4(Beat, Time, 0, 1)
}
"""

AUDIO = """//kage:unit pixels

package main

var Time float
var AudioBass float

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
\treturn vec4(AudioBass, Time, 0, 1)
}
"""

LATE_DIRECTIVE = """// a
// b
// c
// d
// e
//kage:unit pixels
package main

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
\treturn color
}
"""

WRONG_TYPE = """//kage:unit pixels
package main

var Frame float

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
\treturn color
}
"""


class Base(unittest.TestCase):
    def setUp(self):
        self._td = tempfile.TemporaryDirectory()
        root = Path(self._td.name)
        self.cache = root / "cache"
        self.live = self.cache / "live"
        self.live.mkdir(parents=True)
        self.log = self.cache / "setlist_log.jsonl"
        self.dest = root / "kagelife-shaders"
        (self.dest / "presets").mkdir(parents=True)

    def tearDown(self):
        self._td.cleanup()

    def song(self, hid, title, artist="X", idea="", at="2026-11-21T12:00:00+00:00", source=GOOD, slot="llm_a"):
        with open(self.log, "a", encoding="utf-8") as f:
            f.write(json.dumps({"at": at, "slot": slot, "history_id": hid, "title": title, "artist": artist,
                                "idea": idea}, ensure_ascii=False) + "\n")
        if source is not None:
            (self.live / ("%s.kage" % hid)).write_text(source, encoding="utf-8")

    def save(self, *args, name="live1"):
        return subprocess.run(
            ["sh", str(SAVE), "--cache", str(self.cache), "--dest", str(self.dest), "--name", name, *args],
            capture_output=True,
            text=True,
            timeout=20,
        )

    def out(self, name="live1"):
        return self.dest / "presets" / name

    def index(self, name="live1"):
        return json.loads((self.out(name) / "index.json").read_text(encoding="utf-8"))


class Saves(Base):
    def test_copies_in_log_order_with_index_like_stage_a(self):
        self.song("h1", "Intro", "A", "blue rings")
        self.song("h2", "Second Song", "B", "red bars")
        p = self.save()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.index(), [
            {"title": "Intro", "artist": "A", "file": "01_intro.kage", "idea": "blue rings"},
            {"title": "Second Song", "artist": "B", "file": "02_second_song.kage", "idea": "red bars"},
        ])
        self.assertEqual((self.out() / "01_intro.kage").read_text(encoding="utf-8"), GOOD)
        self.assertEqual(sorted(x.name for x in self.out().iterdir()),
                         ["01_intro.kage", "02_second_song.kage", "index.json"])

    def test_same_history_id_keeps_the_last_record_at_its_first_place(self):
        self.song("h1", "One", idea="first try")
        self.song("h2", "Two")
        self.song("h1", "One", idea="rewrite", slot="llm_b")
        p = self.save()
        self.assertEqual(p.returncode, 0, p.stderr)
        idx = self.index()
        self.assertEqual([(r["title"], r["idea"]) for r in idx], [("One", "rewrite"), ("Two", "")])
        self.assertEqual([r["file"] for r in idx], ["01_one.kage", "02_two.kage"])

    def test_since_keeps_only_later_records(self):
        self.song("h1", "Old", at="2026-11-20T10:00:00+00:00")
        self.song("h2", "New", at="2026-11-21T10:00:00+00:00")
        p = self.save("--since", "2026-11-21T00:00:00+09:00")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual([r["title"] for r in self.index()], ["New"])

    def test_since_accepts_z_suffix(self):
        self.song("h1", "Old", at="2026-11-20T10:00:00+00:00")
        self.song("h2", "New", at="2026-11-21T10:00:00+00:00")
        p = self.save("--since", "2026-11-21T00:00:00Z")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual([r["title"] for r in self.index()], ["New"])

    def test_song_missing_in_live_is_skipped_with_warning(self):
        self.song("h1", "One")
        self.song("h2", "Gone", source=None)
        self.song("h3", "Three")
        p = self.save()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual([r["file"] for r in self.index()], ["01_one.kage", "02_three.kage"])
        self.assertIn("h2", p.stderr)
        self.assertIn("WARN", p.stderr)

    def test_broken_log_line_is_skipped_with_warning(self):
        self.song("h1", "One")
        with open(self.log, "a", encoding="utf-8") as f:
            f.write("{not json\n")
        p = self.save()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(len(self.index()), 1)
        self.assertIn("WARN", p.stderr)

    def test_unsafe_history_id_is_skipped(self):
        self.song("h1", "One")
        with open(self.log, "a", encoding="utf-8") as f:
            f.write(json.dumps({"at": "2026-11-21T12:00:00+00:00", "slot": "llm_a", "history_id": "../x",
                                "title": "Bad", "artist": "", "idea": ""}) + "\n")
        p = self.save()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual([r["title"] for r in self.index()], ["One"])


class Stops(Base):
    def test_existing_preset_folder_is_not_overwritten(self):
        self.song("h1", "One")
        self.out().mkdir()
        (self.out() / "keep.kage").write_text("x", encoding="utf-8")
        p = self.save()
        self.assertEqual(p.returncode, 2)
        self.assertEqual([x.name for x in self.out().iterdir()], ["keep.kage"])

    def test_nothing_to_save_creates_no_folder(self):
        self.song("h1", "Gone", source=None)
        p = self.save()
        self.assertEqual(p.returncode, 1)
        self.assertFalse(self.out().exists())

    def test_missing_log_is_nothing_to_save(self):
        p = self.save()
        self.assertEqual(p.returncode, 1)
        self.assertFalse(self.out().exists())

    def test_missing_dest_is_usage_error(self):
        self.song("h1", "One")
        p = subprocess.run(["sh", str(SAVE), "--cache", str(self.cache), "--dest", str(self.dest / "nope"),
                            "--name", "live1"], capture_output=True, text=True, timeout=20)
        self.assertEqual(p.returncode, 2)
        self.assertFalse((self.dest / "nope").exists())

    def test_bad_name_is_usage_error(self):
        self.song("h1", "One")
        for bad in ["../x", "a/b", ".hidden", ""]:
            p = self.save(name=bad)
            self.assertEqual(p.returncode, 2, bad)
        self.assertEqual(list((self.dest / "presets").iterdir()), [])

    def test_name_is_required(self):
        p = subprocess.run(["sh", str(SAVE), "--cache", str(self.cache), "--dest", str(self.dest)],
                           capture_output=True, text=True, timeout=20)
        self.assertEqual(p.returncode, 2)


class Slug(unittest.TestCase):
    def test_ascii_lower_digits_underscore(self):
        self.assertEqual(save_setlist.slug("Hello, World! 2"), "hello_world_2")

    def test_accents_are_folded(self):
        self.assertEqual(save_setlist.slug("Café Déjà Vu"), "cafe_deja_vu")

    def test_japanese_only_falls_back_to_history_id(self):
        self.assertEqual(save_setlist.slug("夜に駆ける", "Spotify:Track-123"), "spotify_track_123")

    def test_mixed_keeps_ascii_part(self):
        self.assertEqual(save_setlist.slug("夜に駆ける (Remix)", "h9"), "remix")

    def test_nothing_ascii_at_all(self):
        self.assertEqual(save_setlist.slug("夜", "曲"), "song")

    def test_long_title_is_cut(self):
        s = save_setlist.slug("a" * 100)
        self.assertLessEqual(len(s), 40)
        self.assertRegex(s, r"^[a-z0-9_]+$")

    def test_file_uses_fallback_slug(self):
        b = Base("run")
        b.setUp()
        try:
            b.song("Track-7", "夜に駆ける")
            p = b.save()
            self.assertEqual(p.returncode, 0, p.stderr)
            self.assertEqual(b.index()[0]["file"], "01_track_7.kage")
            self.assertEqual(b.index()[0]["title"], "夜に駆ける")
        finally:
            b.tearDown()


class ShadersRepoCheck(Base):
    def test_good_files_have_no_warning(self):
        self.song("h1", "One")
        p = self.save()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertNotIn("kagelife-shaders", p.stderr)

    def test_audio_uniform_late_directive_and_wrong_type_are_listed_after_copy(self):
        self.song("h1", "One")
        self.song("h2", "Audio", source=AUDIO)
        self.song("h3", "Late", source=LATE_DIRECTIVE)
        self.song("h4", "Typed", source=WRONG_TYPE)
        p = self.save()
        self.assertEqual(p.returncode, 0, p.stderr)
        # copied anyway
        self.assertEqual(len(self.index()), 4)
        err = p.stderr
        self.assertIn("02_audio.kage", err)
        self.assertIn("AudioBass", err)
        self.assertIn("03_late.kage", err)
        self.assertIn("//kage:unit pixels", err)
        self.assertIn("04_typed.kage", err)
        self.assertIn("Frame", err)
        self.assertNotIn("01_one.kage:", err)

    def test_problems_function(self):
        self.assertEqual(save_setlist.shaders_repo_problems(GOOD), [])
        self.assertTrue(any("AudioBass" in m for m in save_setlist.shaders_repo_problems(AUDIO)))
        self.assertTrue(any("kage:unit" in m for m in save_setlist.shaders_repo_problems(LATE_DIRECTIVE)))


class Python39(unittest.TestCase):
    def test_sources_parse_as_python_3_9(self):
        import ast
        for p in [TOOL / "save_setlist.py"]:
            ast.parse(p.read_text(encoding="utf-8"), filename=str(p), feature_version=(3, 9))


if __name__ == "__main__":
    unittest.main()
