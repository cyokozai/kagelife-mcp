"""start.sh / stop.sh のテスト。kagelife と docker を偽物に差し替え、引数と環境変数の扱いを確かめる。

偽の kagelife は受け取った引数を記録して眠り続ける。偽の docker（KAGELIFE_DOCKER）は引数を記録し、
`container inspect` は $FAKE_RUNNING があれば「動いている」、無ければ「無い」と答える。
"""

import os
import shutil
import stat
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
START = HERE.parent / "start.sh"
STOP = HERE.parent / "stop.sh"

FAKE_KAGELIFE = """#!/bin/sh
printf '%s\\n' "$*" > "$FAKE_KL_ARGS"
printf '%s\\n' "${KAGELIFE_CONTROL_FILE:-unset}" > "$FAKE_KL_ARGS.env"
if [ -e "$FAKE_KL_DIE" ]; then
  echo "fake kagelife: dying" >&2
  exit 1
fi
trap 'exit 0' TERM INT
while :; do sleep 1; done
"""

FAKE_DOCKER = """#!/bin/sh
printf '%s\\n' "$*" >> "$FAKE_DOCKER_LOG"
case "$1 $2" in
  "image inspect")
    [ -e "$FAKE_NO_IMAGE" ] && exit 1
    exit 0 ;;
  "container inspect")
    if [ -e "$FAKE_RUNNING" ]; then echo true; exit 0; fi
    exit 1 ;;
esac
exit 0
"""

FAKE_MCP = """#!/bin/sh
printf '%s\\n' "$*" > "$FAKE_MCP_ARGS"
printf '%s\\n' "${KAGELIFE_SPOTIFY_CLIENT_ID:-unset}" > "$FAKE_MCP_ARGS.env"
if [ -e "$FAKE_MCP_DIE" ]; then
  echo "fake mcp: dying" >&2
  exit 1
fi
trap 'exit 0' TERM INT
while :; do sleep 1; done
"""

FAKE_PGREP = """#!/bin/sh
[ -e "$FAKE_DECK" ]
"""

SECRET = "fake-client-id-0123456789"


def write_exec(path: Path, body: str) -> None:
    path.write_text(body)
    path.chmod(path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)


class ScriptTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="macA-test-"))
        self.shaders = self.tmp / "shaders"
        self.shaders.mkdir()
        self.cache = self.tmp / "cache"
        self.config = self.tmp / "config"
        self.config.mkdir()
        self.kl = self.tmp / "kagelife"
        self.docker = self.tmp / "docker"
        write_exec(self.kl, FAKE_KAGELIFE)
        write_exec(self.docker, FAKE_DOCKER)
        (self.config / "spotify-token.json").write_text("{}")
        self.mcp = self.tmp / "kagelife-mcp"
        self.pgrep = self.tmp / "pgrep"
        write_exec(self.mcp, FAKE_MCP)
        write_exec(self.pgrep, FAKE_PGREP)
        self.mcp_args = self.tmp / "mcp_args"
        self.kl_args = self.tmp / "kl_args"
        self.docker_log = self.tmp / "docker_log"
        self.env = {
            "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
            "HOME": str(self.tmp / "home"),
            "FAKE_KL_ARGS": str(self.kl_args),
            "FAKE_KL_DIE": str(self.tmp / "die"),
            "FAKE_DOCKER_LOG": str(self.docker_log),
            "FAKE_RUNNING": str(self.tmp / "running"),
            "FAKE_NO_IMAGE": str(self.tmp / "noimage"),
            "KAGELIFE_DOCKER": str(self.docker),
            "KAGELIFE_BIN": str(self.kl),
            "KAGELIFE_SHADERS": str(self.shaders),
            "KAGELIFE_CACHE": str(self.cache),
            "KAGELIFE_CONFIG": str(self.config),
            "KAGELIFE_MCP_BIN": str(self.mcp),
            "KAGELIFE_PGREP": str(self.pgrep),
            "FAKE_MCP_ARGS": str(self.mcp_args),
            "FAKE_MCP_DIE": str(self.tmp / "mcpdie"),
            "FAKE_DECK": str(self.tmp / "deck"),
        }

    def tearDown(self):
        # Never leave a fake kagelife running.
        for name in ("kagelife.pid", "nowplaying.pid"):
            pid_file = self.cache / name
            if pid_file.exists():
                try:
                    os.kill(int(pid_file.read_text().strip()), 15)
                except (ValueError, ProcessLookupError):
                    pass
        shutil.rmtree(self.tmp, ignore_errors=True)

    def run_sh(self, script, *args, env=None):
        e = dict(self.env)
        if env:
            e.update(env)
        return subprocess.run(["sh", str(script), *args], env=e, capture_output=True, text=True, timeout=30)

    def docker_calls(self):
        if not self.docker_log.exists():
            return []
        return self.docker_log.read_text().splitlines()

    def pid(self):
        return int((self.cache / "kagelife.pid").read_text().strip())

    def alive(self, pid):
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return False
        # A zombie still answers kill 0; check its state where /proc exists.
        stat_file = Path("/proc/%d/stat" % pid)
        if stat_file.exists():
            return stat_file.read_text().split()[2] != "Z"
        return True

    def wait_args(self):
        for _ in range(50):
            if self.kl_args.exists() and self.kl_args.read_text():
                return self.kl_args.read_text().strip()
            time.sleep(0.1)
        self.fail("fake kagelife was not started")

    # --- start.sh ---

    def test_missing_client_id_stops_before_starting_anything(self):
        r = self.run_sh(START)
        self.assertEqual(r.returncode, 1, r.stderr)
        self.assertIn("KAGELIFE_SPOTIFY_CLIENT_ID", r.stderr)
        self.assertFalse((self.cache / "kagelife.pid").exists())
        self.assertEqual(self.docker_calls(), [])

    def test_missing_shaders_dir_is_an_error(self):
        r = self.run_sh(START, "--no-nowplaying", env={"KAGELIFE_SHADERS": str(self.tmp / "nope")})
        self.assertEqual(r.returncode, 1)
        self.assertIn("nope", r.stderr)
        self.assertFalse((self.cache / "kagelife.pid").exists())

    def test_unknown_option_is_usage_error(self):
        r = self.run_sh(START, "--bogus")
        self.assertEqual(r.returncode, 2)
        self.assertIn("usage", r.stderr.lower())

    def test_no_nowplaying_starts_kagelife_with_llm_slots_and_skips_docker(self):
        r = self.run_sh(START, "--no-nowplaying")
        self.assertEqual(r.returncode, 0, r.stderr)
        args = self.wait_args()
        self.assertEqual(args, "-shaders %s -llm-slots llm_a,llm_b" % self.shaders)
        self.assertTrue(self.alive(self.pid()))
        self.assertTrue((self.cache / "kagelife.log").exists())
        self.assertEqual(self.docker_calls(), [])

    def test_extra_args_after_double_dash_go_to_kagelife(self):
        r = self.run_sh(START, "--no-nowplaying", "--", "-display", "2", "-fullscreen")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.wait_args(),
                         "-shaders %s -llm-slots llm_a,llm_b -display 2 -fullscreen" % self.shaders)

    def test_audio_option_is_passed_to_kagelife(self):
        r = self.run_sh(START, "--no-nowplaying", "--audio", "2")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.wait_args(), "-shaders %s -llm-slots llm_a,llm_b -audio 2" % self.shaders)

    def test_audio_line_and_extra_args(self):
        r = self.run_sh(START, "--no-nowplaying", "--audio", "default", "--audio-line", "--", "-display", "2")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.wait_args(),
                         "-shaders %s -llm-slots llm_a,llm_b -audio default -audio-line -display 2" % self.shaders)

    def test_audio_env_is_passed_to_kagelife(self):
        r = self.run_sh(START, "--no-nowplaying", env={"KAGELIFE_AUDIO": "Mic", "KAGELIFE_AUDIO_LINE": "1"})
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.wait_args(), "-shaders %s -llm-slots llm_a,llm_b -audio Mic -audio-line" % self.shaders)

    def test_audio_line_without_audio_is_an_error(self):
        r = self.run_sh(START, "--no-nowplaying", "--audio-line")
        self.assertEqual(r.returncode, 1)
        self.assertIn("--audio-line needs --audio", r.stderr)
        self.assertFalse((self.cache / "kagelife.pid").exists())

    def test_without_audio_no_audio_flag(self):
        r = self.run_sh(START, "--no-nowplaying")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertNotIn("-audio", self.wait_args())

    def test_options_override_environment(self):
        other = self.tmp / "other-shaders"
        other.mkdir()
        other_cache = self.tmp / "other-cache"
        r = self.run_sh(START, "--no-nowplaying", "--shaders", str(other), "--cache", str(other_cache))
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("-shaders %s " % other, self.wait_args())
        self.assertTrue((other_cache / "kagelife.pid").exists())
        os.kill(int((other_cache / "kagelife.pid").read_text()), 15)

    def test_control_file_env_is_passed_through(self):
        r = self.run_sh(START, "--no-nowplaying", env={"KAGELIFE_CONTROL_FILE": str(self.tmp / "c.json")})
        self.assertEqual(r.returncode, 0, r.stderr)
        self.wait_args()
        self.assertEqual((self.tmp / "kl_args.env").read_text().strip(), str(self.tmp / "c.json"))

    def test_kagelife_that_dies_at_once_is_reported(self):
        (self.tmp / "die").touch()
        r = self.run_sh(START, "--no-nowplaying")
        self.assertEqual(r.returncode, 1)
        self.assertIn("dying", r.stderr)
        self.assertFalse((self.cache / "kagelife.pid").exists())

    def test_refuses_to_start_twice(self):
        self.assertEqual(self.run_sh(START, "--no-nowplaying").returncode, 0)
        first = self.pid()
        r = self.run_sh(START, "--no-nowplaying")
        self.assertEqual(r.returncode, 1)
        self.assertIn("stop.sh", r.stderr)
        self.assertEqual(self.pid(), first)

    def test_nowplaying_container_args_and_client_id_never_printed(self):
        r = self.run_sh(START, env={"KAGELIFE_NOWPLAYING_RUNNER": "docker", "KAGELIFE_SPOTIFY_CLIENT_ID": SECRET, "KAGELIFE_START_CHECK": "0"})
        self.assertEqual(r.returncode, 0, r.stderr)
        runs = [c for c in self.docker_calls() if c.startswith("run ")]
        self.assertEqual(runs, [
            "run -d --name kagelife-nowplaying-spotify -e KAGELIFE_SPOTIFY_CLIENT_ID"
            " -v %s:/config -v %s:/out kagelife-nowplaying-spotify"
            " --token /config/spotify-token.json --out /out/now_playing.json --interval 3"
            % (self.config, self.cache)
        ])
        for text in (r.stdout, r.stderr, self.docker_log.read_text()):
            self.assertNotIn(SECRET, text)

    def test_missing_image_tells_how_to_build(self):
        (self.tmp / "noimage").touch()
        r = self.run_sh(START, env={"KAGELIFE_NOWPLAYING_RUNNER": "docker", "KAGELIFE_SPOTIFY_CLIENT_ID": SECRET})
        self.assertEqual(r.returncode, 1)
        self.assertIn("docker build", r.stderr)
        self.assertFalse((self.cache / "kagelife.pid").exists())

    def test_missing_config_dir_says_login_first(self):
        r = self.run_sh(START, env={"KAGELIFE_SPOTIFY_CLIENT_ID": SECRET, "KAGELIFE_CONFIG": str(self.tmp / "noconf")})
        self.assertEqual(r.returncode, 1)
        self.assertIn("login", r.stderr)
        self.assertFalse((self.cache / "kagelife.pid").exists())

    def test_existing_container_is_refused(self):
        (self.tmp / "running").touch()
        r = self.run_sh(START, env={"KAGELIFE_NOWPLAYING_RUNNER": "docker", "KAGELIFE_SPOTIFY_CLIENT_ID": SECRET})
        self.assertEqual(r.returncode, 1)
        self.assertIn("stop.sh", r.stderr)
        self.assertFalse(any(c.startswith("run ") for c in self.docker_calls()))
        self.assertFalse((self.cache / "kagelife.pid").exists())

    def test_container_name_and_image_from_env(self):
        r = self.run_sh(START, env={"KAGELIFE_NOWPLAYING_RUNNER": "docker",
                                    "KAGELIFE_SPOTIFY_CLIENT_ID": SECRET, "KAGELIFE_START_CHECK": "0",
                                    "KAGELIFE_NOWPLAYING_NAME": "np-x", "KAGELIFE_NOWPLAYING_IMAGE": "img-y"})
        self.assertEqual(r.returncode, 0, r.stderr)
        run = [c for c in self.docker_calls() if c.startswith("run ")][0]
        self.assertIn("--name np-x ", run)
        self.assertIn(" img-y --token", run)

    # --- modes, binary runner (new) ---

    def kl_args_for(self, *args, env=None):
        r = self.run_sh(START, *args, env=env)
        self.assertEqual(r.returncode, 0, r.stderr)
        return r, self.wait_args()

    def test_mode_human_has_no_llm_slots_and_no_nowplaying(self):
        r, args = self.kl_args_for("--mode", "human")
        self.assertEqual(args, "-shaders %s" % self.shaders)
        self.assertEqual((self.cache / "kagelife_mode").read_text(), "human\n")
        self.assertFalse((self.cache / "nowplaying.pid").exists())
        self.assertEqual(self.docker_calls(), [])

    def test_mode_ai_starts_nowplaying_binary_with_llm_slots(self):
        r, args = self.kl_args_for("--mode", "ai", env={"KAGELIFE_SPOTIFY_CLIENT_ID": SECRET})
        self.assertEqual(args, "-shaders %s -llm-slots llm_a,llm_b" % self.shaders)
        self.assertEqual((self.cache / "kagelife_mode").read_text(), "ai\n")
        for _ in range(50):
            if self.mcp_args.exists() and self.mcp_args.read_text():
                break
            time.sleep(0.1)
        self.assertEqual(self.mcp_args.read_text().strip(),
                         "nowplaying-spotify --token %s/spotify-token.json --out %s/now_playing.json --interval 3"
                         % (self.config, self.cache))
        # The value reaches the process by environment only.
        self.assertEqual(Path(str(self.mcp_args) + ".env").read_text().strip(), SECRET)
        self.assertTrue(self.alive(int((self.cache / "nowplaying.pid").read_text())))
        self.assertTrue((self.cache / "nowplaying.log").exists())
        self.assertEqual(self.docker_calls(), [])
        self.assertNotIn(SECRET, r.stdout + r.stderr)

    def test_mode_full_warns_without_deck_and_not_with_deck(self):
        r, _ = self.kl_args_for("--mode", "full", env={"KAGELIFE_SPOTIFY_CLIENT_ID": SECRET})
        self.assertIn("kagelife-deck", r.stderr)
        self.assertEqual((self.cache / "kagelife_mode").read_text(), "full\n")
        self.assertEqual(self.run_sh(STOP).returncode, 0)
        self.kl_args.unlink()
        (self.tmp / "deck").touch()
        r, _ = self.kl_args_for("--mode", "full", env={"KAGELIFE_SPOTIFY_CLIENT_ID": SECRET})
        self.assertNotIn("kagelife-deck", r.stderr)

    def test_bad_mode_is_usage_error(self):
        r = self.run_sh(START, "--mode", "robot")
        self.assertEqual(r.returncode, 2)
        self.assertFalse((self.cache / "kagelife.pid").exists())

    def test_no_mode_writes_nothing_and_clears_stale_mode_file(self):
        self.cache.mkdir(parents=True)
        (self.cache / "kagelife_mode").write_text("ai\n")
        self.kl_args_for("--no-nowplaying")
        self.assertFalse((self.cache / "kagelife_mode").exists())

    def test_no_llm_slots_flag(self):
        _, args = self.kl_args_for("--no-nowplaying", "--no-llm-slots")
        self.assertEqual(args, "-shaders %s" % self.shaders)

    def test_stop_stops_nowplaying_process_and_removes_mode_file(self):
        self.kl_args_for("--mode", "ai", env={"KAGELIFE_SPOTIFY_CLIENT_ID": SECRET})
        npid = int((self.cache / "nowplaying.pid").read_text())
        r = self.run_sh(STOP)
        self.assertEqual(r.returncode, 0, r.stderr)
        for _ in range(50):
            if not self.alive(npid):
                break
            time.sleep(0.1)
        self.assertFalse(self.alive(npid))
        self.assertFalse((self.cache / "nowplaying.pid").exists())
        self.assertFalse((self.cache / "kagelife_mode").exists())

    def test_missing_mcp_binary_starts_nothing_and_points_to_docker(self):
        r = self.run_sh(START, "--mode", "ai", env={"KAGELIFE_SPOTIFY_CLIENT_ID": SECRET,
                                                    "KAGELIFE_MCP_BIN": str(self.tmp / "nobin")})
        self.assertEqual(r.returncode, 1)
        self.assertIn("KAGELIFE_NOWPLAYING_RUNNER=docker", r.stderr)
        self.assertFalse((self.cache / "kagelife.pid").exists())
        self.assertFalse(self.kl_args.exists())

    def test_missing_token_file_says_login_first_before_starting(self):
        (self.config / "spotify-token.json").unlink()
        r = self.run_sh(START, "--mode", "ai", env={"KAGELIFE_SPOTIFY_CLIENT_ID": SECRET})
        self.assertEqual(r.returncode, 1)
        self.assertIn("nowplaying-spotify login --token", r.stderr)
        self.assertFalse(self.kl_args.exists())

    def test_nowplaying_binary_that_dies_is_reported(self):
        (self.tmp / "mcpdie").touch()
        r = self.run_sh(START, "--mode", "ai", env={"KAGELIFE_SPOTIFY_CLIENT_ID": SECRET})
        self.assertEqual(r.returncode, 1)
        self.assertIn("fake mcp: dying", r.stderr)
        self.assertFalse((self.cache / "nowplaying.pid").exists())

    def test_client_id_missing_in_ai_mode(self):
        r = self.run_sh(START, "--mode", "ai")
        self.assertEqual(r.returncode, 1)
        self.assertIn("KAGELIFE_SPOTIFY_CLIENT_ID", r.stderr)

    # --- stop.sh ---

    def test_stop_kills_kagelife_and_removes_container(self):
        self.assertEqual(self.run_sh(START, "--no-nowplaying").returncode, 0)
        pid = self.pid()
        (self.tmp / "running").touch()
        r = self.run_sh(STOP)
        self.assertEqual(r.returncode, 0, r.stderr)
        for _ in range(50):
            if not self.alive(pid):
                break
            time.sleep(0.1)
        self.assertFalse(self.alive(pid))
        self.assertFalse((self.cache / "kagelife.pid").exists())
        calls = self.docker_calls()
        self.assertIn("stop kagelife-nowplaying-spotify", calls)
        self.assertIn("rm kagelife-nowplaying-spotify", calls)

    def test_stop_when_nothing_runs_is_fine(self):
        r = self.run_sh(STOP)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertFalse(any(c.startswith(("stop ", "rm ")) for c in self.docker_calls()))

    def test_stop_with_stale_pid_file(self):
        self.cache.mkdir(parents=True)
        (self.cache / "kagelife.pid").write_text("999999\n")
        r = self.run_sh(STOP, "--no-nowplaying")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertFalse((self.cache / "kagelife.pid").exists())
        self.assertEqual(self.docker_calls(), [])


if __name__ == "__main__":
    unittest.main()
