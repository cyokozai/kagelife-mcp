# conductor の補助スクリプトのテスト環境。標準ライブラリだけで動くので pip では何も入れない。
# 本番（この Mac）では /usr/bin/python3 と sh で直接動かす。
FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    LANG=C.UTF-8

WORKDIR /app
COPY . /app

ENTRYPOINT ["python", "-m", "unittest", "discover", "-s", "/app/tests", "-v"]
