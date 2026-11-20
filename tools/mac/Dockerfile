# start.sh / stop.sh のテストを回すための image。標準ライブラリだけで動くので pip では何も入れない。
# Debian の sh（dash）で、POSIX sh として動くかも同時に見る。本番は Mac の sh で直接動かす。
FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    LANG=C.UTF-8

WORKDIR /app
COPY . /app

ENTRYPOINT ["python", "-m", "unittest", "discover", "-s", "/app/tests", "-v"]
