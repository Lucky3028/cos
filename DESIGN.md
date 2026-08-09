# cos 設計書

## 1. 目的

`cos` は、Codex のセッションを TUI で一覧・閲覧・再開・削除する Linux 向けツールである。

画面表示と本設計書での呼称は `session`（セッション）に統一する。Codex app-server の API 名（`thread/list` など）と、それに対応する内部型 `Thread` は外部仕様との対応を保つため変更しない。

Codex の JSONL ファイルは直接編集せず、`codex app-server --stdio` の JSON-RPC API 経由で操作する。

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
- sub-agent session
- `codex exec` の履歴
- session tree の一括削除
- 削除済みデータのバックアップやごみ箱

## 3. 構成

```text
┌──────────────┐     SessionStore      ┌────────────────────┐
│ Bubble Tea UI│ ────────────────────▶ │ AppServerStore     │
└──────────────┘                       └─────────┬──────────┘
                                                 │ JSON-RPC/stdio
                                       ┌─────────▼──────────┐
                                       │ codex app-server   │
                                       └────────────────────┘
```

### ファイル構成

- `main.go`: CLI オプション、cwd の取得、TUI 起動、Codex CLI への引き継ぎ
- `types.go`: `Thread`、`Conversation`、`SessionStore` などのドメイン型
- `rpc.go`: JSON-RPC クライアント、app-server プロセス管理
- `store.go`: app-server API の呼び出しとレスポンス変換
- `ui.go`: Bubble Tea モデル、一覧・会話表示、キー操作
- `*_test.go`: JSON-RPC、ページング、会話変換、UI操作のテスト

## 4. app-server 通信

起動時に以下のプロセスを開始し、`initialize` と `initialized` 通知を送信する。

```text
codex app-server --stdio
```

使用するメソッドは次のとおり。

- `initialize`
- `thread/list`
- `thread/read`
- `thread/delete`

`thread/list` の基本パラメータは以下のとおり。

```json
{
  "archived": false,
  "sourceKinds": ["cli", "vscode", "appServer"],
  "sortKey": "updated_at",
  "sortDirection": "desc"
}
```

cwd 表示では起動時に取得した絶対パスを `cwd` に指定する。全体表示では `cwd` を省略する。ページングの `nextCursor` が返る限り、一覧を継続取得する。

app-server のバージョンによって `thread/list` の `data` は配列、または
`{ "items": [...], "nextCursor": "..." }` のオブジェクトで返る場合がある。
通信層の境界で両方の形式を受け入れ、UI 層には同じ session 一覧として渡す。

JSON-RPC の reader は、レスポンスの間に挿入される通知を無視して pending request に対応するレスポンスだけを返す。app-server の終了や stdio の切断はエラーとして UI に通知する。

## 5. ドメインモデル

`SessionStore` が UI と通信層の境界になる。

```go
type SessionStore interface {
    List(ctx context.Context, scope ListScope, cwd string) ([]Thread, error)
    Read(ctx context.Context, id string) (Conversation, error)
    Delete(ctx context.Context, id string) error
}
```

API の thread status が `{ "type": "active" }` の場合、`Thread.Active` を true にする。active session は一覧に表示するが削除・再開できない。

一覧の表示タイトルは、API の `name` を優先する。`name` が空の場合は
`preview` を代替タイトルとして使用し、それも空の場合は `(untitled)` を表示する。
左ペインの `preview` は一覧用の短い概要であり、右ペインの conversation preview
（会話プレビュー）とは別の概念として扱う。

会話変換では、ユーザー本文とアシスタント本文を表示する。reasoning と巨大な tool output は表示せず、次の項目は短い活動要約に変換する。

- `commandExecution`
- `fileChange`
- `mcpToolCall`
- `dynamicToolCall`
- `webSearch`
- sub-agent activity

## 6. TUI 操作

| キー | 操作 |
|---|---|
| `j` / `k`, ↑ / ↓ | session 選択 |
| `Tab` | 左右ペイン切り替え |
| `PageUp` / `PageDown` | 会話スクロール |
| `/` | タイトル・preview・cwd 検索 |
| `a` | cwd 表示と全体表示の切り替え |
| `p` | 右ペインの会話プレビュー表示・非表示 |
| `r` | 再読み込み |
| `Enter` | 選択中 session を再開 |
| `d` | 削除確認 |
| `y` | 削除実行 |
| `n` / `Esc` | 削除確認・検索のキャンセル |
| `q` / `Ctrl-C` | 終了 |

マウスの左クリックで session またはペインを選択できる。ホイール操作では、
左ペイン上では session 選択、右ペイン上では会話スクロールを行い、操作した
ペインへフォーカスも移す。

### レイアウトとリサイズ

- 画面はヘッダー、左右ペイン、操作ヘルプを表示するステータスバーで構成する。
- 左右ペインの枠線を含め、端末の表示領域を超えない高さに制限する。
- 端末リサイズ時は、左ペインの選択 session がスクロール範囲外に残らないよう表示位置を調整する。
- 左右ペインのフォーカスは枠線の色で示し、選択中の session 行はオレンジ文字と薄い背景で示す。
- session 間には空行を入れ、一覧の区切りを明確にする。
- 配色はオレンジを基調とし、activity は灰色、assistant は黄色系、user は緑系で表示する。
- ステータスバーには一時的な操作結果を表示せず、操作ヘルプのみを表示する。

cwd 表示と全体表示では選択位置を独立して保持する。初めて切り替える scope
では現在の選択位置を初期値として使用し、2 回目以降はその scope で最後に
選択していた位置を復元する。取得件数が異なる場合は、その一覧の末尾を超えない
範囲に補正する。

### 削除確認

`d` を押した時点で対象 session の writer lock と `thread/read` の最新状態を確認する。writer lock が取得できない、または active の場合は削除確認を表示せず、使用中で削除できないことをエラーポップアップで通知する。非 active かつ writer lock が空いている場合のみ削除確認を表示する。

`Enter` を押した時点でも同じ確認を行う。writer lock が取得できない、または active の場合は 再開せず、削除・再開ともに使用中で実行できないことをエラーポップアップで通知する。非 active の場合は確認画面を表示せず TUI を終了する。

削除確認は画面下部のステータス表示ではなく、一覧と会話プレビューを背景に
残した中央ポップアップで表示する。対象 session のタイトルは最大 3 行まで
表示し、確認文の上下には空行を入れる。`y` と `n / Esc` の選択肢はポップアップ
内の中央に揃える。

削除確認後に別の writer が session を使用し始める競合は、`thread/delete` のエラーとして検知する。削除成功後は同じ scope で一覧を再取得し、削除対象 ID が残っていないことを確認する。削除または再取得に失敗した場合は、現在の一覧を保持したまま中央エラーポップアップを表示する。

### エラー表示

app-server 通信、session の読み込み、削除などで発生したエラーは、ステータスバーではなく一覧と会話プレビューを背景に残した中央エラーポップアップで表示する。ポップアップは `Enter` または `Esc` で閉じる。

## 7. CLI

```text
cos --help
cos --version
cos
```

`CODEX_HOME` を含む環境変数は、app-server の子プロセスへそのまま継承する。

再開要求で TUI が終了した後、app-server を閉じてから、保存された cwd が空でなければ
`codex --cd <session.CWD> resume <session.ID>`、空なら `codex resume <session.ID>` を
シェルを介さずに起動する。標準入出力と環境変数は継承する。Codex の起動失敗や終了エラーは
標準エラー出力へ出し、終了ステータスを返す。

## 8. テストと検証

次のコマンドで検証する。

```text
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

テスト対象は以下のとおり。

- JSON-RPC の初期化、通知混在、エラー、app-server 終了
- `thread/list` の cwd 指定とページング
- `thread/list` の `data` 配列・オブジェクト両形式
- 会話項目の変換と活動要約
- タイトル・preview・cwd 検索
- active session の削除禁止
- idle session の Enter による 再開要求と TUI 終了
- active session と writer lock 保持 session の 再開禁止
- 保存 cwd と session ID を使った Codex CLI 起動
- 検索中、削除確認中、エラーモーダル中の Enter による 再開非実行
- scope ごとの選択位置保持
- マウス操作、端末リサイズ、会話プレビュー切り替え
- 中央削除確認ポップアップ

## 9. 設計上の前提

- Go 1.26 を基準とする。
- Linux を対象とする。
- cwd の判定は親子関係ではなく、Codex に保存された cwd との文字列完全一致とする。
- 削除は app-server の `thread/delete` に任せ、ローカルファイルを直接操作しない。
- 削除は不可逆操作のため、必ず確認画面を挟む。
