# Runtime for nowplaying-spotify. Standard library only: no pip install.
FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    LANG=C.UTF-8

WORKDIR /app

COPY nowplaying_spotify.py /app/nowplaying_spotify.py
COPY tests /app/tests

ENTRYPOINT ["python", "/app/nowplaying_spotify.py"]
