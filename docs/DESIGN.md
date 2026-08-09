# cos 基本設計

## 1. 目的

`cos` は、Codex の session（セッション）を TUI で一覧・閲覧・再開・削除する Linux 向けツールである。

Codex の JSONL ファイルは直接編集せず、`codex app-server --stdio` の JSON-RPC API 経由で操作する。画面表示と本設計書での呼称は `session` に統一し、Codex app-server の API 名（`thread/list` など）と内部型 `Thread` は外部仕様との対応を保つため変更しない。

## 2. スコープ

### 対象

- 起動時の cwd と完全一致する session の一覧表示
- 全 session の一覧表示への切り替え
- タイトル・preview・cwd による検索
- session の会話内容の閲覧
- 選択した session の `codex resume` への引き継ぎ
- コマンド実行、ファイル変更、MCP 操作などの活動要約
- 確認付きの session 完全削除

### 対象外

- アーカイブ済み session
- sub-agent session（一覧・閲覧・操作の対象外。ただし親 session の子孫確認では検出する）
- `codex exec` の履歴
- session tree の一括削除
- 削除済みデータのバックアップやごみ箱

## 3. 全体構成

```text
┌──────────────┐     SessionStore      ┌────────────────────┐
│ Bubble Tea UI│ ────────────────────▶ │ AppServerStore     │
└──────────────┘                       └─────────┬──────────┘
                                                 │ JSON-RPC/stdio
                                       ┌─────────▼──────────┐
                                       │ codex app-server   │
                                       └────────────────────┘
```

`internal/domain` が UI と通信層で共有するドメイン型と `SessionStore` を定義する。`internal/appserver` はその実装として app-server との通信・変換・接続管理を担い、`internal/tui` は一覧・会話表示とユーザー操作を担う。CLI は起動時の cwd を取得し、TUI 終了後に必要であれば Codex CLI へ session の再開を引き継ぐ。

## 4. 主要な操作

- 起動時に app-server へ接続し、起動時の cwd に一致する session を一覧表示する。
- ユーザーは全体表示への切り替え、検索、ページ移動、会話の閲覧を行える。
- session の再開時は app-server を終了してから、保存された session ID と cwd を使って `codex resume` を起動する。
- session の削除時は、writer lock、最新状態、全階層の子孫を確認してから確認画面を表示する。確認できない場合や安全性を判断できない場合は処理を中止する。

## 5. 設計方針

- session の読み書きは app-server API に限定し、ローカルの JSONL ファイルを直接操作しない。
- active、状態不明、writer lock 保持中、子孫ありなど、安全性を確認できない session は削除・再開しない。
- app-server の応答形式の差異は通信層で吸収し、UI 層には共通のドメイン型を渡す。
- 確認後から削除・再開までの別クライアントとの競合は、app-server API の制約上 best effort とする。
- app-server との通信エラーや操作エラーは、一覧と会話プレビューを残した中央ポップアップで利用者に通知する。

## 6. パッケージの責務

- `cmd/cos`: CLI オプション、cwd の取得、TUI の起動、Codex CLI への引き継ぎ
- `internal/domain`: session、conversation、Store などの共有モデル
- `internal/appserver`: app-server プロセス、JSON-RPC、Store 実装
- `internal/tui`: Bubble Tea による表示、入力、非同期操作
- `internal/lock`: writer lock の状態確認

## 7. 詳細設計

具体的な API 仕様、データモデル、TUI の操作・レイアウト、削除時の状態確認、CLI の引き継ぎ、テスト項目は [DETAIL_DESIGN.md](DETAIL_DESIGN.md) に定義する。

## 8. 検証

実装後は次の検証を行う。

```text
go test ./...
go test -race ./...
go vet ./...
go build ./...
```
