"""rekordbox の再生履歴を見張り、今かかっている曲を JSON に書き出す。

master.db は読み取り専用で扱う。rekordbox が動いている最中でもロックや WAL に
触らないよう、毎回 master.db と master.db-wal を一時の場所へ写し、写しを開く。
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import shutil
import sys
import tempfile
import time
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable, Optional, Protocol

SOURCE_NAME = "rekordbox-history"
DB_NAME = "master.db"
# -shm は写さない。写しを開いた SQLite が -wal から作り直す。
SNAPSHOT_SUFFIXES = ("", "-wal")
DEFAULT_DB_DIR = Path.home() / "Library" / "Pioneer" / "rekordbox"
DEFAULT_OUT = Path.home() / ".cache" / "kagelife" / "now_playing.json"

log = logging.getLogger("nowplaying")


@dataclass(frozen=True)
class Track:
    history_id: str
    content_id: str
    title: str
    artist: str
    bpm: Optional[float]
    key: Optional[str]
    genre: Optional[str]
    played_at: Optional[str]


class HistorySource(Protocol):
    def latest(self) -> Optional[Track]: ...


# --- 書き出し ---------------------------------------------------------------


def write_atomic(path: Path, obj: Any) -> None:
    """obj を JSON にして path へ原子的に書く（同じフォルダの一時ファイル → rename）。"""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=f".{path.name}.", suffix=".tmp", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(obj, f, ensure_ascii=False, indent=2)
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


def build_payload(track: Track, detected_at: datetime) -> dict:
    return {
        "title": track.title,
        "artist": track.artist,
        "bpm": track.bpm,
        "key": track.key,
        "genre": track.genre,
        "played_at": track.played_at,
        "detected_at": detected_at.isoformat(),
        "source": SOURCE_NAME,
        "history_id": track.history_id,
        "content_id": track.content_id,
    }


# --- 差分の判定 -------------------------------------------------------------


class Watcher:
    """履歴の最新の行が変わったときだけ書き出す。

    同じ履歴の行（history_id）と、直前に書いた曲と同じ曲（content_id）は書かない。
    起動時には既存の出力を読み、再起動しても同じ曲を書き直さない。
    """

    def __init__(
        self,
        source: HistorySource,
        out_path: Path,
        clock: Callable[[], datetime] = lambda: datetime.now(timezone.utc),
    ) -> None:
        self.source = source
        self.out_path = Path(out_path)
        self.clock = clock
        self.last_history_id: Optional[str] = None
        self.last_content_id: Optional[str] = None
        self._load_last()

    def _load_last(self) -> None:
        try:
            data = json.loads(self.out_path.read_text(encoding="utf-8"))
        except (FileNotFoundError, ValueError):
            return
        if isinstance(data, dict):
            self.last_history_id = data.get("history_id")
            self.last_content_id = data.get("content_id")

    def is_new(self, track: Track) -> bool:
        if track.history_id == self.last_history_id:
            return False
        if track.content_id and track.content_id == self.last_content_id:
            return False
        return True

    def poll(self) -> Optional[dict]:
        track = self.source.latest()
        if track is None or not self.is_new(track):
            return None
        payload = build_payload(track, self.clock())
        write_atomic(self.out_path, payload)
        self.last_history_id = track.history_id
        self.last_content_id = track.content_id
        return payload


# --- master.db を写して読む -------------------------------------------------


def _stat_key(src_dir: Path) -> tuple:
    key = []
    for suffix in SNAPSHOT_SUFFIXES:
        try:
            st = (src_dir / (DB_NAME + suffix)).stat()
            key.append((st.st_size, st.st_mtime_ns))
        except FileNotFoundError:
            key.append(None)
    return tuple(key)


def snapshot_db(src_dir: Path, dst_dir: Path, retries: int = 3) -> Path:
    """master.db と master.db-wal を dst_dir へ写し、写した master.db の場所を返す。

    写している間に元が変わった（大きさか更新時刻が違う）ときは写し直す。
    retries 回やっても落ち着かなければ、最後の写しをそのまま使う。
    """
    src_dir, dst_dir = Path(src_dir), Path(dst_dir)
    if not (src_dir / DB_NAME).is_file():
        raise FileNotFoundError(f"{src_dir / DB_NAME} がありません")
    for _ in range(max(1, retries)):
        before = _stat_key(src_dir)
        for suffix in SNAPSHOT_SUFFIXES:
            src = src_dir / (DB_NAME + suffix)
            dst = dst_dir / (DB_NAME + suffix)
            if src.is_file():
                shutil.copyfile(src, dst)
            elif dst.exists():
                dst.unlink()
        if _stat_key(src_dir) == before:
            break
    return dst_dir / DB_NAME


def track_from_row(row: Any) -> Track:
    """pyrekordbox の DjmdSongHistory の行を Track に直す。"""
    content = getattr(row, "Content", None)
    title = artist = ""
    bpm = key = genre = None
    if content is not None:
        title = content.Title or ""
        artist = content.ArtistName or ""
        # rekordbox は BPM を 100 倍の整数で持つ（12850 = 128.50）
        if content.BPM:
            bpm = content.BPM / 100
        key_obj = getattr(content, "Key", None)
        key = getattr(key_obj, "ScaleName", None) or None
        genre = content.GenreName or None
    created = getattr(row, "created_at", None)
    played_at = created.isoformat() if isinstance(created, datetime) else (
        str(created) if created else None
    )
    return Track(
        history_id=str(row.ID),
        content_id=str(row.ContentID or ""),
        title=title,
        artist=artist,
        bpm=bpm,
        key=key,
        genre=genre,
        played_at=played_at,
    )


def open_master_db(db_path: Path) -> Any:
    """pyrekordbox で写しの master.db を開く。テストでは差し替える。"""
    from pyrekordbox.masterdb import MasterDatabase

    return MasterDatabase(path=db_path, db_dir=db_path.parent)


def query_history(db: Any) -> tuple[Optional[Any], int]:
    """(最新の行, 履歴の行数) を返す。削除済みの行は数えない。"""
    from pyrekordbox.masterdb import models

    q = db.query(models.DjmdSongHistory).filter(
        models.DjmdSongHistory.rb_local_deleted == 0
    )
    count = q.count()
    latest = q.order_by(
        models.DjmdSongHistory.created_at.desc(),
        models.DjmdSongHistory.TrackNo.desc(),
    ).first()
    return latest, count


class RekordboxHistorySource:
    """master.db を毎回写して開き、履歴の最新の行を読む。"""

    def __init__(
        self,
        db_dir: Path,
        opener: Callable[[Path], Any] = open_master_db,
        query: Callable[[Any], tuple[Optional[Any], int]] = query_history,
    ) -> None:
        self.db_dir = Path(db_dir)
        self.opener = opener
        self.query = query
        self.last_count: Optional[int] = None

    def latest(self) -> Optional[Track]:
        with tempfile.TemporaryDirectory(prefix="nowplaying-") as work:
            copied = snapshot_db(self.db_dir, Path(work))
            db = self.opener(copied)
            try:
                row, count = self.query(db)
                self.last_count = count
                return track_from_row(row) if row is not None else None
            finally:
                close = getattr(db, "close", None)
                if close:
                    close()


# --- CLI --------------------------------------------------------------------


def parse_args(argv: Optional[list[str]] = None) -> argparse.Namespace:
    p = argparse.ArgumentParser(
        description="rekordbox の再生履歴から今の曲を JSON に書き出す"
    )
    p.add_argument("--db-dir", type=Path, default=DEFAULT_DB_DIR,
                   help="master.db のあるフォルダ（読み取り専用で扱う）")
    p.add_argument("--out", type=Path, default=DEFAULT_OUT,
                   help="書き出し先の JSON")
    p.add_argument("--interval", type=float, default=3.0,
                   help="常駐モードで履歴を見る間隔（秒）")
    p.add_argument("--once", action="store_true",
                   help="1 回だけ読み、最新の行と行数を表示して終わる（書き出さない）")
    p.add_argument("-v", "--verbose", action="store_true")
    return p.parse_args(argv)


def run_once(source: RekordboxHistorySource) -> int:
    track = source.latest()
    result = {
        "history_count": source.last_count,
        "latest": asdict(track) if track else None,
    }
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


def run_forever(watcher: Watcher, interval: float) -> int:
    log.info("見張りを始めます（%.1f 秒ごと）→ %s", interval, watcher.out_path)
    while True:
        try:
            payload = watcher.poll()
            if payload:
                log.info("新しい曲: %s / %s", payload["artist"], payload["title"])
        except Exception as e:  # 一時的な読み損ないで止めない
            log.warning("読み取りに失敗しました: %s", e)
        time.sleep(interval)


def main(argv: Optional[list[str]] = None) -> int:
    args = parse_args(argv)
    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO,
        format="%(asctime)s %(levelname)s %(message)s",
    )
    # pyrekordbox の「Rekordbox is running!」などの警告は読み取りには関係しないので抑える
    if not args.verbose:
        logging.getLogger("pyrekordbox").setLevel(logging.ERROR)
    source = RekordboxHistorySource(args.db_dir)
    if args.once:
        return run_once(source)
    try:
        return run_forever(Watcher(source, args.out), args.interval)
    except KeyboardInterrupt:
        return 0


if __name__ == "__main__":
    sys.exit(main())
