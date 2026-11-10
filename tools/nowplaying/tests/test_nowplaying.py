"""nowplaying の単体テスト。実際の master.db と pyrekordbox には頼らない。"""

import json
import os
import sys
from datetime import datetime, timezone
from pathlib import Path
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import nowplaying  # noqa: E402
from nowplaying import Track, Watcher, snapshot_db, track_from_row, write_atomic  # noqa: E402


def make_track(history_id="h1", content_id="c1", title="Song A"):
    return Track(
        history_id=history_id,
        content_id=content_id,
        title=title,
        artist="Artist A",
        bpm=128.0,
        key="8A",
        genre="Techno",
        played_at="2026-11-21T21:00:00",
    )


class FakeSource:
    """latest() が返す値を順に差し替えられる履歴の偽物。"""

    def __init__(self, tracks):
        self.tracks = list(tracks)

    def latest(self):
        if len(self.tracks) > 1:
            return self.tracks.pop(0)
        return self.tracks[0] if self.tracks else None


FIXED_NOW = datetime(2026, 11, 21, 12, 0, 0, tzinfo=timezone.utc)


def new_watcher(source, out):
    return Watcher(source, out, clock=lambda: FIXED_NOW)


# --- 差分を見つける ---------------------------------------------------------


def test_poll_writes_first_track(tmp_path):
    out = tmp_path / "now_playing.json"
    w = new_watcher(FakeSource([make_track()]), out)

    written = w.poll()

    assert written is not None
    data = json.loads(out.read_text(encoding="utf-8"))
    assert data == {
        "title": "Song A",
        "artist": "Artist A",
        "bpm": 128.0,
        "key": "8A",
        "genre": "Techno",
        "played_at": "2026-11-21T21:00:00",
        "detected_at": "2026-11-21T12:00:00+00:00",
        "source": "rekordbox-history",
        "history_id": "h1",
        "content_id": "c1",
    }


def test_poll_writes_when_new_history_row_appears(tmp_path):
    out = tmp_path / "now_playing.json"
    w = new_watcher(
        FakeSource([make_track(), make_track("h2", "c2", "Song B")]), out
    )
    w.poll()

    written = w.poll()

    assert written is not None
    assert json.loads(out.read_text(encoding="utf-8"))["title"] == "Song B"


def test_poll_with_empty_history_writes_nothing(tmp_path):
    out = tmp_path / "now_playing.json"
    w = new_watcher(FakeSource([]), out)

    assert w.poll() is None
    assert not out.exists()


# --- 重複を書かない ---------------------------------------------------------


def test_poll_does_not_rewrite_same_history_row(tmp_path):
    out = tmp_path / "now_playing.json"
    w = new_watcher(FakeSource([make_track()]), out)
    w.poll()
    before = out.stat().st_mtime_ns
    os.utime(out, ns=(before - 10_000_000, before - 10_000_000))
    before = out.stat().st_mtime_ns

    assert w.poll() is None
    assert out.stat().st_mtime_ns == before


def test_poll_skips_same_content_in_new_history_row(tmp_path):
    # ヘッドホンで頭出ししてから本番で流すと、同じ曲が続けて 2 行載りうる
    out = tmp_path / "now_playing.json"
    w = new_watcher(FakeSource([make_track("h1", "c1"), make_track("h2", "c1")]), out)
    w.poll()

    assert w.poll() is None
    assert json.loads(out.read_text(encoding="utf-8"))["history_id"] == "h1"


def test_restarted_watcher_does_not_rewrite_existing_output(tmp_path):
    out = tmp_path / "now_playing.json"
    new_watcher(FakeSource([make_track()]), out).poll()

    restarted = new_watcher(FakeSource([make_track()]), out)

    assert restarted.poll() is None


def test_broken_existing_output_is_overwritten(tmp_path):
    out = tmp_path / "now_playing.json"
    out.write_text("{broken", encoding="utf-8")
    w = new_watcher(FakeSource([make_track()]), out)

    assert w.poll() is not None
    assert json.loads(out.read_text(encoding="utf-8"))["history_id"] == "h1"


# --- 原子的に書く -----------------------------------------------------------


def test_write_atomic_creates_parent_and_leaves_no_temp(tmp_path):
    out = tmp_path / "a" / "b" / "now_playing.json"

    write_atomic(out, {"title": "日本語の曲"})

    assert json.loads(out.read_text(encoding="utf-8")) == {"title": "日本語の曲"}
    assert sorted(p.name for p in out.parent.iterdir()) == ["now_playing.json"]


def test_write_atomic_keeps_old_file_when_write_fails(tmp_path):
    out = tmp_path / "now_playing.json"
    write_atomic(out, {"title": "old"})

    with pytest.raises(TypeError):
        write_atomic(out, {"title": object()})

    assert json.loads(out.read_text(encoding="utf-8")) == {"title": "old"}
    assert sorted(p.name for p in tmp_path.iterdir()) == ["now_playing.json"]


def test_write_atomic_uses_rename(tmp_path, monkeypatch):
    out = tmp_path / "now_playing.json"
    calls = []
    real_replace = os.replace

    def spy(src, dst):
        calls.append((Path(src).parent, Path(dst)))
        real_replace(src, dst)

    monkeypatch.setattr(nowplaying.os, "replace", spy)

    write_atomic(out, {"title": "x"})

    # 一時ファイルは同じフォルダに作る（rename が同じファイルシステム内で済むように）
    assert calls == [(tmp_path, out)]


# --- 一時の場所へ写す -------------------------------------------------------


def test_snapshot_copies_db_and_wal_but_not_shm(tmp_path):
    src = tmp_path / "rekordbox"
    src.mkdir()
    (src / "master.db").write_bytes(b"db")
    (src / "master.db-wal").write_bytes(b"wal")
    (src / "master.db-shm").write_bytes(b"shm")
    dst = tmp_path / "work"
    dst.mkdir()

    copied = snapshot_db(src, dst)

    assert copied == dst / "master.db"
    assert (dst / "master.db").read_bytes() == b"db"
    assert (dst / "master.db-wal").read_bytes() == b"wal"
    assert not (dst / "master.db-shm").exists()
    assert (src / "master.db").read_bytes() == b"db"


def test_snapshot_without_wal(tmp_path):
    src = tmp_path / "rekordbox"
    src.mkdir()
    (src / "master.db").write_bytes(b"db")
    dst = tmp_path / "work"
    dst.mkdir()

    snapshot_db(src, dst)

    assert sorted(p.name for p in dst.iterdir()) == ["master.db"]


def test_snapshot_missing_db_raises(tmp_path):
    with pytest.raises(FileNotFoundError):
        snapshot_db(tmp_path, tmp_path)


# --- pyrekordbox の行を Track に直す ----------------------------------------


def test_track_from_row_maps_fields():
    content = SimpleNamespace(
        ID="c9",
        Title="Song Z",
        ArtistName="Artist Z",
        BPM=12850,
        Key=SimpleNamespace(ScaleName="Fm"),
        GenreName="House",
    )
    row = SimpleNamespace(
        ID="h9",
        ContentID="c9",
        Content=content,
        created_at=datetime(2026, 11, 21, 21, 5, 3),
    )

    t = track_from_row(row)

    assert t == Track(
        history_id="h9",
        content_id="c9",
        title="Song Z",
        artist="Artist Z",
        bpm=128.5,
        key="Fm",
        genre="House",
        played_at="2026-11-21T21:05:03",
    )


def test_track_from_row_tolerates_missing_metadata():
    content = SimpleNamespace(
        ID="c1", Title=None, ArtistName=None, BPM=0, Key=None, GenreName=None
    )
    row = SimpleNamespace(ID="h1", ContentID="c1", Content=content, created_at=None)

    t = track_from_row(row)

    assert (t.title, t.artist, t.bpm, t.key, t.genre, t.played_at) == (
        "", "", None, None, None, None
    )


def test_rekordbox_source_reads_snapshot_not_original(tmp_path):
    src = tmp_path / "rekordbox"
    src.mkdir()
    (src / "master.db").write_bytes(b"db")
    opened = []

    class FakeDB:
        closed = False

        def close(self):
            FakeDB.closed = True

    def opener(path):
        opened.append(path)
        assert path.read_bytes() == b"db"
        return FakeDB()

    row = SimpleNamespace(ID="h1", ContentID="c1", Content=None, created_at=None)
    source = nowplaying.RekordboxHistorySource(
        src, opener=opener, query=lambda db: (row, 42)
    )

    t = source.latest()

    assert t.history_id == "h1"
    assert source.last_count == 42
    assert FakeDB.closed
    assert opened[0].parent != src  # 元のフォルダを直接開かない
    assert not opened[0].exists()  # 読み終えたら写しは消える


def test_track_from_row_without_content():
    row = SimpleNamespace(ID="h1", ContentID="c1", Content=None, created_at=None)

    t = track_from_row(row)

    assert t.history_id == "h1"
    assert t.title == ""
