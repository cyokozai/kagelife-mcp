# nowplaying-spotify（Spotify で今かかっている曲を書き出す）

Spotify Web API で再生中の曲を数秒ごとに読み、曲が変わったら
`~/.cache/kagelife/now_playing.json` に書き出します。キーは rekordbox 版（`tools/nowplaying/`）と同じなので、
`tools/conductor/` の `wait_next.py` はそのまま読めます。Python 3.12 の標準ライブラリだけで動きます。

## 早見表

先に「Spotify のアプリ登録」を済ませ、控えた Client ID を環境変数に入れておきます。引用符の中を実際の値に置き換えてください
（`<Client ID>` のように山かっこで書いたまま打つと、zsh が parse error を出します）。

```sh
export KAGELIFE_SPOTIFY_CLIENT_ID='控えた Client ID'
```

| したいこと | コマンド（このフォルダで） |
|---|---|
| image を作る | `docker build -t kagelife-nowplaying-spotify .` |
| ログインする（最初の 1 回） | 下の「ログイン」 |
| 1 回だけ読む（書き出さない） | `docker run --rm -e KAGELIFE_SPOTIFY_CLIENT_ID -v "$HOME/.config/kagelife:/config" kagelife-nowplaying-spotify --once --token /config/spotify-token.json` |
| 見張り続ける | 下の「常駐モード」 |
| テスト | `docker run --rm -v "$PWD/../conductor:/conductor:ro" --entrypoint python kagelife-nowplaying-spotify -m unittest discover -s /app/tests -v` |
| 片付け | `docker rmi kagelife-nowplaying-spotify` |

- `-e KAGELIFE_SPOTIFY_CLIENT_ID` は、手元の環境変数の値をそのままコンテナへ渡す書き方です。`--client-id '控えた Client ID'` を付けても同じです
- テストでは `tools/conductor` を `/conductor` にマウントします。マウントしないと、`wait_next.py` で読めるかを確かめるテストが飛ばされます（`skipped` と出ます）

## Go 版（Docker なしで Mac で動かす）

kagelife-mcp の実行ファイルには、この道具を Go で書き直したものが、サブコマンド `nowplaying-spotify` として入っています。
フラグの名前と既定値、終了コード、トークンのファイルの形、`now_playing.json` のキーと値の作り方は Python 版と同じです。
Python 版で login したトークンのファイルは、そのまま読めます。Python 版と Docker の手順は、予備としてこのまま残します。

### ビルド

リポジトリのいちばん上のフォルダで次を実行すると、Apple Silicon の Mac 向けの `kagelife-mcp` ができます。ビルドもコンテナの中で行います。

```sh
docker run --rm -v "$PWD":/src -w /src -e CGO_ENABLED=0 -e GOOS=darwin -e GOARCH=arm64 golang:1.27.3 go build -o kagelife-mcp .
```

### 使い方

Client ID は Python 版と同じく環境変数 `KAGELIFE_SPOTIFY_CLIENT_ID` から読みます。

| したいこと | コマンド |
|---|---|
| ログインする（最初の 1 回） | `./kagelife-mcp nowplaying-spotify login --token "$HOME/.config/kagelife/spotify-token.json"` |
| 1 回だけ読む（書き出さない） | `./kagelife-mcp nowplaying-spotify --once` |
| 見張り続ける | `./kagelife-mcp nowplaying-spotify --interval 3` |
| テスト | `docker run --rm -v "$PWD":/src -w /src golang:1.27.3 go test -race ./internal/nowplaying/...` |

- `--token` と `--out` の既定は Python 版と同じ `~/.config/kagelife/spotify-token.json` と `~/.cache/kagelife/now_playing.json` です
- login の待ち受けの既定は Python 版と同じ `0.0.0.0:8888` です。コンテナを通さないので、同じネットワークのほかのマシンからも届きます。届かないようにするには `--listen-host 127.0.0.1` を付けます
- 見張りは前面で動き続けます。Ctrl-C（SIGINT）か SIGTERM で止まり、終了コードは 0 です
- 読み取りの誤りの扱い、書き出す JSON、history_id の決まりは、下の各節に書いたとおりです

### Python 版と違うところ

- `--tok` のようにフラグの名前を略しては書けません（Python 版の argparse は受け付けます）
- login を待っている間に Ctrl-C を押すと、traceback を出さずに終了コード 1 で終わります
- 見張りの途中で想定外の形の応答（album が文字列など）が来ても止まらず、間をあけて読み直します
- トラック ID が文字列でない応答は曲として扱いません

## Spotify のアプリ登録

1. https://developer.spotify.com/dashboard に自分の Spotify アカウントでログインし、**Create app** を押す
2. 名前と説明は自由に書く
3. **Redirect URIs** に `http://127.0.0.1:8888/callback` を入れて **Add** を押す。
   `localhost` は使えません（Spotify が受け付けません）。必ず `127.0.0.1` で書きます
4. 「Which API/SDKs are you planning to use?」で **Web API** にチェックを入れる
5. 規約に同意して保存する
6. アプリの **Settings** を開き、**Client ID** だけを控える。Client Secret は使いません（PKCE で認可するため）。
   Client ID はコードやリポジトリに書かず、環境変数か `--client-id` で渡します

アプリは開発モードのままで使えます。開発モードのアプリを使えるのは、Dashboard の **User Management** に載ったアカウントだけです。
読み取りで 403 が返るときは、ここに自分のアカウントが載っているかを見てください。

## ログイン

```sh
mkdir -p "$HOME/.config/kagelife" && chmod 700 "$HOME/.config/kagelife"
docker run --rm -it -p 127.0.0.1:8888:8888 \
  -e KAGELIFE_SPOTIFY_CLIENT_ID \
  -v "$HOME/.config/kagelife:/config" \
  kagelife-nowplaying-spotify login --token /config/spotify-token.json
```

1. 表示された URL をブラウザで開き、許可する
2. ブラウザが `http://127.0.0.1:8888/callback` に戻り、「Login OK」と出れば終わりです。トークンは
   `~/.config/kagelife/spotify-token.json` に保存されます
3. 5 分以内に戻らなければ失敗として終わります（`--login-timeout` で変えられます）

- 認可の方式は Authorization Code + PKCE です。求める権限は `user-read-currently-playing` と `user-read-playback-state` の 2 つだけです
- 戻ってきた `state` が送ったものと違えば、その戻りは捨てて待ち続けます
- コンテナの中では `0.0.0.0:8888` で待ちますが、`-p 127.0.0.1:8888:8888` なので外のマシンからは届きません
- トークンのファイルは同じフォルダの一時ファイルに書いてから rename で置き換えます。権限は 0600 です
- アクセストークンが切れる 60 秒前と、API が 401 を返したときに、refresh_token で更新します。
  Spotify が新しい refresh_token を返したら、それも保存します
- トークン・認可コード・PKCE の verifier はログに出しません
- 更新が拒まれた（refresh_token が無効になった）ときは、見張りを止めて「`login` をやり直す」と出します

## 常駐モード

```sh
mkdir -p "$HOME/.cache/kagelife"
docker run --rm --name kagelife-nowplaying-spotify \
  -e KAGELIFE_SPOTIFY_CLIENT_ID \
  -v "$HOME/.config/kagelife:/config" \
  -v "$HOME/.cache/kagelife:/out" \
  kagelife-nowplaying-spotify --token /config/spotify-token.json --out /out/now_playing.json --interval 3
```

- 3 秒ごと（`--interval`）に `/v1/me/player/currently-playing` と `/v1/me/player/queue` を読みます
- トークンを更新したら書き戻すので、`/config` は書き込み可でマウントします
- 止めるときは Ctrl-C か `docker stop kagelife-nowplaying-spotify`
- `-v` を付けると、読み飛ばしたエピソードや広告もログに出ます

`--once` は 1 回だけ読み、書き出すはずの JSON を表示して終わります。`now_playing.json` には書きません
（アクセストークンが切れていれば更新し、トークンのファイルは書き換えます）。

### 読み取りの誤りの扱い

| 返ってきたもの | 扱い |
|---|---|
| 204（何も再生していない） | 直前の曲を `is_playing: false` にして書く。曲を一度も読んでいなければ何も書かない |
| 401 | トークンを 1 回だけ更新して読み直す。もう一度 401 なら、下の 5xx と同じく間をあけて続ける |
| 429 | `Retry-After` の秒数だけ待つ（`--interval` より短ければ `--interval`） |
| 5xx・ネットワークの誤り | 待ち時間を倍にしながら（最長 60 秒）続ける。読めたら `--interval` に戻す |
| ポッドキャストのエピソード・広告・ローカルファイル | 読み飛ばし、直前の曲をそのまま残す |

キューだけ読めなかったとき（5xx など）は、直前の `next` を残して曲の情報は書きます。キューが 429 のときは、その回の読み取り全体を
`Retry-After` のあとにやり直します。

## 書き出す JSON

```json
{
  "title": "曲名",
  "artist": "アーティスト A, アーティスト B",
  "bpm": null,
  "key": null,
  "genre": null,
  "played_at": "2026-11-21T11:59:30.000+00:00",
  "detected_at": "2026-11-21T12:00:00.000+00:00",
  "source": "spotify",
  "history_id": "sp-<トラック ID>-<再生を始めた時刻のミリ秒>",
  "content_id": "トラック ID",
  "album": "アルバム名",
  "duration_ms": 200000,
  "progress_ms": 30000,
  "is_playing": true,
  "fetched_at": "2026-11-21T12:00:00.000+00:00",
  "next": [
    {"title": "次の曲", "artist": "アーティスト", "duration_ms": 180000, "track_id": "トラック ID"}
  ]
}
```

- `title` から `content_id` までは rekordbox 版と同じキーです。`bpm`・`key`・`genre` は Spotify から取らないので、いつも `null` です
- `artist` は、アーティストが複数なら `", "` でつなぎます
- `played_at` は `fetched_at − progress_ms`、つまり今の位置から逆算した再生の始まりです。時刻はすべて UTC です
- `detected_at` は、この道具がその再生を初めて見つけた時刻です
- `fetched_at` は、その中身を Spotify から読んだ時刻です。204 で `is_playing: false` にしたときは、`progress_ms` と `fetched_at` を
  最後に曲を読めたときのまま残します（`played_at` の式が崩れないように）
- `next` はキューの先頭から曲だけを 3 つまで並べます（エピソードは飛ばします）
- 書き方は「同じフォルダの一時ファイルに書く → rename」です。読み手が書きかけのファイルを読むことはありません

### history_id の決まり

`wait_next.py` は `history_id` が変わったことを曲の変わり目として扱います。そのため次のように決めています。

- 別の曲に変わったら、新しい `history_id` にする
- 同じ曲のリピート（終わり近くから頭に戻った）でも、新しい `history_id` にする。
  「前に読んだ位置に、そこから経った時間を足すと、曲の終わりの 15 秒前を過ぎている」かつ
  「今の位置が、その終わりから経った時間 + 15 秒より手前」のとき、リピートとみなします
- 曲の途中でシークしただけなら変えない（前にも後ろにも）。一時停止していた時間は経った時間に数えません
- `history_id` は英数字と `-` だけでできています（`CONDUCTOR.md` がファイル名に使うため）

終わり近くで手で頭に戻したときは、リピートと見分けられないので、新しい `history_id` になります。

### 書き出す時と書かない時

- 書くのは、曲（`history_id`）・`is_playing`・`next` のどれかが変わったときだけです
- 再生が進んだだけ、シークしただけでは書きません
- 起動時に既存の `now_playing.json` を読み、同じ再生が続いていれば書き直しません。
  `source` が `spotify` でないファイル（rekordbox 版が書いたもの）は引き継がず、最初の読み取りで置き換えます

## 規約の注意

Spotify の Developer Policy は、次の 2 つを禁じています。

- **III.6**: Spotify の音源と映像などを同期させること
- **III.10**: 公の場や店舗など、商用の場で流すこと

この道具は曲名などを kagelife の VJ に渡して映像を選ばせます。使うのは私的な利用にとどめてください。
人前での上映やイベントで使うかどうかは、規約を読んだうえで利用者が判断し、その責任は利用者が負います。

## 確かめたこと・確かめていないこと

確かめたこと（2026-11-19、`python:3.12-slim` のコンテナ、上の早見表のテストのコマンド）

- 単体テスト 53 件が通る。HTTP はすべて偽物で、本物の Spotify には繋いでいない
- PKCE の code_challenge が RFC 7636 の例と一致する。認可の URL に verifier が入らない
- login で、違う state（ASCII 以外を含む）を捨て、正しい state のコードでトークンを取り、0600 で保存する。
  コード・verifier・トークンがログと標準エラーに出ない
- トークンの更新で、新しい refresh_token を保存し、返ってこなければ古いものを残す
- 204・401（1 回だけ更新）・429（`Retry-After`）・5xx とネットワークの誤り（倍にしながら待つ）の扱い
- 上の history_id の決まり（曲の変化・リピート・後から見つけたリピート・シーク・一時停止）
- 変わったときだけ書くこと、再起動で同じ曲を書き直さないこと、キューの 429 や書き出しの失敗のあとでも曲の変わり目を取り違えないこと
- `tools/conductor/wait_next.py` が、最初の曲で `start`、一時停止では何も出さず、次の曲で `song` を出す

確かめていないこと

- 本物の Spotify での login と読み取り（ユーザーのログインが要るため）。上の「ログイン」と `--once` で確かめてください
- Docker Desktop for Mac で、ブラウザから `127.0.0.1:8888` の戻りがコンテナに届くこと
- 開発モードのアプリで、3 秒ごとに 2 つの API を読み続けて 429 にならないか（Spotify は上限の数を公開していません）
- ローカルファイルの再生（トラック ID が無い）を読み飛ばす振る舞いを、実際の応答で見たこと
- プライベートセッション中や、別の端末で再生しているときの応答
- 規約の条番号と文言は、この README を書いた時点で Spotify の原文と照らし合わせていない
