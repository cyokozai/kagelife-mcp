"""Run scenario.json through the Python version and write golden.json.

The Go test TestCrossCheckWithPythonVersion runs the same scenario through the Go version and
compares now_playing.json byte for byte after every step. Regenerate after changing the scenario:

    docker run --rm -v "$PWD":/src -w /src/internal/nowplaying/spotify/testdata/crosscheck \
      python:3.12-slim python gen_golden.py /src/tools/nowplaying-spotify
"""

import json
import sys
import tempfile
from datetime import datetime, timedelta
from pathlib import Path

sys.path.insert(0, sys.argv[1])
import nowplaying_spotify as nps  # noqa: E402


class Client:
    def __init__(self):
        self.current = None
        self.queue_items = []

    def currently_playing(self):
        return self.current

    def queue(self):
        return {"currently_playing": None, "queue": self.queue_items}


def main():
    scenario = json.loads(Path("scenario.json").read_text(encoding="utf-8"))
    now = [datetime.fromisoformat(scenario["start"])]
    client = Client()
    out = []
    with tempfile.TemporaryDirectory() as d:
        path = Path(d) / "now_playing.json"
        w = nps.Watcher(client, path, clock=lambda: now[0])
        for step in scenario["steps"]:
            now[0] = now[0] + timedelta(seconds=step["advance"])
            client.current = step["current"]
            client.queue_items = step["queue"]
            written = w.poll() is not None
            out.append({"note": step["note"], "written": written,
                        "file": path.read_text(encoding="utf-8") if path.exists() else None})
    Path("golden.json").write_text(json.dumps(out, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
