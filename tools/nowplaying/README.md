# nowplaying — rekordbox の「今かかっている曲」を書き出す

rekordbox の再生履歴（master.db）を数秒ごとに読み、新しい曲が載ったら
`~/.cache/kagelife/now_playing.json` に書き出します。Claude はこのファイルを読んで、次のシェーダを書きます。

## 早見表

| したいこと | コマンド（このフォルダで） |
|---|---|
| image を作る | `docker build -t kagelife-nowplaying .` |
| 1 回だけ読む（書き出さない） | `docker run --rm -v "$HOME/Library/Pioneer/rekordbox:/rekordbox:ro" kagelife-nowplaying --once --db-dir /rekordbox` |
| 見張り続ける | 下の「常駐モード」 |
| テスト | `docker run --rm --entrypoint python kagelife-nowplaying -m pytest -q /app/tests` |
| 片付け | `docker rmi kagelife-nowplaying` |

## 常駐モード

```sh
mkdir -p "$HOME/.cache/kagelife"
docker run --rm --name kagelife-nowplaying \
  -v "$HOME/Library/Pioneer/rekordbox:/rekordbox:ro" \
  -v "$HOME/.cache/kagelife:/out" \
  kagelife-nowplaying --db-dir /rekordbox --out /out/now_playing.json --interval 3
```

- rekordbox のフォルダは **`:ro`（読み取り専用）** でマウントします。書き込み可でマウントするのは書き出し先だけです
- 止めるときは Ctrl-C か `docker stop kagelife-nowplaying`

## 書き出す JSON

```json
{
  "title": "曲名",
  "artist": "アーティスト",
  "bpm": 128.0,
  "key": "Am",
  "genre": "Techno",
  "played_at": "2026-11-21T21:00:00",
  "detected_at": "2026-11-21T12:00:03.120000+00:00",
  "source": "rekordbox-history",
  "history_id": "履歴の行の ID",
  "content_id": "曲の ID"
}
```

- `bpm` は rekordbox が解析した BPM です（再生中のテンポではありません）。解析していなければ `null`
- `key`・`genre` は無ければ `null`
- `played_at` は履歴の行の時刻です。rekordbox は `+00:00` を付けて保存していますが、
  本当に UTC なのか現地時刻なのかは確かめていません。そのため時差を付けずに書いています
- `detected_at` はこの道具が見つけた時刻（UTC）です
- 書き方は「同じフォルダの一時ファイルに書く → rename」です。読み手が書きかけのファイルを読むことはありません

### 同じ曲を書かない決まり

- 直前に書いた行と同じ `history_id` なら書きません
- 履歴の行が新しくても、直前に書いた曲と同じ `content_id` なら書きません
  （ヘッドホンで頭出ししてから本番で流すと、同じ曲が 2 行続けて載ることがあるため）
- 起動時に既存の `now_playing.json` を読むので、道具を立ち上げ直しても同じ曲を書き直しません

## 読み方: なぜ毎回写してから読むか

rekordbox が動いている最中の master.db は WAL モードで、最近の変更は `master.db-wal` にあります。
WAL モードのファイルを直接開くと、読み取りでも SQLite は `master.db-shm` に書こうとします。そのため `:ro` のマウントでは
開けないか、rekordbox とロックを取り合うおそれがあります（直接開く方は試していません）。そこで毎回 `master.db` と `master.db-wal` を一時のフォルダへ写し、写しを開きます。

- `master.db-shm` は写しません（写しを開いた SQLite が作り直します）
- 写している間に元の大きさや更新時刻が変わったら、写し直します（3 回まで）
- 読み終えたら写しは消します。7MB ほどなので、3 秒ごとに写しても負担は小さいです
- 暗号を解くのは pyrekordbox 0.5.0 です。鍵は pyrekordbox が内蔵しており、この道具は鍵を持ちません

## rekordbox の設定

- **PERFORMANCE モード**で使います。マニュアルによると、PERFORMANCE モードで **1 分以上**再生した曲が History に載ります
- 1 分は長すぎるので、**Preferences > Advanced > Browse > Playback time**（1〜60 秒）を短くすると載るまでの時間が縮む、という第三者の報告があります（確かめていません）
- 履歴は「どこかのデッキで一定時間再生された曲」です。ヘッドホンで頭出しした曲も載る、という報告があります（確かめていません）

## 履歴が実時間で増えるかを確かめる手順

曲を再生しないと試せないので、ライブの前に一度だけ行ってください。

1. 上の「常駐モード」で見張りを始める（`-v` を付けると読み取りのたびにログが出ます）
2. 別の端末で `cat ~/.cache/kagelife/now_playing.json` を実行し、今の中身を見ておく
3. rekordbox の PERFORMANCE モードで、まだ履歴に無い曲をデッキ 1 で再生する
4. 1 分（Playback time を縮めたならその秒数）＋3 秒ほど待ち、見張りのログに「新しい曲」が出るかを見る
5. 2 と同じコマンドで、`title` と `played_at` が今の曲に変わったかを見る
6. 続けて次の 2 つも見ておくと、本番で迷いません
   - ヘッドホンだけで（マスターに出さずに）別の曲を 1 分以上流したとき、書き換わるか
   - Playback time を短くしたとき、書き換わるまでの時間が縮むか

## 規約の注意

master.db は暗号化されています。この道具は pyrekordbox を使って暗号を解き、読み取りだけをします。
暗号を解いて読むことは、rekordbox の利用規約の上では灰色です。自分の Mac の自分のライブラリを
読むだけにとどめ、この道具や読み出した中身を配らないでください。

## 確かめたこと・確かめていないこと

確かめたこと（2026-11-11、macOS 26.7.2 arm64、rekordbox 7.2.19、rekordbox 起動中の状態で `--once`）

- linux/arm64 のコンテナで `sqlcipher3-wheels` の wheel（manylinux_2_28_aarch64）がそのまま入る。ビルドは不要
- 7.2.19 の master.db を写しから開け、履歴の行数と最新の行が読めた（pyrekordbox が試験済みとするのは 7.0.9 まで）
- 常駐モードで 2 秒ごとに 4 回読み、書き出しは 1 回だけだった

確かめていないこと

- 再生してから履歴に載るまでの実際の秒数と、Playback time の効き目
- ヘッドホンでの頭出しが履歴に載るか
- `played_at` の `+00:00` が本当に UTC か
- rekordbox が WAL を書き戻す（checkpoint）瞬間に写したときの振る舞い。写し直しで避けるつもりですが、試していません
- rekordbox の版が上がったときに読めるか
