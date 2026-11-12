# CONDUCTOR（ライブ中にシェーダを裏へ用意する手順）

Claude Code がこのファイルを読み、曲が変わるたびに、映っていない予約名（llm_a / llm_b）へ次のシェーダを書く。
画面を切り替えるのは人。Claude は拍に間に合わないので、曲ごとの用意だけを受け持つ。

## 置き場所（最初に 1 回だけ決める）

下のコマンドの `<C>` は、このファイルがあるフォルダの絶対パス。動かす機械に合わせて置き換えて打つ。
Bash は呼ぶたびに変数を忘れるので、変数にはせず、毎回パスを書く。

| 機械 | `<C>` |
|---|---|
| **VJ 機（主。Linux）** | `~/kagelife-mcp/tools/conductor` |
| Mac（予備） | `<clone した場所>/tools/conductor` |

VJ 機では、MCP（kagelife-mcp）は同じ機械で動き、今の曲は Mac から `~/.cache/kagelife/now_playing.json` に送られてくる（Mac 側の `push_now_playing.sh`）。
Stream Deck の合図は `~/.cache/kagelife/cue.json` に書かれる。

## 早見表

| 場面 | すること |
|---|---|
| 始める | 最初の 1 回だけ `sh <C>/wait_next.sh --reset --timeout 900` で待つ（今かかっている曲がすぐ `start` で出る）。続けて下の「1 周」を回す |
| 待つ（2 回目から） | `sh <C>/wait_next.sh --timeout 900` を背景で実行し、終わるのを待つ |
| 書く先 | `sh <C>/back_slot.sh <get_state の active>` が出す名前 |
| 書く前 | `sh <C>/check_shader.sh <file>` が `OK` になるまで直す |
| 書けた | `sh <C>/note_written.sh <予約名>` で記録し、端末に 1 行だけ書く。例: 「llm_b に書いた。Super+Shift+9 で出せます」 |
| 人から指示が来た | 自動の流れより先に従う（下の「人と一緒に操作する」） |
| 422 | 直して書き直す。**3 回**で諦め、1 行で伝える |
| 409 | フェード中。2 秒ごとに `get_state` を見て `fade: idle` になってから、書く先を決め直す（60 秒で諦める） |
| 403 | 予約名以外へ書こうとした。書かずに止まり、伝える |
| 段 A の曲 | `~/.cache/kagelife/setlist/index.json` に今の曲があれば、そのファイルを検査して書くだけ |

キーと予約名: 8 = llm_a、9 = llm_b。1〜7 は basic-vj、0 は直前の予約名以外へ戻る。Shift を足すとクロスフェード。

## 人と一緒に操作する（VJ 機の画面）

- VJ 機の画面では、picom が「Claude Code が映った端末（tmux vj の 0 番）」を kagelife の上に重ねている
- 人は F12 で端末を出し入れする
- 人は Super+1〜9・0・Space・h と Super+Shift+1〜9 で kagelife を操作する。フォーカスは端末のまま
- **人の操作が先。** Claude は頼まれない限り切り替えない（`switch_shader`・`crossfade` を呼ばない）。
  頼まれたときも、`get_state` の `active` が前に見たときから変わっていたら、人が切り替えた直後とみなし、30 秒待つか人に確かめてから切り替える
- 人に知らせるのは端末で、短く 1 行。例: 「llm_b に書いた。Super+Shift+9 で出せます」
- 人は端末で Claude に指示できる（例: 「次は暗めに」「今の曲は〇〇」）。指示が来たら、自動の流れより先に従う。
  「今の曲は〇〇」と言われたら、now_playing より人の言った曲を使う
- **端末の文字は客席から見える。** トークン・control.json・パスワード・`.env` の中身を画面に出さない。長い出力（ファイルの全文、JSON の全体）も出さない

## してはいけないこと

- 予約名（llm_a・llm_b）以外へ書く
- 頼まれずに `switch_shader`・`crossfade`・`set_bpm` を呼ぶ（人がその場で言ったときだけ）
- 光の決まりを破る（下の「シェーダの決まり」）
- control.json・トークン・パスワード・`.env` を読む・表示する
- 1 曲のあいだに何本も書く。1 曲 1 本（直しの書き直しと、人に頼まれた分は数えない）

## 1 周

1. **待つ。** Bash の run_in_background で実行する。
   - 指揮を始めて最初の 1 回: `sh <C>/wait_next.sh --reset --timeout 900`。
     前回の `conductor_state.json` が残っていると、今かかっている曲では `start` が出ず、次の曲まで待ってしまうため。
     `--reset` で忘れるのは曲と合図の記録だけで、最後に書いた予約名（`last_slot`）は残る
   - 2 回目から: `sh <C>/wait_next.sh --timeout 900`
   - 終わったら 1 行の JSON が出る。`changed` が `start`（初回）・`song`（曲が変わった）・`cue`（「次の曲」ボタン）のどれか
   - 終了コード 3 は「15 分なにも起きなかった」。2 回目からのコマンドで待ち直す
   - Monitor で見張るなら `--follow` を付ける（変化のたびに 1 行出し、終わらない）
   - 下書きの置き場所 `~/.cache/kagelife/live/` は、wait_next が起動のたびに作る
   - 待っている間に人から指示が来たら、そちらを先に片付ける
2. **曲を読む。** 出力の `now_playing` の `title`・`artist`・`bpm`・`key`・`genre` を見る。
   `now_playing` が `null` のとき（合図だけ来た）は、前の曲のまま次の絵を用意する。
3. **段 A の曲か確かめる。** `~/.cache/kagelife/setlist/index.json` があり、`title`（と `artist`）が合う行があれば、そのファイルを使って 6 へ進む。
4. **裏の予約名を決める。** `get_state` を呼ぶ。
   - `fade:` が `fading to ...` なら、`fade: idle` になるまで待つ（2 秒ごと、60 秒まで）
   - `sh <C>/back_slot.sh <active>` を実行し、出た名前に書く。決め方:
     - `active: llm_a` → llm_b、`active: llm_b` → llm_a
     - それ以外（1〜7 などが映っている）→ 最後に書いていない方（`conductor_state.json` の `last_slot`）。記録が無ければ llm_a
   - ツールに `write_back_slot` があれば（kagelife-mcp 0.2 以降）、名前を決めずにそれを使ってよい。0.1.0 には無く、`get_state` に `llm_slots` も出ない
5. **絵の案を決めて書く。** 案は 1 行で決める（例: 「128 BPM の Techno、Am → 青緑の同心円が拍で少し太る」）。
   - ジャンルと BPM で動きの速さを、キー（短調・長調）で色の寒暖を決める、くらいで十分。人の指示（「暗めに」など）があればそれを優先する
   - **画面の左半分には端末が重なることがある。** 見せたい形は中央から右に置く
   - ファイルに書く: `~/.cache/kagelife/live/<history_id>.kage`（フォルダが無ければ `mkdir -p ~/.cache/kagelife/live`）
6. **検査する。** `sh <C>/check_shader.sh <file>`。`NG` なら直す。`WARN loop_heavy` はループを減らす。
7. **書く。** MCP の `write_shader` に `name`（4 で決めた予約名）と `source`（ファイルの中身）を渡す。
   - 成功: 「Compiled and ...」。画面は変わらない。続けて `sh <C>/note_written.sh <予約名>` で記録する
   - 422（コンパイルの誤り）: `line:col` を見て直し、6 からやり直す。3 回で諦める
   - 409 `slot_live`: 4 からやり直す（3 回まで）
   - 403 `forbidden_name`: 書く先を誤った。止まって伝える
   - 401・503: 1 行で伝え、次の曲を待つ
8. **知らせる。** 端末に 1 行だけ。例: 「llm_a に書いた（青緑の同心円）。Super+Shift+8 で出せます」
9. **1 に戻る。**

曲が変わってから 2 分以内に 8 まで終えるのを目安にする。間に合わなければ、その曲は諦めて次を待つ。

## シェーダの決まり

- 1 行目は `//kage:unit pixels`。続いて `package main` と `func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4`
- コメントは短い英語（ASCII だけ）。64KiB 以下
- uniform は `Time float`・`Beat float`・`Frame int`・`Random float`・`Resolution vec2`・`Cursor vec2` だけ。名前が同じで型が違うと描画時に panic する
- 光の決まり（basic-vj の README と同じ）
  - 画面全体を光らせるのは 1 拍に 1 回まで
  - 1 秒に 3 回を超えない（拍ごとの明滅は 180 BPM 以下でだけ成り立つ。曲の `bpm` が 180 を超えるなら、全画面の明滅は 2 拍に 1 回にする）
  - 毎フレーム変わる `Random` で画面全体を明滅させない
- 重さ: VJ 機は Intel HD 630。1 画素あたりのループは 16 回未満にする（04_warp は 20 回で、重ね表示ありで 36fps 前後、なしで 45fps 前後）
- `Beat` は kagelife のタップか `set_bpm` の拍で、rekordbox の BPM ではない。曲の `bpm` は動きの速さの目安にだけ使う
- 書き方の詳細は MCP の `get_kage_guide`

## 段 A（ライブの前に、セットリストの曲ごとに作っておく）

1. 人からセットリスト（曲名・アーティスト）をもらう
2. `mkdir -p ~/.cache/kagelife/setlist` で置き場所を作る
3. 曲ごとにシェーダを書き、`~/.cache/kagelife/setlist/NN_<ascii-slug>.kage` に置く。1 本ずつ `sh <C>/check_shader.sh <file>` を通す
4. `~/.cache/kagelife/setlist/index.json` に対応を書く:
   `[{"title": "曲名", "artist": "アーティスト", "file": "01_intro.kage", "idea": "1 行の案"}]`
5. できれば本番の前に、1 本ずつ裏の予約名へ書いてコンパイルを通し、人にキーで見てもらう（切り替えは人）
6. 本番では「1 周」の 3 で見つかったファイルを、検査してから書くだけにする

段 A を Mac で作ったときは、`setlist/` を VJ 機の `~/.cache/kagelife/setlist/` に写しておく。

## まだ無いもの

- **明るさの検査（未）。** 描画して平均の輝度の振れ幅を測る検査は、kagelife-shaders に `cmd/render` が入ってから（W3）足す。それまでは光の決まりを自分で読んで守る
- 「次の曲」ボタン（U2）の `cue.json` は中身が未定。wait_next は中身を見ず、更新時刻だけを見る
