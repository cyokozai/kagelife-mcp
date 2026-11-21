"""mode.sh のテスト。kagelife_mode（start.sh が書く 1 語）を読み、無い・読めない・知らない語ならフル構成とみなす。"""

import os
import subprocess
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
MODE = HERE.parent / "mode.sh"


class Mode(unittest.TestCase):
    def setUp(self):
        self._td = tempfile.TemporaryDirectory()
        self.dir = Path(self._td.name)
        self.file = self.dir / "kagelife_mode"

    def tearDown(self):
        self._td.cleanup()

    def run_mode(self, *args, env=None):
        return subprocess.run(["sh", str(MODE), *args], capture_output=True, text=True, timeout=10, env=env)

    def mode(self):
        p = self.run_mode("--dir", str(self.dir))
        self.assertEqual(p.returncode, 0, p.stderr)
        return p.stdout.strip()

    def test_known_words(self):
        for w in ("human", "ai", "full"):
            self.file.write_text(w + "\n", encoding="utf-8")
            self.assertEqual(self.mode(), w)

    def test_spaces_and_crlf_are_ignored(self):
        self.file.write_text("  ai \r\n", encoding="utf-8")
        self.assertEqual(self.mode(), "ai")

    def test_missing_file_is_full(self):
        self.assertEqual(self.mode(), "full")

    def test_unknown_word_is_full(self):
        self.file.write_text("AI\n", encoding="utf-8")
        self.assertEqual(self.mode(), "full")
        self.file.write_text("", encoding="utf-8")
        self.assertEqual(self.mode(), "full")

    def test_unreadable_file_is_full(self):
        self.file.mkdir()  # a folder cannot be read as the file
        self.assertEqual(self.mode(), "full")

    def test_kagelife_cache_env_is_the_default_dir(self):
        self.file.write_text("ai\n", encoding="utf-8")
        env = dict(os.environ, KAGELIFE_CACHE=str(self.dir))
        p = self.run_mode(env=env)
        self.assertEqual((p.returncode, p.stdout.strip()), (0, "ai"))

    def test_bad_usage(self):
        self.assertEqual(self.run_mode("--dir").returncode, 2)
        self.assertEqual(self.run_mode("--x", "y").returncode, 2)


if __name__ == "__main__":
    unittest.main()
