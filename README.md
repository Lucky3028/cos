# cos

Codex のセッションを一覧・閲覧・再開・削除する Linux 向け TUI ツールです。

`cos` はセッションの JSONL ファイルを直接操作せず、`codex app-server --stdio` の JSON-RPC API を利用します。

## 主な機能

- 起動したディレクトリのセッション一覧表示
- 全セッションへの切り替え
- タイトル、preview、cwd による検索
- セッションの会話内容と活動概要の閲覧
- `codex resume` によるセッション再開
- 確認付きのセッション削除

## 必要条件

- Linux
- Go 1.26 以降（ソースからビルドする場合）
- mise（リポジトリからビルド・検証する場合）
- `codex` コマンド
- `codex app-server --stdio` を利用できる Codex CLI

## インストール

Go があれば、リポジトリをチェックアウトせずにインストールできます。

```sh
go install github.com/Lucky3028/cos/cmd/cos@latest
```

または、リポジトリを取得してビルドします。

```sh
mise run build
```

## 使い方

```sh
cos
cos --help
cos --version
```

- 起動時のカレントディレクトリにあるセッションが最初に表示されます。
- セッションを選択して `Enter` を押すと、TUI を終了して、選択したセッションで Codex を起動します。
- セッションを選択して `d` を押すと、セッションの削除に進みます。

## キー操作

| キー | 操作 |
| --- | --- |
| `j` / `k`, `↑` / `↓` | セッション選択 |
| `Tab` | 左右ペイン切り替え |
| `PageUp` / `PageDown` | 会話スクロール |
| `/` | タイトル・preview・cwd 検索 |
| `a` | cwd 表示と全体表示の切り替え |
| `p` | 会話プレビューの表示・非表示 |
| `r` | 再読み込み |
| `Enter` | 選択中のセッションを再開 |
| `d` | 削除確認 |
| `y` | セッション削除 |
| `n` / `Esc` | 確認・検索のキャンセル |
| `q` / `Ctrl-C` | 終了 |

マウスのクリックとホイール操作にも対応しています。

## リリース成果物の検証

リリースページから成果物と検証ファイルを取得します。

```sh
VERSION=vX.Y.Z
gh release download "$VERSION" --repo Lucky3028/cos
```

checksum と cosign の keyless 署名を検証します。`cosign` はあらかじめインストールしてください。

```sh
sha256sum --ignore-missing -c checksums.txt

cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp 'https://github\.com/Lucky3028/cos/.github/workflows/release\.yml@refs/tags/v.*' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  checksums.txt
```

GitHub CLI を使うと、リリース成果物の Artifact Attestation も検証できます。

```sh
gh attestation verify cos_X.Y.Z_linux_amd64.tar.gz --repo Lucky3028/cos
```

## 開発

```sh
mise run test
mise run test-race
mise run vet
mise run build
```

すべての CI 用チェックは `mise run ci` で実行できます。

設計の詳細は [docs/DESIGN.md](docs/DESIGN.md) と [docs/DETAIL_DESIGN.md](docs/DETAIL_DESIGN.md) を参照してください。
