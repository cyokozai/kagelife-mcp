# conductor（ライブ中に Claude Code が次のシェーダを裏へ用意する段取り）

曲が変わるたびに、Claude Code が映っていない予約名（llm_a / llm_b）へ次のシェーダを書くための、手順書と補助のスクリプトです。
Claude Code は **VJ 機（Linux）** で動かし、人と Claude の両方が操作します。画面の切り替えは人が行います。

## 置き場所

下の `<C>` は、このフォルダの絶対パスです。機械ごとに置き換えて使います。

| 機械 | `<C>` | ここで動かすもの |
|---|---|---|
| **VJ 機（主）** | `~/kagelife-mcp/tools/conductor` | Claude Code、`wait_next.sh`・`back_slot.sh`・`note_written.sh`・`check_shader.sh` |
| Mac | `<clone した場所>/tools/conductor` | U1（nowplaying）と `push_now_playing.sh` |

どちらの機械でも、このリポジトリの checkout を上の場所に置き、`dev` の版に更新してあることを前提にしています。
VJ 機にも、上の場所への clone が要ります。

## 早見表

| したいこと | すること |
|---|---|
| 今の曲を VJ 機へ送り続ける（Mac） | `sh <C>/push_now_playing.sh vj` |
| 1 回だけ送る（Mac） | `sh <C>/push_now_playing.sh --once vj` |
| 指揮を始める（VJ 機） | Claude Code に「`<C>/CONDUCTOR.md` を読んで指揮を始めて」と頼む |
| 次の曲を待つ（手で試す） | `sh <C>/wait_next.sh --timeout 60` |
| 曲の記録を忘れて初めから | `sh <C>/wait_next.sh --reset`（`last_slot` は残る） |
| 次の書く先を見る | `sh <C>/back_slot.sh 04_warp` |
| 書いた予約名を記録する | `sh <C>/note_written.sh llm_a` |
| シェーダを検査する | `sh <C>/check_shader.sh file.kage`（`--json` で JSON） |
| テスト | 下の「テスト」 |

## 全体の流れ

```
Mac:   rekordbox -> U1 nowplaying -> ~/.cache/kagelife/now_playing.json
                                       | push_now_playing.sh (ssh, tmp -> mv)
VJ 機: ~/.cache/kagelife/now_playing.json  <- Stream Deck: ~/.cache/kagelife/cue.json
         | wait_next.sh
       Claude Code (tmux vj の 0 番) -> kagelife-mcp (stdio, 同じ機械) -> kagelife の llm_a / llm_b
```

## ファイル

| ファイル | 中身 |
|---|---|
| `CONDUCTOR.md` | Claude Code が従う手順書。1 周の流れ、人と一緒に操作する決まり、エラーごとの扱い、してはいけないこと、段 A |
| `push_now_playing.sh` / `.py` | （Mac）`now_playing.json` が変わったら、ssh で VJ 機の同じ場所へ一時ファイル → mv で書く |
| `wait_next.sh` / `.py` | `now_playing.json` の `history_id` か `cue.json` の更新時刻が変わるまで待ち、1 行の JSON を出して終わる。起動のたびに下書きの置き場所 `live/` を作る |
| `back_slot.sh` / `note_written.sh` / `slots.py` | 書く先を決める（`back_slot.sh <active>`）。書けた予約名を `conductor_state.json` の `last_slot` に記録する（`note_written.sh <name>`） |
| `check_shader.sh` / `.py` | 書く前の静的な検査（1 行目・ASCII・64KiB・uniform・ループの回数の目安・`Fragment` の有無） |
| `tests/` | 上のスクリプトのテスト（標準ライブラリの unittest） |
| `Dockerfile` | テストを回すための image（`python:3.12-slim`、pip では何も入れない） |

スクリプトは `sh` と PATH の `python3`（標準ライブラリだけ）で動きます。3.9 以上で動きます（3.9.6 の `/usr/bin/python3` と Homebrew の 3.15 で確かめた。VJ 機の Ubuntu 24.04 は 3.12）。jq は使いません。

## 前提

1. **U1（nowplaying）** が Mac で常駐していて、`~/.cache/kagelife/now_playing.json` を書き換えている（`tools/nowplaying/README.md`）
2. Mac で `push_now_playing.sh` が動いていて、Mac から `ssh vj` が鍵で通る（パスワードを聞かれない）
3. **MCP の登録（VJ 機）**: VJ 機の Claude Code に、同じ機械の `~/.local/bin/kagelife-mcp` を stdio の MCP として登録してある。登録は人が行います。例:

   ```sh
   claude mcp add kagelife -- ~/.local/bin/kagelife-mcp
   ```

4. VJ 機の kagelife を `-llm-slots llm_a,llm_b` 付きで起動してある
5. VJ 機の画面: picom が tmux vj の 0 番の端末（Claude Code）を kagelife に重ねている。人は F12 で端末を出し入れし、Super+1〜9・0・Space・h と Super+Shift+1〜9 で kagelife を操作する

## push_now_playing（Mac）

- `sh <C>/push_now_playing.sh [--once] [host]`。host の既定は `vj`
- 1 秒ごとに `~/.cache/kagelife/now_playing.json` を読み、中身が前に送ったものと違えば `ssh -T -o BatchMode=yes -o ConnectTimeout=5 <host>` で送る
- 送り先では同じフォルダの一時ファイルに書いてから `mv` で置き換えます。フォルダが無ければ作ります
- 書きかけ・壊れた JSON は送りません。同じ中身は 2 度送りません（覚えているのは動いている間だけなので、立て直すと 1 回送り直します）
- 送れなかったら、中身が変わったときか `--retry` 秒後（既定 10 秒）に送り直します
- オプション: `--src`、`--remote-dir`（既定 `~/.cache/kagelife`。`~/` は送り先の `$HOME`）、`--interval`、`--retry`、`--timeout`（テスト用）
- ssh は環境変数 `KAGELIFE_PUSH_SSH` で差し替えられます（テスト用）
- 終了コード: 0 = 正常、1 = `--once` で送れなかった、2 = 使い方の誤り

## 書く先の決め方

`back_slot.sh <active>` は、`active` が llm_a なら llm_b、llm_b なら llm_a を出します。それ以外（1〜7 などが映っている）なら、
`conductor_state.json` の `last_slot` でない方を出します。記録が無ければ llm_a です。
`last_slot` はファイルに残るので、Claude Code のセッションを立て直しても引き継がれます。`wait_next.sh --reset` でも消えません。

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
cd <C>
docker build -t kagelife-conductor-test .
docker run --rm kagelife-conductor-test
docker rmi kagelife-conductor-test
```

## 確かめたこと・確かめていないこと

確かめたこと（2026-11-11）

- コンテナ（`python:3.12-slim`、Debian の `sh` は dash）で 62 件のテストが通った
- この Mac の `/usr/bin/python3`（3.9.6）で、check_shader（試しのシェーダと 04_warp）と wait_next（start・song・cue・打ち切りの rc=3）が動いた
- Mac から VJ 機（kagelife-mcp 0.1.0）へ ssh の JSON-RPC で、CONDUCTOR.md の 4・6・7 を 1 回だけ流した。`active: 04_warp` のとき llm_b に書けて、llm_a の中身（read_shader の sha256）は変わらず、書いた後も `active: 04_warp` のままだった（画面そのものは見ていない）
- Mac で MCP を登録した Claude Code に CONDUCTOR.md を読ませて 1 周させ、llm_a に書けた。`active` は 04_warp のままだった
- Mac の PATH の python3 で `push_now_playing.sh --once` を流し、VJ 機 の仮の場所（`/tmp/u3cd-push-test/`）へ届いた。`ssh vj cat` で中身が同じことを見てから消した

確かめていないこと

- VJ 機で Claude Code を動かし、MCP を ssh なしで登録して 1 周させること（VJ 機の checkout がまだ無い）
- `push_now_playing.sh` の常駐（見張り続ける形）を実機で動かすこと。U1 と組み合わせて、曲が実際に変わったときに届くまでの時間
- `back_slot.sh`・`note_written.sh` を Mac の python3 3.9 で動かすこと（コンテナのテストと、3.9 の構文として読めることだけを見ている）
- 端末を重ねた画面で、Claude の 1 行の知らせが客席からどう見えるか
- 409・422 の扱いを実機で起こすこと
- ループの回数の目安が fps とどれだけ合うか（04_warp の 1 例しか無い）
