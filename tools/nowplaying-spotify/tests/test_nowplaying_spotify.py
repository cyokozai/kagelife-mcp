"""nowplaying-spotify unit tests. No real network: every HTTP call goes to a fake transport.

Runs with the standard library only: python -m unittest discover -s tests
"""

import http.client
import importlib.util
import io
import json
import logging
import os
import stat
import sys
import tempfile
import threading
import unittest
import urllib.parse
import urllib.request
from contextlib import redirect_stderr, redirect_stdout
from datetime import datetime, timedelta, timezone
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))

import nowplaying_spotify as nps  # noqa: E402

# keep the test output clean; assertLogs still sees the records it asks for
nps.log.addHandler(logging.NullHandler())
nps.log.propagate = False

T0 = datetime(2026, 11, 21, 12, 0, 0, tzinfo=timezone.utc)
SECRET_ACCESS = "ACCESS-SECRET-aaaa"
SECRET_REFRESH = "REFRESH-SECRET-bbbb"


# --- fakes -------------------------------------------------------------------


class FakeTransport:
    """Returns queued responses in order and records every request."""

    def __init__(self, responses):
        self.responses = list(responses)
        self.calls = []

    def __call__(self, method, url, headers, body=None, timeout=10.0):
        self.calls.append({"method": method, "url": url, "headers": dict(headers), "body": body})
        if not self.responses:
            raise AssertionError("unexpected request: %s %s" % (method, url))
        r = self.responses.pop(0)
        if isinstance(r, BaseException):
            raise r
        return r


def resp(status, obj=None, headers=None):
    body = b"" if obj is None else json.dumps(obj).encode("utf-8")
    return nps.Response(status, {k.lower(): v for k, v in (headers or {}).items()}, body)


class Clock:
    def __init__(self, t=T0):
        self.t = t

    def __call__(self):
        return self.t

    def advance(self, seconds):
        self.t = self.t + timedelta(seconds=seconds)


def track_item(track_id="t1", name="Song A", artists=("Artist A",), duration_ms=200000, album="Album A"):
    return {
        "type": "track",
        "id": track_id,
        "name": name,
        "duration_ms": duration_ms,
        "album": {"name": album},
        "artists": [{"name": a} for a in artists],
    }


def playing(progress_ms, item=None, is_playing=True):
    return {
        "currently_playing_type": "track",
        "is_playing": is_playing,
        "progress_ms": progress_ms,
        "item": item if item is not None else track_item(),
    }


class FakeClient:
    """Stands in for SpotifyClient: currently_playing() and queue() return scripted values."""

    def __init__(self):
        self.current = None
        self.queue_items = []
        self.queue_calls = 0

    def currently_playing(self):
        if isinstance(self.current, BaseException):
            raise self.current
        return self.current

    def queue(self):
        self.queue_calls += 1
        return {"currently_playing": None, "queue": list(self.queue_items)}


class TmpDir(unittest.TestCase):
    def setUp(self):
        self._td = tempfile.TemporaryDirectory()
        self.dir = Path(self._td.name)
        self.out = self.dir / "now_playing.json"
        self.token_path = self.dir / "cfg" / "spotify-token.json"

    def tearDown(self):
        self._td.cleanup()

    def read_out(self):
        return json.loads(self.out.read_text(encoding="utf-8"))


# --- PKCE --------------------------------------------------------------------


class PkceTest(unittest.TestCase):
    def test_rfc7636_appendix_b_vector(self):
        verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
        self.assertEqual(nps.code_challenge(verifier), "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM")

    def test_verifier_is_url_safe_and_long_enough(self):
        v = nps.new_code_verifier()
        self.assertTrue(43 <= len(v) <= 128, len(v))
        allowed = set("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~")
        self.assertTrue(set(v) <= allowed)
        self.assertNotEqual(v, nps.new_code_verifier())

    def test_authorize_url_has_challenge_but_not_verifier(self):
        v = nps.new_code_verifier()
        url = nps.authorize_url("cid", "http://127.0.0.1:8888/callback", v, "st")
        q = urllib.parse.parse_qs(urllib.parse.urlparse(url).query)
        self.assertTrue(url.startswith("https://accounts.spotify.com/authorize?"))
        self.assertEqual(q["client_id"], ["cid"])
        self.assertEqual(q["response_type"], ["code"])
        self.assertEqual(q["redirect_uri"], ["http://127.0.0.1:8888/callback"])
        self.assertEqual(q["code_challenge_method"], ["S256"])
        self.assertEqual(q["code_challenge"], [nps.code_challenge(v)])
        self.assertEqual(q["state"], ["st"])
        self.assertEqual(q["scope"], ["user-read-currently-playing user-read-playback-state"])
        self.assertNotIn(v, url)


# --- tokens --------------------------------------------------------------------


class TokenTest(TmpDir):
    def save_tokens(self, expires_at):
        nps.save_tokens(self.token_path, {
            "access_token": "old-access", "refresh_token": SECRET_REFRESH,
            "expires_at": expires_at, "token_type": "Bearer",
        })

    def test_save_tokens_is_0600(self):
        self.save_tokens(10**10)
        mode = stat.S_IMODE(os.stat(self.token_path).st_mode)
        self.assertEqual(mode, 0o600)
        self.assertEqual([p.name for p in self.token_path.parent.iterdir()], ["spotify-token.json"])

    def test_valid_token_is_used_without_refresh(self):
        self.save_tokens(1000.0 + 3600)
        t = FakeTransport([])
        tm = nps.TokenManager(self.token_path, "cid", t, now=lambda: 1000.0)
        self.assertEqual(tm.access_token(), "old-access")
        self.assertEqual(t.calls, [])

    def test_expired_token_is_refreshed_and_rotated_refresh_token_saved(self):
        self.save_tokens(1000.0 - 1)
        t = FakeTransport([resp(200, {
            "access_token": SECRET_ACCESS, "token_type": "Bearer", "expires_in": 3600,
            "refresh_token": "rotated-refresh",
        })])
        tm = nps.TokenManager(self.token_path, "cid", t, now=lambda: 1000.0)
        self.assertEqual(tm.access_token(), SECRET_ACCESS)
        [call] = t.calls
        self.assertEqual(call["method"], "POST")
        self.assertEqual(call["url"], nps.TOKEN_URL)
        form = urllib.parse.parse_qs(call["body"].decode())
        self.assertEqual(form["grant_type"], ["refresh_token"])
        self.assertEqual(form["refresh_token"], [SECRET_REFRESH])
        self.assertEqual(form["client_id"], ["cid"])
        self.assertNotIn("client_secret", form)
        self.assertNotIn("Authorization", call["headers"])
        saved = json.loads(self.token_path.read_text())
        self.assertEqual(saved["access_token"], SECRET_ACCESS)
        self.assertEqual(saved["refresh_token"], "rotated-refresh")
        self.assertEqual(saved["expires_at"], 1000.0 + 3600)
        self.assertEqual(stat.S_IMODE(os.stat(self.token_path).st_mode), 0o600)

    def test_refresh_without_rotation_keeps_old_refresh_token(self):
        self.save_tokens(0)
        t = FakeTransport([resp(200, {"access_token": SECRET_ACCESS, "expires_in": 3600})])
        tm = nps.TokenManager(self.token_path, "cid", t, now=lambda: 1000.0)
        tm.access_token()
        self.assertEqual(json.loads(self.token_path.read_text())["refresh_token"], SECRET_REFRESH)

    def test_missing_token_file_requires_login(self):
        tm = nps.TokenManager(self.token_path, "cid", FakeTransport([]), now=lambda: 0.0)
        with self.assertRaises(nps.LoginRequired):
            tm.access_token()

    def test_invalid_grant_requires_login_and_logs_no_secret(self):
        self.save_tokens(0)
        t = FakeTransport([resp(400, {"error": "invalid_grant", "error_description": "Refresh token revoked"})])
        tm = nps.TokenManager(self.token_path, "cid", t, now=lambda: 1000.0)
        with self.assertLogs("nowplaying_spotify", level="DEBUG") as cm:
            nps.log.debug("probe")
            with self.assertRaises(nps.LoginRequired) as ctx:
                tm.access_token()
        text = "\n".join(cm.output) + str(ctx.exception)
        self.assertNotIn(SECRET_REFRESH, text)


# --- API client ------------------------------------------------------------------


class StaticTokens:
    def __init__(self):
        self.refreshed = 0

    def access_token(self):
        return SECRET_ACCESS if self.refreshed == 0 else "new-access"

    def refresh(self):
        self.refreshed += 1


class ClientTest(unittest.TestCase):
    def client(self, responses):
        self.tokens = StaticTokens()
        self.transport = FakeTransport(responses)
        return nps.SpotifyClient(self.tokens, self.transport)

    def test_200_returns_json_and_sends_bearer(self):
        c = self.client([resp(200, playing(1000))])
        self.assertEqual(c.currently_playing()["progress_ms"], 1000)
        call = self.transport.calls[0]
        self.assertEqual(call["url"], nps.API_BASE + "/me/player/currently-playing")
        self.assertEqual(call["headers"]["Authorization"], "Bearer " + SECRET_ACCESS)

    def test_204_means_nothing_playing(self):
        c = self.client([resp(204)])
        self.assertIsNone(c.currently_playing())

    def test_401_refreshes_once_and_retries(self):
        c = self.client([resp(401, {"error": {"status": 401, "message": "The access token expired"}}),
                         resp(200, {"queue": []})])
        self.assertEqual(c.queue(), {"queue": []})
        self.assertEqual(self.tokens.refreshed, 1)
        self.assertEqual(self.transport.calls[1]["headers"]["Authorization"], "Bearer new-access")

    def test_401_twice_raises(self):
        c = self.client([resp(401), resp(401)])
        with self.assertRaises(nps.ApiError) as ctx:
            c.currently_playing()
        self.assertEqual(ctx.exception.status, 401)
        self.assertEqual(self.tokens.refreshed, 1)

    def test_429_raises_with_retry_after(self):
        c = self.client([resp(429, None, {"Retry-After": "7"})])
        with self.assertRaises(nps.RateLimited) as ctx:
            c.currently_playing()
        self.assertEqual(ctx.exception.retry_after, 7.0)

    def test_5xx_raises_api_error_without_token(self):
        c = self.client([resp(503)])
        with self.assertRaises(nps.ApiError) as ctx:
            c.currently_playing()
        self.assertEqual(ctx.exception.status, 503)
        self.assertNotIn(SECRET_ACCESS, str(ctx.exception))


class UrllibTransportTest(unittest.TestCase):
    def test_broken_http_response_becomes_oserror(self):
        real = urllib.request.urlopen

        def broken(req, timeout=None):
            raise http.client.IncompleteRead(b"partial")
        urllib.request.urlopen = broken
        try:
            with self.assertRaises(OSError):
                nps.urllib_transport("GET", "https://api.spotify.com/v1/me/player/queue", {})
        finally:
            urllib.request.urlopen = real


# --- watcher: history_id, write-on-change ------------------------------------------


class WatcherTest(TmpDir):
    def setUp(self):
        super().setUp()
        self.clock = Clock()
        self.client = FakeClient()

    def watcher(self):
        return nps.Watcher(self.client, self.out, clock=self.clock)

    def test_first_track_is_written_with_compatible_schema(self):
        self.client.current = playing(30000, track_item(artists=("A", "B")))
        self.client.queue_items = [track_item("n%d" % i, "Next %d" % i) for i in range(5)]
        w = self.watcher()
        self.assertIsNotNone(w.poll())
        d = self.read_out()
        for k in ("title", "artist", "bpm", "key", "genre", "played_at", "detected_at",
                  "source", "history_id", "content_id"):
            self.assertIn(k, d)
        self.assertEqual(d["title"], "Song A")
        self.assertEqual(d["artist"], "A, B")
        self.assertIsNone(d["bpm"])
        self.assertIsNone(d["key"])
        self.assertIsNone(d["genre"])
        self.assertEqual(d["source"], "spotify")
        self.assertEqual(d["content_id"], "t1")
        self.assertEqual(d["album"], "Album A")
        self.assertEqual(d["duration_ms"], 200000)
        self.assertEqual(d["progress_ms"], 30000)
        self.assertTrue(d["is_playing"])
        self.assertEqual(d["fetched_at"], "2026-11-21T12:00:00.000+00:00")
        self.assertEqual(d["played_at"], "2026-11-21T11:59:30.000+00:00")
        self.assertEqual(d["detected_at"], "2026-11-21T12:00:00.000+00:00")
        self.assertEqual(len(d["next"]), 3)
        self.assertEqual(d["next"][0], {"title": "Next 0", "artist": "Artist A",
                                        "duration_ms": 200000, "track_id": "n0"})
        self.assertRegex(d["history_id"], r"^[A-Za-z0-9-]+$")

    def test_same_track_progressing_is_not_rewritten(self):
        self.client.current = playing(30000)
        w = self.watcher()
        w.poll()
        mtime = self.out.stat().st_mtime_ns
        self.clock.advance(3)
        self.client.current = playing(33000)
        self.assertIsNone(w.poll())
        self.assertEqual(self.out.stat().st_mtime_ns, mtime)

    def test_new_track_gets_new_history_id(self):
        self.client.current = playing(30000)
        w = self.watcher()
        w.poll()
        h1 = self.read_out()["history_id"]
        self.clock.advance(3)
        self.client.current = playing(1000, track_item("t2", "Song B"))
        self.assertIsNotNone(w.poll())
        d = self.read_out()
        self.assertEqual(d["title"], "Song B")
        self.assertNotEqual(d["history_id"], h1)

    def test_seek_back_and_forward_keeps_history_id(self):
        self.client.current = playing(100000)
        w = self.watcher()
        w.poll()
        h1 = self.read_out()["history_id"]
        for progress in (5000, 150000, 2000):  # seek to start, near middle, start again
            self.clock.advance(3)
            self.client.current = playing(progress)
            self.assertIsNone(w.poll())
            self.assertEqual(self.read_out()["history_id"], h1)

    def test_repeat_after_near_end_gets_new_history_id(self):
        self.client.current = playing(195000)  # 5 s before the end of a 200 s track
        w = self.watcher()
        w.poll()
        h1 = self.read_out()["history_id"]
        self.clock.advance(6)
        self.client.current = playing(1000)
        self.assertIsNotNone(w.poll())
        h2 = self.read_out()["history_id"]
        self.assertNotEqual(h2, h1)
        # and the second play keeps its id while it progresses
        self.clock.advance(3)
        self.client.current = playing(4000)
        self.assertIsNone(w.poll())
        self.assertEqual(self.read_out()["history_id"], h2)

    def test_repeat_after_long_gap_gets_new_history_id(self):
        # last seen in the middle, but enough time passed for the track to finish
        self.client.current = playing(100000)
        w = self.watcher()
        w.poll()
        h1 = self.read_out()["history_id"]
        self.clock.advance(110)
        self.client.current = playing(8000)
        self.assertIsNotNone(w.poll())
        self.assertNotEqual(self.read_out()["history_id"], h1)

    def test_paused_time_is_not_counted_as_playback(self):
        self.client.current = playing(100000, is_playing=False)
        w = self.watcher()
        w.poll()
        h1 = self.read_out()["history_id"]
        self.clock.advance(300)  # paused for 5 minutes: progress does not advance
        self.client.current = playing(1000, is_playing=False)
        self.assertIsNone(w.poll())
        self.assertEqual(self.read_out()["history_id"], h1)

    def test_is_playing_change_is_written_without_new_history(self):
        self.client.current = playing(30000)
        w = self.watcher()
        w.poll()
        h1 = self.read_out()["history_id"]
        self.clock.advance(3)
        self.client.current = playing(31000, is_playing=False)
        self.assertIsNotNone(w.poll())
        d = self.read_out()
        self.assertFalse(d["is_playing"])
        self.assertEqual(d["history_id"], h1)

    def test_next_change_is_written(self):
        self.client.current = playing(30000)
        self.client.queue_items = [track_item("n1")]
        w = self.watcher()
        w.poll()
        self.clock.advance(3)
        self.client.current = playing(33000)
        self.client.queue_items = [track_item("n2")]
        self.assertIsNotNone(w.poll())
        self.assertEqual(self.read_out()["next"][0]["track_id"], "n2")

    def test_204_marks_not_playing_once(self):
        self.client.current = playing(30000)
        w = self.watcher()
        w.poll()
        h1 = self.read_out()["history_id"]
        self.clock.advance(3)
        self.client.current = None
        self.assertIsNotNone(w.poll())
        d = self.read_out()
        self.assertFalse(d["is_playing"])
        self.assertEqual(d["history_id"], h1)
        self.clock.advance(3)
        self.assertIsNone(w.poll())

    def test_204_with_nothing_known_writes_nothing(self):
        self.client.current = None
        self.assertIsNone(self.watcher().poll())
        self.assertFalse(self.out.exists())

    def test_episode_and_ad_are_ignored(self):
        w = self.watcher()
        self.client.current = {"currently_playing_type": "episode", "is_playing": True,
                               "progress_ms": 1000, "item": None}
        self.assertIsNone(w.poll())
        self.client.current = {"currently_playing_type": "ad", "is_playing": True,
                               "progress_ms": 1000, "item": None}
        self.assertIsNone(w.poll())
        self.client.current = {"currently_playing_type": "episode", "is_playing": True,
                               "progress_ms": 1000, "item": {"type": "episode", "id": "e1", "name": "Ep"}}
        self.assertIsNone(w.poll())
        self.assertFalse(self.out.exists())

    def test_queue_skips_episodes(self):
        self.client.current = playing(30000)
        self.client.queue_items = [{"type": "episode", "id": "e1", "name": "Ep"}, track_item("n1")]
        w = self.watcher()
        w.poll()
        self.assertEqual([n["track_id"] for n in self.read_out()["next"]], ["n1"])

    def test_queue_failure_keeps_previous_next(self):
        self.client.current = playing(30000)
        self.client.queue_items = [track_item("n1")]
        w = self.watcher()
        w.poll()

        def broken():
            raise nps.ApiError(503)
        self.client.queue = broken
        self.clock.advance(3)
        self.client.current = playing(33000)
        self.assertIsNone(w.poll())
        self.assertEqual(self.read_out()["next"][0]["track_id"], "n1")

    def test_restart_does_not_rewrite_same_song(self):
        self.client.current = playing(30000)
        self.watcher().poll()
        before = self.out.read_bytes()
        h1 = self.read_out()["history_id"]
        self.clock.advance(5)
        self.client.current = playing(35000)
        w2 = self.watcher()
        self.assertIsNone(w2.poll())
        self.assertEqual(self.out.read_bytes(), before)
        # the restored watcher still knows the history id
        self.clock.advance(3)
        self.client.current = playing(38000, is_playing=False)
        w2.poll()
        self.assertEqual(self.read_out()["history_id"], h1)

    def test_rekordbox_file_is_replaced(self):
        self.out.write_text(json.dumps({"title": "x", "history_id": "123", "content_id": "t1",
                                        "source": "rekordbox-history"}))
        self.client.current = playing(30000)
        self.assertIsNotNone(self.watcher().poll())
        d = self.read_out()
        self.assertEqual(d["source"], "spotify")
        self.assertNotEqual(d["history_id"], "123")

    def test_observe_does_not_write(self):
        self.client.current = playing(30000)
        payload = self.watcher().observe()
        self.assertEqual(payload["title"], "Song A")
        self.assertFalse(self.out.exists())

    def test_repeat_seen_late_after_near_end_gets_new_history_id(self):
        # e.g. a 429 made us wait: the repeat is first seen 20 s into the second play
        self.client.current = playing(195000)
        w = self.watcher()
        w.poll()
        h1 = self.read_out()["history_id"]
        self.clock.advance(25)
        self.client.current = playing(20000)
        self.assertIsNotNone(w.poll())
        self.assertNotEqual(self.read_out()["history_id"], h1)

    def test_seek_back_far_from_start_after_long_gap_keeps_history_id(self):
        self.client.current = playing(100000)
        w = self.watcher()
        w.poll()
        h1 = self.read_out()["history_id"]
        self.clock.advance(100)  # the old play would just about end now ...
        self.client.current = playing(60000)  # ... but 60 s in is too far for a restart
        self.assertIsNone(w.poll())
        self.assertEqual(self.read_out()["history_id"], h1)

    def test_queue_rate_limited_on_new_track_keeps_track_change(self):
        self.client.current = playing(30000)
        w = self.watcher()
        w.poll()
        h1 = self.read_out()["history_id"]
        queue = self.client.queue

        def limited():
            raise nps.RateLimited(10)
        self.client.queue = limited
        self.clock.advance(3)
        self.client.current = playing(1000, track_item("t2", "Song B"))
        with self.assertRaises(nps.RateLimited):
            w.poll()
        self.assertEqual(self.read_out()["title"], "Song A")
        self.client.queue = queue
        self.clock.advance(10)
        self.client.current = playing(11000, track_item("t2", "Song B"))
        self.assertIsNotNone(w.poll())
        d = self.read_out()
        self.assertEqual(d["title"], "Song B")
        self.assertNotEqual(d["history_id"], h1)

    def test_failed_write_is_retried_with_new_history_id(self):
        self.client.current = playing(30000)
        w = self.watcher()
        w.poll()
        h1 = self.read_out()["history_id"]
        real = nps.write_atomic

        def broken(path, obj):
            raise OSError("disk full")
        nps.write_atomic = broken
        try:
            self.clock.advance(3)
            self.client.current = playing(1000, track_item("t2", "Song B"))
            with self.assertRaises(OSError):
                w.poll()
        finally:
            nps.write_atomic = real
        self.clock.advance(3)
        self.client.current = playing(4000, track_item("t2", "Song B"))
        self.assertIsNotNone(w.poll())
        d = self.read_out()
        self.assertEqual(d["title"], "Song B")
        self.assertNotEqual(d["history_id"], h1)

    def test_204_keeps_played_at_consistent(self):
        self.client.current = playing(30000)
        w = self.watcher()
        w.poll()
        self.clock.advance(3)
        self.client.current = playing(33000)
        w.poll()
        self.clock.advance(3)
        self.client.current = None
        w.poll()
        d = self.read_out()
        self.assertFalse(d["is_playing"])
        played = nps.parse_iso(d["played_at"])
        fetched = nps.parse_iso(d["fetched_at"])
        self.assertEqual(fetched - played, timedelta(milliseconds=d["progress_ms"]))
        self.assertEqual(d["progress_ms"], 33000)

    def test_unexpected_json_shapes_do_not_crash(self):
        w = self.watcher()
        self.client.current = ["not", "a", "dict"]
        self.assertIsNone(w.poll())
        self.client.current = playing(30000, dict(track_item(), artists="nobody"))
        self.client.queue = lambda: ["not", "a", "dict"]
        self.assertIsNotNone(w.poll())
        d = self.read_out()
        self.assertEqual(d["artist"], "")
        self.assertEqual(d["next"], [])

    def test_restart_with_broken_next_in_file(self):
        self.out.write_text(json.dumps({"source": "spotify", "history_id": "sp-t1-1", "content_id": "t1",
                                        "fetched_at": "2026-11-21T12:00:00+00:00", "progress_ms": 30000,
                                        "duration_ms": 200000, "is_playing": True, "next": ["x", 3]}))
        self.client.current = playing(31000)
        w = self.watcher()
        w.poll()
        self.assertEqual(self.read_out()["history_id"], "sp-t1-1")


# --- run loop: 429, 5xx, login required ------------------------------------------


class RunLoopTest(TmpDir):
    def run_loop(self, errors, polls):
        client = FakeClient()
        w = nps.Watcher(client, self.out, clock=Clock())
        seq = list(errors)

        def poll():
            e = seq.pop(0) if seq else None
            if e is not None:
                raise e
            return None
        w.poll = poll
        sleeps = []
        rc = nps.run_forever(w, 3.0, sleep=sleeps.append, max_polls=polls)
        return rc, sleeps

    def test_429_sleeps_retry_after(self):
        rc, sleeps = self.run_loop([nps.RateLimited(20.0), None], 2)
        self.assertEqual(rc, 0)
        self.assertEqual(sleeps, [20.0, 3.0])

    def test_5xx_and_network_errors_back_off_and_continue(self):
        rc, sleeps = self.run_loop([nps.ApiError(502), OSError("down"), None, nps.ApiError(500)], 4)
        self.assertEqual(rc, 0)
        self.assertEqual(sleeps, [6.0, 12.0, 3.0, 6.0])

    def test_backoff_is_capped(self):
        rc, sleeps = self.run_loop([nps.ApiError(500)] * 8, 8)
        self.assertEqual(max(sleeps), nps.MAX_BACKOFF)

    def test_login_required_stops(self):
        rc, sleeps = self.run_loop([nps.LoginRequired("run login")], 5)
        self.assertEqual(rc, 1)


# --- login callback server ---------------------------------------------------------


class LoginTest(TmpDir):
    def start_login(self, transport):
        self.lines = []
        result = {}
        ready = threading.Event()

        def on_url(url, port):
            result["url"], result["port"] = url, port
            ready.set()

        def target():
            try:
                result["rc"] = nps.login("cid", self.token_path, transport,
                                         listen_host="127.0.0.1", listen_port=0,
                                         redirect_uri="http://127.0.0.1:8888/callback",
                                         timeout=10, on_url=on_url, now=lambda: 1000.0)
            except Exception as e:  # surfaced to the test
                result["error"] = e
                ready.set()
        th = threading.Thread(target=target, daemon=True)
        th.start()
        self.assertTrue(ready.wait(10))
        return th, result

    def hit(self, port, query):
        url = "http://127.0.0.1:%d/callback?%s" % (port, urllib.parse.urlencode(query))
        try:
            with urllib.request.urlopen(url, timeout=5) as r:
                return r.status
        except urllib.error.HTTPError as e:
            return e.code

    def test_login_validates_state_and_saves_tokens(self):
        t = FakeTransport([resp(200, {"access_token": SECRET_ACCESS, "refresh_token": SECRET_REFRESH,
                                      "expires_in": 3600, "token_type": "Bearer",
                                      "scope": "user-read-currently-playing user-read-playback-state"})])
        err = io.StringIO()
        with self.assertLogs("nowplaying_spotify", level="DEBUG") as cm, redirect_stderr(err):
            th, result = self.start_login(t)
            q = urllib.parse.parse_qs(urllib.parse.urlparse(result["url"]).query)
            state = q["state"][0]
            self.assertEqual(self.hit(result["port"], {"code": "c", "state": "wrong"}), 400)
            self.assertEqual(self.hit(result["port"], {"code": "c", "state": "äあ"}), 400)
            self.assertEqual(self.hit(result["port"], {"code": "the-code", "state": state}), 200)
            th.join(10)
        self.assertEqual(result.get("rc"), 0, result.get("error"))
        [call] = t.calls
        form = urllib.parse.parse_qs(call["body"].decode())
        verifier = form["code_verifier"][0]
        said = "\n".join(cm.output) + err.getvalue()
        for secret in ("the-code", verifier, SECRET_ACCESS, SECRET_REFRESH):
            self.assertNotIn(secret, said)
        self.assertNotIn(verifier, result["url"])
        self.assertEqual(form["grant_type"], ["authorization_code"])
        self.assertEqual(form["code"], ["the-code"])
        self.assertEqual(form["redirect_uri"], ["http://127.0.0.1:8888/callback"])
        self.assertEqual(form["client_id"], ["cid"])
        self.assertEqual(nps.code_challenge(form["code_verifier"][0]), q["code_challenge"][0])
        saved = json.loads(self.token_path.read_text())
        self.assertEqual(saved["refresh_token"], SECRET_REFRESH)
        self.assertEqual(saved["expires_at"], 1000.0 + 3600)
        self.assertEqual(stat.S_IMODE(os.stat(self.token_path).st_mode), 0o600)

    def test_login_denied(self):
        th, result = self.start_login(FakeTransport([]))
        state = urllib.parse.parse_qs(urllib.parse.urlparse(result["url"]).query)["state"][0]
        self.assertEqual(self.hit(result["port"], {"error": "access_denied", "state": state}), 400)
        th.join(10)
        self.assertEqual(result.get("rc"), 1)
        self.assertFalse(self.token_path.exists())

    def test_exchange_without_access_token_fails_cleanly(self):
        th, result = self.start_login(FakeTransport([resp(200, {"token_type": "Bearer"})]))
        state = urllib.parse.parse_qs(urllib.parse.urlparse(result["url"]).query)["state"][0]
        self.assertEqual(self.hit(result["port"], {"code": "the-code", "state": state}), 200)
        th.join(10)
        self.assertEqual(result.get("rc"), 1, result.get("error"))
        self.assertFalse(self.token_path.exists())


# --- CLI ------------------------------------------------------------------------


class CliTest(unittest.TestCase):
    def test_default_command_is_watch(self):
        a = nps.parse_args(["--client-id", "x"])
        self.assertEqual(a.command, "watch")
        self.assertEqual(a.interval, 3.0)
        self.assertFalse(a.once)

    def test_client_id_from_env(self):
        old = os.environ.get("KAGELIFE_SPOTIFY_CLIENT_ID")
        os.environ["KAGELIFE_SPOTIFY_CLIENT_ID"] = "from-env"
        try:
            self.assertEqual(nps.parse_args(["login"]).client_id, "from-env")
        finally:
            if old is None:
                del os.environ["KAGELIFE_SPOTIFY_CLIENT_ID"]
            else:
                os.environ["KAGELIFE_SPOTIFY_CLIENT_ID"] = old


class OnceTest(TmpDir):
    def run_once(self, client):
        real = nps.SpotifyClient
        nps.SpotifyClient = lambda tokens: client
        buf = io.StringIO()
        try:
            with redirect_stdout(buf):
                rc = nps.main(["--client-id", "cid", "--once", "--out", str(self.out),
                               "--token", str(self.token_path)])
        finally:
            nps.SpotifyClient = real
        return rc, buf.getvalue()

    def test_once_prints_and_does_not_write(self):
        client = FakeClient()
        client.current = playing(30000)
        rc, out = self.run_once(client)
        self.assertEqual(rc, 0)
        self.assertEqual(json.loads(out)["title"], "Song A")
        self.assertFalse(self.out.exists())

    def test_once_errors_exit_1_without_traceback(self):
        for err in (nps.RateLimited(5), nps.ApiError(503), OSError("network down")):
            client = FakeClient()
            client.current = err
            rc, out = self.run_once(client)
            self.assertEqual(rc, 1)
            self.assertEqual(out, "")
            self.assertFalse(self.out.exists())


# --- compatibility with tools/conductor/wait_next.py -------------------------------


def load_wait_next():
    for base in (HERE.parents[1] / "conductor", HERE.parents[1].parent / "conductor"):
        path = base / "wait_next.py"
        if path.is_file():
            spec = importlib.util.spec_from_file_location("wait_next", path)
            mod = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(mod)
            return mod
    return None


WAIT_NEXT = load_wait_next()


@unittest.skipIf(WAIT_NEXT is None, "tools/conductor/wait_next.py not found (mount it at /conductor)")
class WaitNextCompatTest(TmpDir):
    def wait_next(self):
        buf = io.StringIO()
        with redirect_stdout(buf):
            rc = WAIT_NEXT.main(["--dir", str(self.dir), "--interval", "0.01", "--timeout", "0.05"])
        return rc, [json.loads(l) for l in buf.getvalue().splitlines() if l.strip()]

    def test_wait_next_sees_start_then_song_then_nothing_on_pause(self):
        clock, client = Clock(), FakeClient()
        client.current = playing(30000)
        w = nps.Watcher(client, self.out, clock=clock)
        w.poll()
        rc, [ev] = self.wait_next()
        self.assertEqual(rc, 0)
        self.assertEqual(ev["changed"], ["start"])
        self.assertEqual(ev["now_playing"]["title"], "Song A")

        clock.advance(3)
        client.current = playing(31000, is_playing=False)
        w.poll()
        rc, events = self.wait_next()
        self.assertEqual((rc, events), (3, []))  # pause is not a new song

        clock.advance(3)
        client.current = playing(1000, track_item("t2", "Song B"))
        w.poll()
        rc, [ev] = self.wait_next()
        self.assertEqual(ev["changed"], ["song"])
        self.assertEqual(ev["now_playing"]["title"], "Song B")


if __name__ == "__main__":
    logging.basicConfig(level=logging.CRITICAL)
    unittest.main()
