# conductor（ライブ中に Claude Code が次のシェーダを裏へ用意する段取り）

曲が変わるたびに、Claude Code が映っていない予約名（llm_a / llm_b）へ次のシェーダを書くための、手順書と補助のスクリプトです。
画面の切り替え（キー 8・9、Shift+8・Shift+9）は人が行います。

## 早見表

| したいこと | すること |
|---|---|
| 指揮を始める | Claude Code に「tools/conductor/CONDUCTOR.md を読んで指揮を始めて」と頼む |
| 次の曲を待つ（手で試す） | `sh tools/conductor/wait_next.sh --timeout 60` |
| 状態を忘れて初めから | `sh tools/conductor/wait_next.sh --reset` |
| シェーダを検査する | `sh tools/conductor/check_shader.sh file.kage`（`--json` で JSON） |
| テスト | 下の「テスト」 |

## ファイル

| ファイル | 中身 |
|---|---|
| `CONDUCTOR.md` | Claude Code が従う手順書。1 周の流れ、エラーごとの扱い、してはいけないこと、段 A |
| `wait_next.sh` / `wait_next.py` | `now_playing.json` の `history_id` か `cue.json` の更新時刻が変わるまで待ち、1 行の JSON を出して終わる |
| `check_shader.sh` / `check_shader.py` | 書く前の静的な検査（1 行目・ASCII・64KiB・uniform・ループの回数の目安・`Fragment` の有無） |
| `tests/` | 上の 2 つのテスト（標準ライブラリの unittest） |
| `Dockerfile` | テストを回すための image（`python:3.12-slim`、pip では何も入れない） |

どちらのスクリプトも、macOS に最初からある `sh` と `/usr/bin/python3`（3.9）だけで動きます。jq は使いません。

## 前提

1. **U1（nowplaying）** が常駐していて、`~/.cache/kagelife/now_playing.json` を書き換えている（`tools/nowplaying/README.md`）
2. **U5（MCP の経路）**: この Mac から `ssh -T -o BatchMode=yes pc01 /home/cyokozai/.local/bin/kagelife-mcp` が通る
3. **MCP の登録**: Claude Code に上の ssh を stdio の MCP として登録してある。登録はユーザーが行います。例:

   ```sh
   claude mcp add kagelife -- ssh -T -o BatchMode=yes pc01 /home/cyokozai/.local/bin/kagelife-mcp
   ```

4. PC01 の kagelife を `-llm-slots llm_a,llm_b` 付きで起動してある

## wait_next の出力

```json
{"changed": ["song"], "now_playing": {"title": "...", "bpm": 128.0, "history_id": "..."}, "previous_history_id": "...", "cue_mtime": null, "at": "2026-11-21T12:00:03+00:00"}
```

- `changed` は `start`（前回の状態が無く、今の曲が読めた）・`song`・`cue` の組み合わせ。曲と合図が同時なら `["song", "cue"]`
- 前回の状態は `~/.cache/kagelife/conductor_state.json` に持ちます。曲が同じなら、タグを直して書き換わっても出しません
- `cue.json` は中身を読みません。更新時刻だけを見ます（U2 の中身が未定のため）
- 書きかけ・壊れた JSON は読めなかったものとして次の周で読み直します
- 終了コード: 0 = 変化あり、3 = `--timeout` で打ち切り、2 = 使い方の誤り
- オプション: `--dir`（既定 `~/.cache/kagelife`、環境変数 `KAGELIFE_CACHE` でも変えられる）、`--interval`（既定 1 秒）、`--timeout`（既定 0 = 待ち続ける）、`--follow`（変化のたびに 1 行出して終わらない。Monitor 向け）、`--reset`

## check_shader の判定

| 種類 | 内容 | 結果 |
|---|---|---|
| `first_line` | 1 行目が `//kage:unit pixels` でない（BOM も含む） | 誤り |
| `non_ascii` | ASCII 以外の文字がある（行番号を出す） | 誤り |
| `too_large` | 65536 バイトを超える | 誤り |
| `unknown_uniform` / `uniform_type` | パッケージの階層の `var` が 6 つの uniform 以外か、型が違う | 誤り |
| `no_fragment` | `func Fragment(` が無い | 誤り |
| `loop_too_heavy` | 1 画素あたりのループが 64 回を超える | 誤り |
| `loop_heavy` | 16 回以上 | 警告 |
| `loop_unknown` | 上限が整数の定数でなく数えられない | 警告 |
| `random_flash` | `Random` を使っている（全画面の明滅に使っていないか目で確かめる） | 警告 |

ループの回数は「いちばん内側のループの、外側からの回数の積」を足したものです。補助の関数の中のループも同じように足すので、呼ぶ回数は数えていません。目安です。
コンパイルの検査は `write_shader` の 422 に任せます。明るさの検査は、kagelife-shaders に `cmd/render` が入ってから（W3）足します。

## テスト

```sh
cd tools/conductor
docker build -t kagelife-conductor-test .
docker run --rm kagelife-conductor-test
docker rmi kagelife-conductor-test
```

## 確かめたこと・確かめていないこと

確かめたこと（2026-11-11）

- コンテナ（`python:3.12-slim`、Debian の `sh` は dash）で 40 件のテストが通った
- PC01（kagelife-mcp 0.1.0）へ ssh の JSON-RPC で、CONDUCTOR.md の 4・6・7 を 1 回だけ流した。`active: 04_warp` のとき llm_b に書けて、llm_a の中身（read_shader の sha256）は変わらず、書いた後も `active: 04_warp` のままだった（画面そのものは見ていない）

確かめていないこと

- macOS の `/usr/bin/python3`（3.9）と bash 3.2 での実行。構文が 3.9 で読めることだけをテストで見ている
- Claude Code に MCP を登録した状態で、CONDUCTOR.md を読ませて 1 周させること
- 曲が実際に変わったときの、U1 と組み合わせた動き
- 409・422 の扱いを実機で起こすこと
- ループの回数の目安が fps とどれだけ合うか（04_warp の 1 例しか無い）
