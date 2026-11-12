"""push_now_playing のテスト。ssh を偽物に差し替え（KAGELIFE_PUSH_SSH）、送られた中身と回数を確かめる。

偽の ssh はオプションとホストを記録し、最後の引数（リモートで走らせるコマンド）をこの機械の sh で実行する。
"""

import json
import os
import stat
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
SH = HERE.parent / "push_now_playing.sh"

FAKE_SSH = """#!/bin/sh
# fake ssh: log argv, fail if $FAKE_FAIL exists, else run the last arg locally.
# $FAKE_LATE_FAIL: run the remote command (it lands), then report failure as if the link dropped.
# $FAKE_CUT: pass only the first 5 bytes of stdin, as if the stream was cut in transit.
printf '%s\\n' "$*" >> "$FAKE_LOG"
if [ -e "$FAKE_FAIL" ]; then
  cat > /dev/null
  exit 255
fi
for a in "$@"; do last=$a; done
if [ -e "$FAKE_LATE_FAIL" ]; then
  sh -c "$last"
  exit 255
fi
if [ -e "$FAKE_CUT" ]; then
  head -c 5 | sh -c "$last"
  exit $?
fi
exec sh -c "$last"
"""


class Base(unittest.TestCase):
    def setUp(self):
        self._td = tempfile.TemporaryDirectory()
        d = Path(self._td.name)
        self.src_dir = d / "mac"
        self.src_dir.mkdir()
        self.src = self.src_dir / "now_playing.json"
        self.remote = d / "remote" / "kage cache"  # space on purpose
        self.log = d / "ssh.log"
        self.fail_flag = d / "fail.flag"
        self.late_fail = d / "late_fail.flag"
        self.cut = d / "cut.flag"
        self.fake = d / "u3cd-fake-ssh"
        self.fake.write_text(FAKE_SSH)
        self.fake.chmod(self.fake.stat().st_mode | stat.S_IEXEC)
        self.env = dict(
            os.environ,
            KAGELIFE_PUSH_SSH=str(self.fake),
            FAKE_LOG=str(self.log),
            FAKE_FAIL=str(self.fail_flag),
            FAKE_LATE_FAIL=str(self.late_fail),
            FAKE_CUT=str(self.cut),
        )

    def tearDown(self):
        self._td.cleanup()

    def put(self, obj):
        tmp = self.src.with_name("u3cd-tmp")
        tmp.write_text(json.dumps(obj), encoding="utf-8")
        os.replace(tmp, self.src)

    def argv(self, *extra):
        return ["sh", str(SH), "--src", str(self.src), "--remote-dir", str(self.remote),
                "--interval", "0.05", "--retry", "0.2", *extra]

    def run_for(self, seconds, *extra):
        return subprocess.run(self.argv("--timeout", str(seconds), *extra),
                              capture_output=True, text=True, env=self.env, timeout=30)

    def start(self, seconds, *extra):
        return subprocess.Popen(self.argv("--timeout", str(seconds), *extra),
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=self.env)

    def calls(self):
        if not self.log.exists():
            return []
        return self.log.read_text().splitlines()

    def dest(self):
        return self.remote / "now_playing.json"

    def remote_hid(self):
        try:
            return json.loads(self.dest().read_text())["history_id"]
        except (OSError, ValueError, KeyError):
            return None

    def wait_for(self, cond, limit=5.0):
        end = time.monotonic() + limit
        while time.monotonic() < end:
            if cond():
                return
            time.sleep(0.02)
        self.fail("condition not met within %.1fs" % limit)


class Send(Base):
    def test_once_sends_content_atomically(self):
        self.put({"title": "A", "history_id": "h1"})
        p = self.run_for(5, "--once")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.dest().read_bytes(), self.src.read_bytes())
        self.assertEqual(sorted(x.name for x in self.remote.iterdir()), ["now_playing.json"])

    def test_default_host_and_ssh_options(self):
        self.put({"history_id": "h1"})
        self.run_for(5, "--once")
        [line] = self.calls()
        self.assertTrue(line.startswith("-T -o BatchMode=yes -o ConnectTimeout=5 vj "), line)

    def test_host_argument(self):
        self.put({"history_id": "h1"})
        self.run_for(5, "--once", "vjbox")
        self.assertIn(" vjbox ", self.calls()[0])

    def test_same_content_is_not_sent_twice(self):
        self.put({"history_id": "h1"})
        p = self.start(1.0)
        time.sleep(0.3)
        self.put({"history_id": "h1"})  # rewritten, same bytes
        p.communicate(timeout=30)
        self.assertEqual(len(self.calls()), 1)

    def test_change_is_sent(self):
        self.put({"history_id": "h1"})
        p = self.start(1.5)
        time.sleep(0.4)
        self.put({"history_id": "h2", "title": "B"})
        p.communicate(timeout=30)
        self.assertEqual(len(self.calls()), 2)
        self.assertEqual(json.loads(self.dest().read_text())["history_id"], "h2")

    def test_broken_json_is_not_sent(self):
        self.src.write_text("{half", encoding="utf-8")
        p = self.run_for(0.4)
        self.assertEqual(self.calls(), [])
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_missing_source_waits(self):
        p = self.run_for(0.4)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.calls(), [])


class Retry(Base):
    def test_failure_is_retried_after_interval(self):
        self.put({"history_id": "h1"})
        self.fail_flag.write_text("x")
        p = self.start(2.0)
        time.sleep(0.5)
        self.fail_flag.unlink()
        out, err = p.communicate(timeout=30)
        self.assertGreaterEqual(len(self.calls()), 2)
        self.assertEqual(json.loads(self.dest().read_text())["history_id"], "h1")
        # after the success it is not sent again
        n = len(self.calls())
        time.sleep(0.1)
        self.assertEqual(len(self.calls()), n)

    def test_failure_forgets_what_was_sent(self):
        # B lands on the VJ machine but ssh reports failure; then the Mac goes back to A.
        # A must be sent again, or the VJ machine keeps showing B.
        a = {"history_id": "h1", "title": "A"}
        self.put(a)
        p = self.start(3.0)
        self.wait_for(lambda: self.remote_hid() == "h1")
        self.late_fail.write_text("x")
        self.put({"history_id": "h2", "title": "B"})
        self.wait_for(lambda: self.remote_hid() == "h2" and len(self.calls()) >= 2)
        self.late_fail.unlink()
        self.put(a)
        p.communicate(timeout=30)
        self.assertEqual(self.remote_hid(), "h1")

    def test_once_reports_failure(self):
        self.put({"history_id": "h1"})
        self.fail_flag.write_text("x")
        p = self.run_for(5, "--once")
        self.assertEqual(p.returncode, 1)
        self.assertFalse(self.dest().exists())


class RemoteDir(Base):
    def test_tilde_dir_expands_on_remote(self):
        home = Path(self._td.name) / "rhome"
        home.mkdir()
        self.put({"history_id": "h1"})
        env = dict(self.env, HOME=str(home))
        p = subprocess.run(
            ["sh", str(SH), "--src", str(self.src), "--remote-dir", "~/.cache/kagelife", "--once"],
            capture_output=True, text=True, env=env, timeout=30)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertTrue((home / ".cache" / "kagelife" / "now_playing.json").exists())


if __name__ == "__main__":
    unittest.main()
