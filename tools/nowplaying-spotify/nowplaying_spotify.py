"""Watch Spotify playback through the Web API and write now_playing.json.

The output keeps the keys of tools/nowplaying (rekordbox), so tools/conductor works unchanged.
Auth is Authorization Code with PKCE: there is no client secret anywhere.
Standard library only.

Never log access tokens, refresh tokens, authorization codes or code verifiers.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import http.client
import json
import logging
import os
import secrets
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from typing import Any, Callable, Optional

SOURCE_NAME = "spotify"
AUTHORIZE_URL = "https://accounts.spotify.com/authorize"
TOKEN_URL = "https://accounts.spotify.com/api/token"
API_BASE = "https://api.spotify.com/v1"
SCOPES = "user-read-currently-playing user-read-playback-state"
DEFAULT_REDIRECT_URI = "http://127.0.0.1:8888/callback"
DEFAULT_OUT = Path.home() / ".cache" / "kagelife" / "now_playing.json"
DEFAULT_TOKEN = Path.home() / ".config" / "kagelife" / "spotify-token.json"
CLIENT_ID_ENV = "KAGELIFE_SPOTIFY_CLIENT_ID"

NEXT_COUNT = 3
# history_id rules (see README): a same-track observation is a new play only when the
# progress went back to within START_WINDOW_MS of the start AND the previous play would
# have reached within END_WINDOW_MS of its end by now.
START_WINDOW_MS = 15000
END_WINDOW_MS = 15000
# Refresh the access token this many seconds before it expires.
EXPIRY_MARGIN_S = 60
MAX_BACKOFF = 60.0

log = logging.getLogger("nowplaying_spotify")


# --- errors ------------------------------------------------------------------


class LoginRequired(Exception):
    """No usable token: the user has to run `login` again."""


class ApiError(Exception):
    def __init__(self, status: int, detail: str = "") -> None:
        self.status = status
        self.detail = detail
        super().__init__("HTTP %d%s" % (status, (": " + detail) if detail else ""))


class RateLimited(Exception):
    def __init__(self, retry_after: float) -> None:
        self.retry_after = retry_after
        super().__init__("rate limited, retry after %.0f s" % retry_after)


# --- HTTP transport ------------------------------------------------------------


@dataclass
class Response:
    status: int
    headers: dict  # lower-case keys
    body: bytes

    def json(self) -> Any:
        return json.loads(self.body.decode("utf-8")) if self.body else None


Transport = Callable[..., Response]


def urllib_transport(method: str, url: str, headers: dict, body: Optional[bytes] = None,
                     timeout: float = 10.0) -> Response:
    """Send one request. HTTP errors come back as a Response; network errors raise OSError."""
    req = urllib.request.Request(url, data=body, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return Response(r.status, {k.lower(): v for k, v in r.headers.items()}, r.read())
    except urllib.error.HTTPError as e:
        hdrs = {k.lower(): v for k, v in (e.headers.items() if e.headers else [])}
        try:
            data = e.read()
        except (OSError, http.client.HTTPException):
            data = b""
        return Response(e.code, hdrs, data)
    except http.client.HTTPException as e:  # IncompleteRead, RemoteDisconnected, BadStatusLine ...
        raise OSError("%s: %s" % (type(e).__name__, e)) from e


def _error_detail(r: Response) -> str:
    """A short, token-free description of an error body (Spotify's error code/message only)."""
    try:
        obj = r.json()
    except ValueError:
        return ""
    if not isinstance(obj, dict):
        return ""
    err = obj.get("error")
    if isinstance(err, dict):
        return str(err.get("message", ""))[:200]
    if isinstance(err, str):
        return err[:100]
    return ""


def _retry_after(r: Response) -> float:
    try:
        return max(1.0, float(r.headers.get("retry-after", "5")))
    except ValueError:
        return 5.0


# --- PKCE ----------------------------------------------------------------------


def _b64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


def new_code_verifier() -> str:
    # 64 random bytes -> 86 chars of [A-Za-z0-9-_], within RFC 7636's 43..128
    return _b64url(secrets.token_bytes(64))


def code_challenge(verifier: str) -> str:
    return _b64url(hashlib.sha256(verifier.encode("ascii")).digest())


def authorize_url(client_id: str, redirect_uri: str, verifier: str, state: str) -> str:
    q = urllib.parse.urlencode({
        "client_id": client_id,
        "response_type": "code",
        "redirect_uri": redirect_uri,
        "code_challenge_method": "S256",
        "code_challenge": code_challenge(verifier),
        "state": state,
        "scope": SCOPES,
    })
    return AUTHORIZE_URL + "?" + q


# --- tokens --------------------------------------------------------------------


def save_tokens(path: Path, tokens: dict) -> None:
    """Write tokens atomically with mode 0600 (tmp file in the same dir, then rename)."""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd, tmp = tempfile.mkstemp(prefix="." + path.name + ".", suffix=".tmp", dir=path.parent)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(tokens, f, indent=2)
            f.write("\n")
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass
        raise


def _token_request(transport: Transport, form: dict) -> Response:
    body = urllib.parse.urlencode(form).encode("ascii")
    return transport("POST", TOKEN_URL,
                     {"Content-Type": "application/x-www-form-urlencoded"}, body)


def _tokens_from_response(obj: dict, now: float, old: Optional[dict] = None) -> dict:
    tokens = dict(old or {})
    tokens["access_token"] = obj["access_token"]
    tokens["token_type"] = obj.get("token_type", "Bearer")
    tokens["expires_at"] = now + float(obj.get("expires_in", 3600))
    if obj.get("scope"):
        tokens["scope"] = obj["scope"]
    if obj.get("refresh_token"):  # Spotify may rotate it
        tokens["refresh_token"] = obj["refresh_token"]
    return tokens


class TokenManager:
    def __init__(self, path: Path, client_id: str, transport: Transport = urllib_transport,
                 now: Callable[[], float] = time.time) -> None:
        self.path = Path(path)
        self.client_id = client_id
        self.transport = transport
        self.now = now
        self._tokens: Optional[dict] = None

    def _load(self) -> dict:
        if self._tokens is None:
            try:
                self._tokens = json.loads(self.path.read_text(encoding="utf-8"))
            except FileNotFoundError:
                raise LoginRequired("no token file at %s: run `login` first" % self.path) from None
            except ValueError:
                raise LoginRequired("token file %s is broken: run `login` again" % self.path) from None
        return self._tokens

    def access_token(self) -> str:
        tokens = self._load()
        if not tokens.get("access_token") or float(tokens.get("expires_at", 0)) - EXPIRY_MARGIN_S <= self.now():
            self.refresh()
            tokens = self._load()
        return tokens["access_token"]

    def refresh(self) -> None:
        tokens = self._load()
        if not tokens.get("refresh_token"):
            raise LoginRequired("no refresh token: run `login` again")
        r = _token_request(self.transport, {
            "grant_type": "refresh_token",
            "refresh_token": tokens["refresh_token"],
            "client_id": self.client_id,
        })
        if r.status == 429:
            raise RateLimited(_retry_after(r))
        if r.status == 400 or r.status == 401:
            detail = _error_detail(r)
            log.error("token refresh rejected (HTTP %d %s)", r.status, detail)
            raise LoginRequired("token refresh rejected (%s): run `login` again" % (detail or r.status))
        if r.status != 200:
            raise ApiError(r.status, _error_detail(r))
        self._tokens = _tokens_from_response(r.json(), self.now(), tokens)
        save_tokens(self.path, self._tokens)
        log.info("access token refreshed")


# --- API client ------------------------------------------------------------------


class SpotifyClient:
    def __init__(self, tokens: Any, transport: Transport = urllib_transport) -> None:
        self.tokens = tokens
        self.transport = transport

    def _get(self, path: str) -> Any:
        url = API_BASE + path
        for attempt in (0, 1):
            headers = {"Authorization": "Bearer " + self.tokens.access_token(),
                       "Accept": "application/json"}
            r = self.transport("GET", url, headers)
            if r.status == 401 and attempt == 0:
                log.info("HTTP 401 on %s: refreshing the token once", path)
                self.tokens.refresh()
                continue
            break
        if r.status == 204:
            return None
        if r.status == 429:
            raise RateLimited(_retry_after(r))
        if r.status != 200:
            raise ApiError(r.status, _error_detail(r))
        return r.json()

    def currently_playing(self) -> Optional[dict]:
        return self._get("/me/player/currently-playing")

    def queue(self) -> Optional[dict]:
        return self._get("/me/player/queue")


# --- output ----------------------------------------------------------------------


def iso(dt: datetime) -> str:
    return dt.astimezone(timezone.utc).isoformat(timespec="milliseconds")


def parse_iso(s: Any) -> Optional[datetime]:
    if not isinstance(s, str):
        return None
    try:
        dt = datetime.fromisoformat(s)
    except ValueError:
        return None
    return dt if dt.tzinfo else dt.replace(tzinfo=timezone.utc)


def write_atomic(path: Path, obj: Any) -> None:
    """Write obj as JSON to path atomically (tmp file in the same dir, then rename)."""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix="." + path.name + ".", suffix=".tmp", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(obj, f, ensure_ascii=False, indent=2)
            f.write("\n")
            f.flush()
            os.fsync(f.fileno())
        os.chmod(tmp, 0o644)  # readable by other users (mkstemp makes 0600)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass
        raise


def artist_names(item: dict) -> str:
    artists = item.get("artists")
    if not isinstance(artists, list):
        return ""
    return ", ".join(str(a["name"]) for a in artists if isinstance(a, dict) and a.get("name"))


def next_tracks(queue_obj: Any) -> list:
    out = []
    items = queue_obj.get("queue") if isinstance(queue_obj, dict) else None
    for it in items if isinstance(items, list) else []:
        if not isinstance(it, dict) or it.get("type") != "track" or not it.get("id"):
            continue
        out.append({"title": it.get("name", ""), "artist": artist_names(it),
                    "duration_ms": it.get("duration_ms"), "track_id": it["id"]})
        if len(out) >= NEXT_COUNT:
            break
    return out


@dataclass
class Observation:
    track_id: str
    progress_ms: int
    duration_ms: int
    is_playing: bool
    fetched_at: datetime


def track_of(cur: Any) -> Optional[dict]:
    """The track item of a currently-playing response, or None (nothing, episode, ad)."""
    if not isinstance(cur, dict):
        return None
    item = cur.get("item")
    if cur.get("currently_playing_type", "track") != "track":
        return None
    if not isinstance(item, dict) or item.get("type") != "track" or not item.get("id"):
        return None
    return item


def is_new_play(prev: Optional[Observation], obs: Observation) -> bool:
    """Decide whether obs starts a new play (a new history_id). See README for the rules.

    Same track: a new play only when the progress went backwards, the previous play would
    have reached within END_WINDOW_MS of its end by now, and obs is no further from the start
    than the time since that expected end (+ START_WINDOW_MS). Anything else is a seek.
    """
    if prev is None or prev.track_id != obs.track_id:
        return True
    if obs.progress_ms >= prev.progress_ms:
        return False  # moving forward (or a forward seek)
    elapsed_ms = 0.0
    if prev.is_playing:
        elapsed_ms = max(0.0, (obs.fetched_at - prev.fetched_at).total_seconds() * 1000)
    expected_ms = prev.progress_ms + elapsed_ms
    if expected_ms < prev.duration_ms - END_WINDOW_MS:
        return False  # the previous play cannot have ended yet: a seek backwards
    since_end_ms = max(0.0, expected_ms - prev.duration_ms)
    return obs.progress_ms <= since_end_ms + START_WINDOW_MS


def make_history_id(track_id: str, started_at: datetime) -> str:
    # letters, digits and '-' only: CONDUCTOR.md uses it as a file name
    return "sp-%s-%d" % (track_id, int(started_at.timestamp() * 1000))


class Watcher:
    """Poll Spotify and write now_playing.json only when something a reader cares about changed.

    Written on: a new history_id, a change of is_playing, a change of `next`.
    Not written on: progress alone (including seeks inside the same play).
    """

    def __init__(self, client: Any, out_path: Path,
                 clock: Callable[[], datetime] = lambda: datetime.now(timezone.utc)) -> None:
        self.client = client
        self.out_path = Path(out_path)
        self.clock = clock
        self.payload: Optional[dict] = None
        self.prev: Optional[Observation] = None
        self.next: list = []
        self._load_last()

    def _load_last(self) -> None:
        try:
            data = json.loads(self.out_path.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            return
        if not isinstance(data, dict) or data.get("source") != SOURCE_NAME:
            return
        fetched = parse_iso(data.get("fetched_at"))
        if not data.get("content_id") or not data.get("history_id") or fetched is None:
            return
        try:
            prev = Observation(str(data["content_id"]), int(data.get("progress_ms") or 0),
                               int(data.get("duration_ms") or 0), bool(data.get("is_playing")), fetched)
        except (TypeError, ValueError):
            return
        nxt = data.get("next")
        self.payload = data
        self.next = [n for n in nxt if isinstance(n, dict)] if isinstance(nxt, list) else []
        self.prev = prev

    @staticmethod
    def _key(p: Optional[dict]) -> Any:
        if p is None:
            return None
        nxt = p.get("next") if isinstance(p.get("next"), list) else []
        return (p.get("history_id"), p.get("is_playing"),
                tuple(n.get("track_id") for n in nxt if isinstance(n, dict)))

    def _fetch_next(self) -> list:
        """The next tracks. 429 and LoginRequired propagate: the whole poll is retried later."""
        try:
            return next_tracks(self.client.queue())
        except (ApiError, OSError, ValueError) as e:
            log.warning("could not read the queue (%s): keeping the previous one", e)
            return self.next

    def observe(self) -> Optional[dict]:
        """Read Spotify once and return the payload that would be written (does not write)."""
        return self._observe()[0]

    def _observe(self) -> tuple:
        """Read Spotify once. Returns (payload, prev, next) without changing self.

        The caller commits prev/next only after the payload was handled, so a failure halfway
        (a 429 on the queue, a failed write) cannot leave a new track paired with the old
        history_id on the next poll.
        """
        cur = self.client.currently_playing()
        now = self.clock()
        item = track_of(cur)
        if item is None:
            if cur is not None:
                kind = cur.get("currently_playing_type") if isinstance(cur, dict) else type(cur).__name__
                log.debug("ignoring non-track playback (%s)", kind)
                return self.payload, self.prev, self.next
            # nothing playing (204): keep the last track and mark it stopped. progress_ms and
            # fetched_at stay those of the last time the track was seen, so played_at still
            # equals fetched_at - progress_ms.
            if self.payload is None:
                return None, self.prev, self.next
            p = dict(self.payload, is_playing=False)
            prev = self.prev
            if prev is not None:
                prev = Observation(prev.track_id, prev.progress_ms, prev.duration_ms, False, prev.fetched_at)
            return p, prev, self.next
        obs = Observation(item["id"], int(cur.get("progress_ms") or 0), int(item.get("duration_ms") or 0),
                          bool(cur.get("is_playing")), now)
        started = now - timedelta(milliseconds=obs.progress_ms)
        if is_new_play(self.prev, obs) or self.payload is None:
            history_id, detected_at = make_history_id(obs.track_id, started), iso(now)
        else:
            history_id, detected_at = self.payload["history_id"], self.payload.get("detected_at", iso(now))
        nxt = self._fetch_next()
        return {
            "title": item.get("name", ""),
            "artist": artist_names(item),
            "bpm": None,
            "key": None,
            "genre": None,
            "played_at": iso(started),
            "detected_at": detected_at,
            "source": SOURCE_NAME,
            "history_id": history_id,
            "content_id": obs.track_id,
            "album": (item.get("album") or {}).get("name", ""),
            "duration_ms": obs.duration_ms,
            "progress_ms": obs.progress_ms,
            "is_playing": obs.is_playing,
            "fetched_at": iso(now),
            "next": nxt,
        }, obs, nxt

    def poll(self) -> Optional[dict]:
        """Observe once; write and return the payload only if it changed."""
        p, prev, nxt = self._observe()
        changed = p is not None and self._key(p) != self._key(self.payload)
        if changed:
            write_atomic(self.out_path, p)  # if this raises, nothing below is committed
        # commit: from here on the new observation is the reference for the next poll.
        # Unchanged payloads are kept in memory only (newest progress), the file is not rewritten.
        self.prev, self.next = prev, nxt
        if p is not None:
            self.payload = p
        return p if changed else None


def run_forever(watcher: Watcher, interval: float, sleep: Callable[[float], None] = time.sleep,
                max_polls: Optional[int] = None) -> int:
    """Poll until interrupted. Returns 1 when a new login is needed."""
    log.info("watching Spotify every %.1f s -> %s", interval, watcher.out_path)
    backoff = interval
    polls = 0
    while max_polls is None or polls < max_polls:
        polls += 1
        try:
            p = watcher.poll()
            if p:
                log.info("%s: %s / %s (history_id %s)", "playing" if p["is_playing"] else "stopped",
                         p["artist"], p["title"], p["history_id"])
            backoff = interval
            sleep(interval)
        except LoginRequired as e:
            log.error("%s", e)
            return 1
        except RateLimited as e:
            log.warning("rate limited: waiting %.0f s", e.retry_after)
            sleep(max(e.retry_after, interval))
        except (ApiError, OSError, ValueError, KeyError, TypeError) as e:
            backoff = min(backoff * 2, MAX_BACKOFF)
            log.warning("read failed (%s): retrying in %.0f s", e, backoff)
            sleep(backoff)
    return 0


# --- login -----------------------------------------------------------------------


def login(client_id: str, token_path: Path, transport: Transport = urllib_transport, *,
          listen_host: str = "0.0.0.0", listen_port: int = 8888,
          redirect_uri: str = DEFAULT_REDIRECT_URI, timeout: float = 300.0,
          on_url: Optional[Callable[[str, int], None]] = None,
          now: Callable[[], float] = time.time) -> int:
    """Run the PKCE login: print the URL, wait for one callback, exchange the code, save tokens."""
    verifier = new_code_verifier()
    state = secrets.token_urlsafe(24)
    callback_path = urllib.parse.urlparse(redirect_uri).path or "/"
    result: dict = {}

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):  # the query holds the code: never log requests
            pass

        def _reply(self, status: int, text: str) -> None:
            body = text.encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            u = urllib.parse.urlparse(self.path)
            if u.path != callback_path:
                self._reply(404, "not found")
                return
            q = urllib.parse.parse_qs(u.query)
            got_state = (q.get("state") or [""])[0]
            if not secrets.compare_digest(got_state.encode("utf-8"), state.encode("ascii")):
                self._reply(400, "state mismatch: ignored")
                return
            if "error" in q:
                result["error"] = q["error"][0][:100]
                self._reply(400, "login failed: %s" % result["error"])
                return
            if not q.get("code"):
                self._reply(400, "no code")
                return
            result["code"] = q["code"][0]
            self._reply(200, "Login OK. You can close this tab.")

    server = HTTPServer((listen_host, listen_port), Handler)
    try:
        url = authorize_url(client_id, redirect_uri, verifier, state)
        port = server.server_address[1]
        if on_url:
            on_url(url, port)
        else:
            print("Open this URL in a browser and allow access:\n\n%s\n" % url, flush=True)
            print("Waiting for %s (listening on %s:%d) ..." % (redirect_uri, listen_host, port), flush=True)
        deadline = time.monotonic() + timeout
        while not result:
            left = deadline - time.monotonic()
            if left <= 0:
                log.error("login timed out after %.0f s", timeout)
                return 1
            server.timeout = left
            server.handle_request()
    finally:
        server.server_close()

    if "error" in result:
        log.error("login was not allowed: %s", result["error"])
        return 1
    r = _token_request(transport, {
        "grant_type": "authorization_code",
        "code": result["code"],
        "redirect_uri": redirect_uri,
        "client_id": client_id,
        "code_verifier": verifier,
    })
    if r.status != 200:
        log.error("code exchange failed (HTTP %d %s)", r.status, _error_detail(r))
        return 1
    try:
        obj = r.json()
        tokens = _tokens_from_response(obj, now())
    except (ValueError, KeyError, TypeError, AttributeError):
        log.error("code exchange returned no access token")
        return 1
    if not tokens.get("refresh_token"):
        log.warning("no refresh token was returned: `login` will be needed again in about an hour")
    save_tokens(token_path, tokens)
    log.info("logged in; tokens saved to %s", token_path)
    return 0


# --- CLI -------------------------------------------------------------------------


def parse_args(argv: Optional[list] = None) -> argparse.Namespace:
    p = argparse.ArgumentParser(description="Write the Spotify track now playing to now_playing.json")
    p.add_argument("command", nargs="?", default="watch", choices=["watch", "login"],
                   help="watch (default) or login")
    p.add_argument("--client-id", default=os.environ.get(CLIENT_ID_ENV),
                   help="Spotify app Client ID (default: $%s)" % CLIENT_ID_ENV)
    p.add_argument("--token", type=Path, default=DEFAULT_TOKEN, help="token file (mode 0600)")
    p.add_argument("--out", type=Path, default=DEFAULT_OUT, help="output JSON")
    p.add_argument("--interval", type=float, default=3.0, help="poll interval in seconds")
    p.add_argument("--once", action="store_true", help="read once, print the payload, do not write")
    p.add_argument("--redirect-uri", default=DEFAULT_REDIRECT_URI)
    p.add_argument("--listen-host", default="0.0.0.0", help="login callback bind address")
    p.add_argument("--listen-port", type=int, default=8888, help="login callback port")
    p.add_argument("--login-timeout", type=float, default=300.0)
    p.add_argument("-v", "--verbose", action="store_true")
    return p.parse_args(argv)


def main(argv: Optional[list] = None) -> int:
    args = parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    log.setLevel(logging.DEBUG if args.verbose else logging.INFO)
    if not args.client_id:
        log.error("no Client ID: pass --client-id or set %s", CLIENT_ID_ENV)
        return 2
    if args.interval <= 0:
        log.error("--interval must be > 0")
        return 2
    if args.command == "login":
        return login(args.client_id, args.token, listen_host=args.listen_host,
                     listen_port=args.listen_port, redirect_uri=args.redirect_uri,
                     timeout=args.login_timeout)
    client = SpotifyClient(TokenManager(args.token, args.client_id))
    watcher = Watcher(client, args.out)
    try:
        if args.once:
            try:
                payload = watcher.observe()
            except RateLimited as e:
                log.error("%s", e)
                return 1
            except (ApiError, OSError, ValueError, KeyError, TypeError) as e:
                log.error("read failed (%s)", e)
                return 1
            print(json.dumps(payload, ensure_ascii=False, indent=2))
            return 0
        return run_forever(watcher, args.interval)
    except LoginRequired as e:
        log.error("%s", e)
        return 1
    except KeyboardInterrupt:
        return 0


if __name__ == "__main__":
    sys.exit(main())
