# kagelife-mcp

MCP server that lets LLMs live-code Kage shaders on [kagelife](https://github.com/cyokozai/kagelife) for VJ performances.

`kagelife-mcp` は、Kage シェーダの VJ ツール kagelife を、LLM（Claude Code / Claude Desktop など）から操作するための stdio MCP サーバです。
MCP クライアントから stdio で起動され、ツール呼び出しを、常駐している kagelife GUI の制御口（127.0.0.1 の HTTP/JSON）へ中継します。
純 Go で書かれていて、cgo や GPU ライブラリは要りません。

## 前提

- 先に kagelife（GUI）を起動しておくこと。GUI が起動していなければ、各ツールはエラーで起動を促します（`kagelife-mcp` 自体は落ちません）。
- kagelife 側は **制御口 v1** に対応した版が必要です（発見ファイルの `version` が `1`）。版が合わなければ、ツールは版の不一致を伝えるエラーを返します。
- 制御口 v1 の契約の正典は kagelife の `docs/adr/ADR-006-control-api.md` です。

## インストール

```sh
go install github.com/cyokozai/kagelife-mcp@latest
```

Go 1.27.1 以降。`$GOPATH/bin`（既定では `~/go/bin`）に PATH を通してください。

## Claude Code への登録

```sh
claude mcp add --transport stdio --scope project kagelife -- kagelife-mcp
```

または、プロジェクト直下の `.mcp.json` に次のように書きます。

```json
{
  "mcpServers": {
    "kagelife": {
      "type": "stdio",
      "command": "kagelife-mcp",
      "args": []
    }
  }
}
```

`kagelife-mcp` は引数を取りません。stdout は JSON-RPC 専用で、ログは stderr に出ます。stdin が閉じられるか、SIGINT / SIGTERM を受けると終了します。

## ツール

| ツール | 内容 |
|---|---|
| `get_state()` | アクティブなシェーダ、シェーダ一覧、BPM（既定値か、タップ・`set_bpm` で設定された値か）、クロスフェードの進み、FPS、解像度、最新のコンパイルエラー |
| `list_shaders()` | シェーダ名の一覧（アクティブなものに印）。コンパイルに失敗したファイルも含む（そのシェーダへは直すまで切り替えられない） |
| `read_shader(name)` | シェーダのソースを返す |
| `write_shader(name, source)` | シェーダを新規作成、または全文置換する。コンパイルに失敗したら何も保存せず、画面は前のシェーダのまま。エラーは `行:列: メッセージ` で返す。書き込んでも画面は切り替わらない（表示中のシェーダが無いときだけ、書き込んだシェーダが表示される） |
| `switch_shader(name)` | 即時に切り替える |
| `crossfade(name, beats?)` | 拍数を指定してクロスフェードする（`beats` は省略可、0 < beats <= 64）。BPM は常に 40〜300（タップ前は既定の 120）なので、BPM を理由には失敗しない |
| `set_bpm(bpm)` | BPM を設定する（40〜300。範囲外は `invalid_bpm`） |
| `get_kage_guide()` | Kage の書き方の手引き（制約、`//kage:unit pixels`、`Fragment` の形、予約 uniform、テンプレート、VJ 向けの助言）。GUI が起動していなくても使える |

## 発見ファイル

kagelife GUI は起動時に、接続先とトークンを JSON で書き出します（パーミッション 0600）。

```json
{"addr":"127.0.0.1:NNNNN","token":"<hex 64 文字>","pid":123,"shader_dir":"/abs/path/shaders","version":1}
```

場所は環境変数 `KAGELIFE_CONTROL_FILE`、無ければ `os.UserCacheDir()/kagelife/control.json` です
（macOS なら `~/Library/Caches/kagelife/control.json`、Linux なら `~/.cache/kagelife/control.json`）。

`kagelife-mcp` はツール呼び出しのたびにこのファイルを読み直します。GUI を再起動してポートやトークンが変わっても、`kagelife-mcp` を再起動する必要はありません。
安全のため、`addr` がループバック以外なら接続を拒否します。制御口への各リクエストのタイムアウトは 5 秒です。

## 開発

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.27.1 go test -race ./...
```

## ライセンス

未定です。
