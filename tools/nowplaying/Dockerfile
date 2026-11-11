# rekordbox の再生履歴を読む nowplaying の実行環境。
# ローカルに Python のパッケージを入れないため、すべてこの image の中で動かす。
FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    LANG=C.UTF-8

WORKDIR /app

COPY requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r /app/requirements.txt

COPY nowplaying.py /app/nowplaying.py
COPY tests /app/tests

ENTRYPOINT ["python", "/app/nowplaying.py"]
