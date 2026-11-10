# CONDUCTOR（ライブ中にシェーダを裏へ用意する手順）

Claude Code がこのファイルを読み、曲が変わるたびに、映っていない予約名（llm_a / llm_b）へ次のシェーダを書く。
画面を切り替えるのは人（キー 8・9、Shift+8・Shift+9）。Claude は拍に間に合わないので、曲ごとの用意だけを受け持つ。

## 早見表

| 場面 | すること |
|---|---|
| 始める | `get_state` を 1 回呼ぶ。下の「1 周」を回す |
| 待つ | `sh tools/conductor/wait_next.sh --timeout 900` を背景で実行し、終わるのを待つ |
| 書く先 | `active` が llm_a なら **llm_b**、llm_b なら **llm_a**。それ以外は最後に書いていない方（まだ書いていなければ llm_a） |
| 書く前 | `sh tools/conductor/check_shader.sh <file>` が `OK` になるまで直す |
| 422 | 直して書き直す。**3 回**で諦め、ユーザーに 1 行で伝える |
| 409 | フェード中。2 秒ごとに `get_state` を見て `fade: idle` になってから、書く先を決め直す（60 秒で諦める） |
| 403 | 予約名以外へ書こうとした。書かずに止まり、ユーザーに伝える |
| 書けた | 「llm_b に〇〇を用意しました。キー 9（Shift+9 でフェード）で出せます」と **1 行だけ**言う |
| 段 A の曲 | `~/.cache/kagelife/setlist/index.json` に今の曲があれば、そのファイルを検査して書くだけ |

キーと予約名: キー 8 = llm_a、キー 9 = llm_b。1〜7 は basic-vj、0 は直前の予約名以外へ戻る。

## してはいけないこと

- 予約名（llm_a・llm_b）以外へ書く
- 頼まれずに `switch_shader`・`crossfade`・`set_bpm` を呼ぶ（ユーザーがその場で言ったときだけ）
- 光の決まりを破る（下の「シェーダの決まり」）
- control.json・トークン・`.env` を読む・表示する
- 1 曲のあいだに何本も書く。1 曲 1 本（直しの書き直しは数えない）

## 1 周

1. **待つ。** `sh tools/conductor/wait_next.sh --timeout 900` を背景で実行する（Bash の run_in_background）。
   - 終わったら 1 行の JSON が出る。`changed` が `start`（初回）・`song`（曲が変わった）・`cue`（「次の曲」ボタン）のどれか
   - 終了コード 3 は「15 分なにも起きなかった」。そのまま同じコマンドで待ち直す
   - Monitor で見張るなら `--follow` を付ける（変化のたびに 1 行出し、終わらない）
2. **曲を読む。** 出力の `now_playing` の `title`・`artist`・`bpm`・`key`・`genre` を見る。
   `now_playing` が `null` のとき（合図だけ来た）は、前の曲のまま次の絵を用意する。
3. **段 A の曲か確かめる。** `~/.cache/kagelife/setlist/index.json` があり、`title`（と `artist`）が合う行があれば、そのファイルを使って 6 へ進む。
4. **裏の予約名を決める。** `get_state` を呼ぶ。
   - `fade:` が `fading to ...` なら、`fade: idle` になるまで待つ（2 秒ごと、60 秒まで）
   - `active: llm_a` → llm_b、`active: llm_b` → llm_a
   - それ以外（1〜7 などが映っている）→ この会話で最後に書いていない方。まだ書いていなければ llm_a
   - ツールに `write_back_slot` があれば（kagelife-mcp 0.2 以降）、名前を決めずにそれを使ってよい。0.1.0 には無く、`get_state` に `llm_slots` も出ない
5. **絵の案を決めて書く。** 案は 1 行で決める（例: 「128 BPM の Techno、Am → 青緑の同心円が拍で少し太る」）。
   - ジャンルと BPM で動きの速さを、キー（短調・長調）で色の寒暖を決める、くらいで十分
   - **PC01 は画面の左半分に端末が重なっている。** 見せたい形は中央から右に置く
   - ファイルに書く: `~/.cache/kagelife/live/<history_id>.kage`
6. **検査する。** `sh tools/conductor/check_shader.sh <file>`。`NG` なら直す。`WARN loop_heavy` はループを減らす。
7. **書く。** MCP の `write_shader` に `name`（4 で決めた予約名）と `source`（ファイルの中身）を渡す。
   - 成功: 「Compiled and ...」。画面は変わらない
   - 422（コンパイルの誤り）: `line:col` を見て直し、6 からやり直す。3 回で諦める
   - 409 `slot_live`: 4 からやり直す（3 回まで）
   - 403 `forbidden_name`: 書く先を誤った。止まってユーザーに伝える
   - 401・503: ユーザーに 1 行で伝え、次の曲を待つ
8. **知らせる。** 1 行だけ。例: 「llm_a に『青緑の同心円』を用意しました。キー 8（Shift+8 でフェード）で出せます」
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
- 重さ: VJ 機は Intel HD 630。1 画素あたりのループは 16 回未満にする（04_warp は 20 回で 37〜45fps）
- `Beat` は kagelife のタップか `set_bpm` の拍で、rekordbox の BPM ではない。曲の `bpm` は動きの速さの目安にだけ使う
- 書き方の詳細は MCP の `get_kage_guide`

## 段 A（ライブの前に、セットリストの曲ごとに作っておく）

1. ユーザーからセットリスト（曲名・アーティスト）をもらう
2. 曲ごとにシェーダを書き、`~/.cache/kagelife/setlist/NN_<ascii-slug>.kage` に置く。1 本ずつ `check_shader.sh` を通す
3. `~/.cache/kagelife/setlist/index.json` に対応を書く:
   `[{"title": "曲名", "artist": "アーティスト", "file": "01_intro.kage", "idea": "1 行の案"}]`
4. できれば本番の前に、1 本ずつ裏の予約名へ書いてコンパイルを通し、ユーザーにキーで見てもらう（切り替えはユーザー）
5. 本番では「1 周」の 3 で見つかったファイルを、検査してから書くだけにする

## まだ無いもの

- **明るさの検査（未）。** 描画して平均の輝度の振れ幅を測る検査は、kagelife-shaders に `cmd/render` が入ってから（W3）足す。それまでは光の決まりを自分で読んで守る
- 「次の曲」ボタン（U2）の `cue.json` は中身が未定。wait_next は中身を見ず、更新時刻だけを見る
