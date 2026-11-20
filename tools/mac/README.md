# mac（Mac 1 台で、Spotify の曲に合わせて自動で VJ する）

Mac 1 台で、Spotify で今かかっている曲に合わせて、Claude Code が曲ごとに kagelife のシェーダを書く構成の手順です。
kagelife・kagelife-mcp・Claude Code は Mac で直接動かし、nowplaying-spotify だけを Docker のコンテナで動かします。
`start.sh` と `stop.sh` は kagelife と nowplaying-spotify のコンテナをまとめて起動・停止します。どちらも何もインストールしません。

合わせるのは曲の変わり目までです。Spotify の API は BPM を返さず、kagelife の音声入力もまだ無いので、拍には合わせません。

```
Mac: Spotify -> nowplaying-spotify (コンテナ) -> ~/.cache/kagelife/now_playing.json
       | tools/conductor/wait_next.sh
     Claude Code -> kagelife-mcp (stdio) -> kagelife の llm_a / llm_b
```

## 早見表

下のコマンドはこのリポジトリの clone の最上位で打ちます。

| したいこと | すること |
|---|---|
| 準備（最初の 1 回） | 下の「準備」の 1〜5 |
| 起動する | `sh tools/mac/start.sh --shaders ~/vj/shaders` |
| 外部ディスプレイに全画面で出す | `sh tools/mac/start.sh --shaders ~/vj/shaders -- -display 2 -fullscreen` |
| Spotify なしで kagelife だけ起動する | `sh tools/mac/start.sh --shaders ~/vj/shaders --no-nowplaying` |
| 指揮を始める | Claude Code に「`tools/conductor/CONDUCTOR.md` を読んで指揮を始めて」と頼む |
| 今の曲を見る | `cat ~/.cache/kagelife/now_playing.json` |
| コンテナのログを見る | `docker logs -f kagelife-nowplaying-spotify` |
| kagelife のログを見る | `tail -f ~/.cache/kagelife/kagelife.log` |
| 止める | `sh tools/mac/stop.sh` |
| テスト | 下の「テスト」 |

## 準備

### 1. 用意するもの

- Docker（OrbStack か Docker Desktop）
- kagelife の v0.2.0 以降。リリースの `kagelife-v0.2.0-darwin-arm64.tar.gz` を使うか、kagelife の `dev` を `CGO_ENABLED=1 go build` で作る。
  入手と PATH の通し方は kagelife の README の「インストール」に従う
- kagelife-mcp を作るための Go（このリポジトリの `go.mod` の版）
- Claude Code
- Spotify のアプリ登録と Client ID。手順は [`tools/nowplaying-spotify/README.md`](../nowplaying-spotify/README.md) の「Spotify のアプリ登録」

### 2. シェーダの置き場所を作る

kagelife は `-shaders` のフォルダから `.kage` を読み、Claude Code が書いた `llm_a.kage`・`llm_b.kage` も同じフォルダに置きます。
そのため、リリースや clone の `shaders/` を直接使わず、VJ 用のフォルダに写して使います。

```sh
mkdir -p ~/vj/shaders
cp ~/Applications/kagelife/current/shaders/*.kage ~/vj/shaders/
```

- kagelife の `shaders/` には 9 本のサンプルがあります。`-llm-slots` 付きでは、予約名を除いたシェーダのうちファイル名順で前から 7 本がキー 1〜7 に付きます
- ライブ向けの basic-vj（9 本）は、[kagelife-shaders](https://github.com/cyokozai/kagelife-shaders) の `feat/basic-vj` ブランチの `presets/basic-vj/` にあります（2026-11-20 時点で main には入っていません）。使うなら、そのフォルダの `.kage` を `~/vj/shaders/` に写します

### 3. kagelife-mcp を作って Claude Code に登録する

```sh
go build -o ~/.local/bin/kagelife-mcp .
```

Claude Code への登録は、利用者が自分で行います。Claude Code を起動するフォルダ（この clone など）で次を打ちます。

```sh
claude mcp add --transport stdio kagelife -- "$HOME/.local/bin/kagelife-mcp"
```

- kagelife-mcp は kagelife の制御口の発見ファイル（macOS では `~/Library/Caches/kagelife/control.json`）を読んで、同じ Mac の kagelife に繋ぎます
- `KAGELIFE_CONTROL_FILE` で発見ファイルの場所を変えたときは、kagelife と kagelife-mcp の両方に同じ値を渡します

### 4. nowplaying-spotify の image を作り、ログインする

```sh
docker build -t kagelife-nowplaying-spotify tools/nowplaying-spotify
export KAGELIFE_SPOTIFY_CLIENT_ID='控えた Client ID'
```

続けて [`tools/nowplaying-spotify/README.md`](../nowplaying-spotify/README.md) の「ログイン」を 1 回行います。トークンは `~/.config/kagelife/` に保存されます。

### 5. 表示する画面を確かめる

外部ディスプレイに出すときは、先に番号を確かめます。

```sh
kagelife -list-displays
```

## 起動する

```sh
export KAGELIFE_SPOTIFY_CLIENT_ID='控えた Client ID'
sh tools/mac/start.sh --shaders ~/vj/shaders
```

1. kagelife を `-llm-slots llm_a,llm_b` 付きで、窓表示のまま背景で起動します。pid は `~/.cache/kagelife/kagelife.pid`、ログは `~/.cache/kagelife/kagelife.log` に書きます
2. nowplaying-spotify のコンテナを `kagelife-nowplaying-spotify` という名前で起動します。`~/.cache/kagelife/now_playing.json` に今の曲を書きます
3. 3 秒後にコンテナが動いているかを見て、止まっていればそのログを出して終了コード 1 で終わります（kagelife は動いたままなので、`stop.sh` で止めます）

`--` のあとに書いたものはそのまま kagelife に渡します（例: `-- -display 2 -fullscreen`）。

| オプション | 環境変数 | 既定 | 中身 |
|---|---|---|---|
| `--shaders DIR` | `KAGELIFE_SHADERS` | なし（必須） | kagelife の `-shaders` に渡すフォルダ |
| `--kagelife PATH` | `KAGELIFE_BIN` | PATH の `kagelife` | kagelife の実行ファイル |
| `--cache DIR` | `KAGELIFE_CACHE` | `~/.cache/kagelife` | `now_playing.json`・pid・ログの置き場所 |
| `--config DIR` | `KAGELIFE_CONFIG` | `~/.config/kagelife` | ログインで作ったトークンのフォルダ。コンテナに `/config` としてマウントする |
| `--no-nowplaying` | | | kagelife だけを起動する。Spotify も Docker も使わない |
| | `KAGELIFE_SPOTIFY_CLIENT_ID` | なし | Client ID。`--no-nowplaying` でなければ必須。無ければ何も起動せずに止まる |
| | `KAGELIFE_NOWPLAYING_IMAGE` | `kagelife-nowplaying-spotify` | コンテナの image |
| | `KAGELIFE_NOWPLAYING_NAME` | `kagelife-nowplaying-spotify` | コンテナの名前 |
| | `KAGELIFE_NOWPLAYING_INTERVAL` | `3` | Spotify を読む間隔（秒） |

- Client ID は `-e KAGELIFE_SPOTIFY_CLIENT_ID` で名前だけをコンテナに渡します。値はコマンドの行にも画面にも出しません
- kagelife がすでに動いている（pid のファイルの pid が生きている）とき、同じ名前のコンテナがあるとき、image が無いときは、何も起動せずに止まります
- 終了コード: 0 = 起動した、1 = 起動しなかった・途中で止まった（理由は標準エラー）、2 = 使い方の誤り
- `--cache` を既定から変えたときは、`tools/conductor/wait_next.sh` にも同じ場所を `KAGELIFE_CACHE` で渡します

## 指揮を始める

Claude Code を起動し、次のように頼みます。

```
tools/conductor/CONDUCTOR.md を読んで指揮を始めて
```

- 曲が変わるたびに、Claude Code が映っていない予約名（llm_a か llm_b）にシェーダを書き、端末に 1 行で知らせます。画面は切り替えません
- 画面を切り替えるのは人です。kagelife の窓を前に出し、8・9 で予約名に、1〜7 でサンプルに切り替えます。Shift を足すとクロスフェードです
- Spotify から来る曲は `bpm`・`key`・`genre` が `null` なので、Claude Code は曲名とアーティストから絵の案を決めます
- 細かい手順と決まりは [`tools/conductor/CONDUCTOR.md`](../conductor/CONDUCTOR.md) にあります。2 台の構成で使う `push_now_playing.sh` は Mac 1 台では使いません

Stream Deck（kagelife-deck）は、この手順とは別に用意します。Mac で deck を繋ぐときは、kagelife の制御口の発見ファイルが macOS では `~/Library/Caches/kagelife/control.json` にあります。

## 止める・片付ける

```sh
sh tools/mac/stop.sh
```

- `kagelife.pid` の kagelife に TERM を送り、5 秒以内に止まらなければ、そう伝えて終了コード 1 で終わります
- コンテナ `kagelife-nowplaying-spotify` を止めて消します。image は残します
- 何も動いていなければ、何もせずに終了コード 0 で終わります
- `--cache DIR` と `--no-nowplaying` は `start.sh` と同じ意味です

使い終えて image も要らなくなったら、`docker rmi kagelife-nowplaying-spotify` で消します。

## 規約の注意

Spotify の Developer Policy は、次の 2 つを禁じています。

- **III.6**: Spotify の音源と映像などを同期させること
- **III.10**: 店舗など、事業の場で流すこと

この構成は曲名を kagelife の VJ に渡し、曲の変わり目ごとに映像を用意させます。用途は展示のデモを想定しています。
人前でどう見せるかは、規約を読んだうえで利用者が判断します。

## テスト

`start.sh` と `stop.sh` のテストは kagelife と docker を偽物に差し替え、引数と環境変数の扱いを確かめます。

```sh
docker build -t kagelife-mac-test tools/mac
docker run --rm kagelife-mac-test
docker rmi kagelife-mac-test
```

## 確かめたこと・確かめていないこと

確かめたこと（2026-11-20）

- `python:3.12-slim` のコンテナ（`sh` は dash）で、`start.sh`・`stop.sh` のテスト 17 件が通った
- この Mac で、kagelife の `dev` を `go build` したものを `start.sh --no-nowplaying` で窓表示で起動し、kagelife-mcp の stdio で `get_state` と `write_back_slot` を流した。
  `write_back_slot` は llm_a に書けて、書く前も後も `active: 01_uv` のままだった。`stop.sh` で kagelife が止まった
- 偽の `now_playing.json`（`history_id` が違う 2 つ）で、`tools/conductor/wait_next.sh --dir` が `start` のあと `song` を出した
- 本物の Docker で、偽の Client ID と空の config のフォルダで `start.sh` を流すと、コンテナが「`login` first」で止まったことを `start.sh` がログ付きで伝えて終了コード 1 で終わり、`stop.sh` が kagelife を止めてコンテナを消した

確かめていないこと

- 本物の Spotify に繋いだ通しの動き（ログイン、曲が変わってから Claude Code が書き終えるまでの時間）
- Claude Code に MCP を登録し、CONDUCTOR.md を読ませて Mac 1 台で 1 周させること
- `-display` と `-fullscreen` を付けた起動と、外部ディスプレイでの見え方
- kagelife のリリース版（v0.2.0）を `start.sh` で起動すること（`dev` から作ったものだけを使った）
- Docker Desktop と OrbStack の違いで動きが変わらないか（この Mac の既定の Docker だけを使った）
- 規約の条番号と文言を、Spotify の原文と照らし合わせること
