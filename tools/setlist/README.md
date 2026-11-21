# setlist（良かった流れを kagelife-shaders に残す）

ライブや展示で Claude Code が曲ごとに書いたシェーダを、kagelife-shaders のプリセット 1 つとして写す道具です。
写したものは CONDUCTOR の段 A と同じ形なので、次のライブで `~/.cache/kagelife/setlist/` に写せば、段 A としてそのまま使えます。

## 材料

どちらも `tools/conductor/` の手順（`CONDUCTOR.md` の「1 周」）で、ライブ中にたまります。

| ファイル | 中身 |
|---|---|
| `~/.cache/kagelife/setlist_log.jsonl` | `note_written.sh <予約名> --history-id ... --title ... --artist ... --idea ...` が、書けた曲ごとに 1 行足した記録 |
| `~/.cache/kagelife/live/<history_id>.kage` | 曲ごとの下書き |

## 使い方

```sh
sh tools/setlist/save_setlist.sh --name 2026-11-21-live --dest ~/src/kagelife-shaders --since 2026-11-21T18:00:00+09:00
```

| オプション | 中身 |
|---|---|
| `--name`（必須） | プリセットの名前。`presets/<name>/` になる。英数字・`.`・`_`・`-` だけで、`.` で始めない |
| `--dest`（必須） | kagelife-shaders の checkout。無ければ止まる |
| `--since` | この時刻より後の記録だけを写す（ISO 8601。末尾の `Z` も可。時差が無ければ UTC とみなす）。既定は全部 |
| `--cache` | 材料のフォルダ。既定は `~/.cache/kagelife`（環境変数 `KAGELIFE_CACHE` でも変えられる） |

できるもの:

```
<dest>/presets/<name>/
  01_<slug>.kage
  02_<slug>.kage
  index.json     [{"title": "曲名", "artist": "名前", "file": "01_<slug>.kage", "idea": "1 行の案"}]
```

- 並びは記録の順です。同じ `history_id` が何度もあれば（人に頼まれて書き直したなど）、最後の記録を、その曲が最初に出た位置に置きます
- `<slug>` は曲名から作る ASCII の小文字・数字・`_` だけの名前です（40 文字まで）。曲名が日本語だけなら `history_id` から作り、それも無理なら `song` にします
- `live/` に下書きが無い曲、読めない行、`history_id` にファイル名として危ない文字がある行は、飛ばして `WARN` を出します
- `presets/<name>/` が既にあれば、何も書かずに止まります（上書きしません）。一時フォルダに書いてから名前を付け替えるので、途中で止まっても半端なフォルダは残りません
- 終了コード: 0 = 写した（警告はあってもよい）、1 = 写す曲が無い、2 = 使い方の誤り・既にある・書けない
- git の操作はしません

## 写した後の警告（kagelife-shaders の検査）

kagelife-shaders の `shaders_test.go`（CI でも走る）は、`presets/*/*.kage` の先頭 5 行以内に `//kage:unit pixels` があることと、
宣言した Uniform が `Time`・`Beat`・`Frame`・`Random`・`Resolution`・`Cursor` の 6 つ（名前と型）に収まることを確かめます。
save_setlist は写した後に同じ 2 点を見て、合わないファイルを `WARN <file>: ...` で一覧にします。写すのはやめません。

kagelife-mcp の手引きには音の予約 Uniform（`AudioBass`・`AudioMid`・`AudioHigh`・`AudioOnset`）があるので、それを使ったシェーダは警告に出ます。
そのまま kagelife-shaders に入れると CI に落ちる見込みが高いので、PR の前に直すか外します。
コンパイル（`ebiten.NewShader`）はこの道具では見ません。kagelife-shaders の CI か、手元のコンテナの `go test ./...` で確かめます。

## kagelife-shaders に入れる（人が行う）

1. kagelife-shaders の checkout で、作業用のブランチを切る
2. `save_setlist.sh` を `--dest` にその checkout を渡して実行する
3. `WARN` に出たファイルを直すか外す。外したら `index.json` の行も消し、番号を詰めるかどうかを決める
4. `git add presets/<name>` して commit し、PR を作る

## 次のライブで段 A として使う

```sh
mkdir -p ~/.cache/kagelife/setlist
cp <kagelife-shaders>/presets/<name>/*.kage <kagelife-shaders>/presets/<name>/index.json ~/.cache/kagelife/setlist/
```

`~/.cache/kagelife/setlist/` に前の段 A が残っていれば、先に別の場所へ移してから写します。

## テスト

```sh
docker run --rm -e LANG=C.UTF-8 -v <リポジトリの絶対パス>:/src -w /src python:3.12-slim \
  python -m unittest discover -s tools/setlist/tests -v
```

`save_setlist.py` は `tools/conductor/check_shader.py` の Uniform の読み取りを使うので、リポジトリ全体をコンテナに渡します。
標準ライブラリだけで書いてあり、macOS の `/usr/bin/python3`（3.9）でも動く書き方にしています（テストで 3.9 の構文として読めることを見ています）。
