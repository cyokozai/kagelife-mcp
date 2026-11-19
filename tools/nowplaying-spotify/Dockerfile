# Spotify の再生中の曲を書き出す nowplaying-spotify の実行環境。
# 標準ライブラリだけで動くので pip install はしない。ローカルの Python も使わない。
FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    LANG=C.UTF-8

WORKDIR /app

COPY nowplaying_spotify.py /app/nowplaying_spotify.py
COPY tests /app/tests

ENTRYPOINT ["python", "/app/nowplaying_spotify.py"]
