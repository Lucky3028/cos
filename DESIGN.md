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

`thread/delete` は対象 session の子孫も削除するため、cos は削除確認時と削除実行時に
writer lock、最新状態、全階層の子孫を確認する。確認できない場合や子孫が見つかった
場合は fail-closed とする。ただし app-server の削除APIには「子孫がない場合のみ削除」
という原子的な事前条件がないため、別クライアントが確認後に子孫を生成する競合は
cos 単独では完全に防げない。

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

- `cmd/cos/main.go`: CLI オプション、cwd の取得、TUI 起動、Codex CLI への引き継ぎ
- `internal/domain/types.go`: `Thread`、`Conversation`、`SessionStore` などのドメイン型
- `internal/appserver/rpc.go`: JSON-RPC クライアント
- `internal/appserver/process.go`: app-server プロセス管理
- `internal/appserver/store.go`: Store の生成、接続管理、終了処理
- `internal/appserver/store_list.go`: 一覧、ページング、検索、子孫取得
- `internal/appserver/store_read.go`: 会話読込と paginated turns fallback
- `internal/appserver/store_delete.go`: 削除と結果不明時の照合
- `internal/appserver/store_convert.go`: app-server レスポンスと会話項目の変換
- `internal/tui/model.go`: Bubble Tea モデルと公開境界
- `internal/tui/commands.go`: 非同期 Store 操作
- `internal/tui/update.go`: キー・マウス操作と状態更新
- `internal/tui/view.go`: 一覧・会話・レイアウト表示
- `internal/tui/popup.go`: 確認・エラーポップアップ表示
- `internal/lock/writer_lock.go`: writer lock の状態確認
- 各ディレクトリの `*_test.go`: RPC、Store、UI、lock、CLI の責務別テスト

パッケージ間の境界は次のとおりとする。`internal/domain` は共有するドメイン型と
`SessionStore` を定義し、`internal/appserver` は `SessionStore` の実装として
`NewDefaultStore`、`NewAppServerStore`、`AppServerStore.Close` を提供する。
`internal/tui` は `NewModel(domain.SessionStore, cwd)` と
`Model.ResumeSession()` を提供し、CLI は再開対象の session だけを受け取る。

## 4. app-server 通信

起動時に以下のプロセスを開始し、`initialize` と `initialized` 通知を送信する。

```text
codex app-server --stdio
```

使用するメソッドは次のとおり。

- `initialize`
- `thread/list`
- `thread/read`
- `thread/turns/list`（paginated thread の会話取得 fallback）
- `thread/delete`
- 子孫確認用の `thread/list`（`ancestorThreadId`、通常・アーカイブ双方）

`thread/list` の基本パラメータは以下のとおり。

```json
{
  "archived": false,
  "sourceKinds": ["cli", "vscode", "appServer"],
  "sortKey": "updated_at",
  "sortDirection": "desc"
}
```

cwd 表示では起動時に取得した絶対パスを `cwd` に指定する。全体表示では `cwd` を省略する。
一覧は常に1ページ（最大100件）だけ取得し、`nextCursor` は UI が保持して次ページを
要求する。前ページへ戻るため、UI は取得済みの開始 cursor の履歴だけを保持する。

検索時は thread/list のページを逐次走査し、title・preview・cwd を cos 側で照合する。
一致が見つかった場合は、その app-server 1ページ内の一致をすべて1ページの検索結果として
返す。一致しない app-server ページは読み飛ばす。最大100ページ（最大10,000 session）までとし、
上限に達した場合は `ThreadPage.Incomplete` を設定して検索結果が不完全であることを UI に表示する。
`ThreadListRequest.SearchPages` と `ThreadPage.ScannedPages` で、UI のページ移動をまたいで走査数を
引き継ぐ。cursor の循環はエラーとして停止する。

app-server のバージョンによって `thread/list` の `data` は配列、または
`{ "items": [...], "nextCursor": "..." }` のオブジェクトで返る場合がある。
通信層の境界で両方の形式を受け入れ、UI 層には同じ session 一覧として渡す。

`thread/read` の `includeTurns: true` が利用できない app-server では、metadata-only の
`thread/read` 後に `thread/turns/list` を使って会話をページ取得する。

会話履歴は新しい turn から取得し、最大 100 turn、本文合計 1 MiB で読み込みを停止する。
取得した turn は表示時に時系列順へ戻す。上限で省略した場合は `Conversation.Truncated`
を設定し、右ペインに省略を表示する。`includeTurns: true` が使える旧サーバーでも、
取得後に同じ上限を適用する。

JSON-RPC の reader は、レスポンスの間に挿入される通知を無視して pending request に対応するレスポンスだけを返す。JSONL 1メッセージは改行を含め最大2 MiBとし、デコード前に超過を拒否する。app-server の終了、stdio の切断、不正JSON、メッセージ上限超過は、要求の完全な書き込み後なら結果不明として扱う。書き込み済み要求の応答タイムアウトも同じ削除結果照合経路に入り、書き込み前のキャンセル・失敗とは区別する。

UI の各非同期操作には 30 秒の deadline を設定する。session 選択、scope 切り替え、再読み込み、
削除・再開確認などで新しい操作を開始した場合、前の操作の context をキャンセルし、応答を待つ
RPC の pending entry も削除する。stdio への書き込みがキャンセル時点で停止している場合は
接続を閉じて書き込み goroutine を解放する。app-server プロセスの寿命は個別 request の context
から分離し、接続が閉じた場合は次の操作で再接続する。

## 5. ドメインモデル

`SessionStore` が UI と通信層の境界になる。

```go
type SessionStore interface {
    List(ctx context.Context, request ThreadListRequest) (ThreadPage, error)
    ListDescendants(ctx context.Context, id string) ([]Thread, error)
    Read(ctx context.Context, id string) (Conversation, error)
    Delete(ctx context.Context, id string) error
}
```

`ThreadPage` は現在ページの session、`NextCursor`、今回走査した app-server ページ数を含む。
通常一覧の UI 保持量は1ページに限定する。タイトル・preview・cwd の検索は検索上限に達した
場合も、取得できた結果を表示しつつ不完全検索であることを通知する。

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

cwd 表示と全体表示では選択位置と cursor 履歴を独立して扱う。`j/k` が現在ページの
端に到達したとき、次 cursor または履歴上の前 cursor があればページを取得する。
終端では循環せず停止する。取得件数が異なる場合は、そのページの末尾を超えない範囲に補正する。

app-server 由来の title、preview、cwd、会話本文、activity summary、エラー文字列は、
ANSI/OSC シーケンスと C0/C1/DEL 制御文字を除去してから表示する。会話本文の改行は保持し、
一覧や activity などの単一行表示では空白へ正規化する。

### 削除確認

`d` を押した時点で対象 session の writer lock、`thread/read` の最新状態、全階層の
子孫を確認する。writer lock の確認に失敗した、active だった、子孫が存在した、
または子孫確認に失敗した場合は削除確認を表示しない。非 active、writer lock が
空いており、子孫が存在しないことを確認できた場合のみ削除確認を表示する。

`y` を押した時点でも同じ確認を再実行する。確認後に状態が変化した場合や、子孫・
writer lock の検証に失敗した場合は `thread/delete` を呼ばない。

`Enter` を押した時点でも同じ確認を行う。writer lock が取得できない、または active の場合は 再開せず、削除・再開ともに使用中で実行できないことをエラーポップアップで通知する。非 active の場合は確認画面を表示せず TUI を終了する。

削除確認は画面下部のステータス表示ではなく、一覧と会話プレビューを背景に
残した中央ポップアップで表示する。対象 session のタイトルは最大 3 行まで
表示し、確認文の上下には空行を入れる。`y` と `n / Esc` の選択肢はポップアップ
内の中央に揃える。

削除確認後に別の writer が session を使用し始める競合は、再確認または
`thread/delete` のエラーとして検知する。書き込み済み削除要求の応答が timeout、接続終了、
不正JSON、メッセージ上限超過になった場合は、新しい5秒 context で app-server に再接続し、
`thread/read` を行う。不在を確認できた場合のみ成功扱いにし、存在、再接続失敗、照合不能は
結果不明エラーとする。`thread/delete` は自動再実行しない。削除の成否にかかわらず同じ
scope・ページを再取得し、現在の一覧が stale のまま残らないようにする。

子孫生成と削除の間の競合は、削除直前の再確認で窓を小さくするが、app-server のAPI仕様上
完全な原子性は保証しない。

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
- 検索の app-server ページ境界、100ページ上限、cursor 循環停止
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
- 書き込み済み削除要求の接続終了・不正JSON・再接続後照合
- 子孫 session の存在・取得失敗時の削除禁止
- 削除確認後に子孫が増えた場合の削除直前再確認
- paginated thread の会話取得 fallback
- Unicode検索語のBackspace、scope切替失敗、一覧表示範囲外クリック
- 非同期応答の scope、cwd、対象 ID、request 世代による破棄
- 非同期 request のキャンセル、pending 除去、書き込み停止時の stdio 切断
- 会話履歴の 100 turn / 1 MiB 制限と省略表示
- ANSI、OSC、BEL、Backspace、CR などの表示文字サニタイズ

## 9. 設計上の前提

- Go 1.26 を基準とする。
- Linux を対象とする。
- cwd の判定は親子関係ではなく、Codex に保存された cwd との文字列完全一致とする。
- 削除は app-server の `thread/delete` に任せ、ローカルファイルを直接操作しない。
- 削除は不可逆操作のため、必ず確認画面を挟む。
- 削除の「子孫なし」保証は確認時点のbest-effortであり、原子的なleaf-only削除APIが
  app-serverに追加されるまで別クライアントとの競合を完全には防げない。
