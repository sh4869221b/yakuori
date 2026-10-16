# PROJECT KNOWLEDGE BASE

**Generated:** 2026-10-16
**Commit:** a967f58
**Branch:** docs/init-deep-guidance

## OVERVIEW

Pure GoのローカルLLM翻訳基盤。ID・tag・placeholder・binaryを保持し、最初の対象形式はWitcher 3の `.w3strings`。
Linux向けの基盤で、翻訳Coreは存在するがCLIの `localize` には未接続。

## STRUCTURE

```text
yakuori/
├── cmd/yakuori/          # CLIエントリーポイント
├── internal/            # 翻訳Core、推論、永続化、公開処理
├── ci/                  # 検証スクリプトとclean gate用Dockerfile
├── docs/                # 基盤契約、推論調査、実行証跡
├── research/publication/ # 公開方式の研究
├── tools/               # cpuspike・w3spikeの研究用実装
├── licenses/            # 第三者ライセンス
└── THIRD_PARTY_NOTICES.md
```

## WHERE TO LOOK

| 作業 | 場所 | 注意点 |
|------|------|--------|
| CLI・終了コード | `cmd/yakuori/main.go`, `internal/cli/` | `cli.Run` → `os.Exit`、終了コードは0/1/2 |
| 翻訳処理の統合 | `internal/localize/` | Coreの生成から出力までの統合 |
| 入力のunit化 | `internal/adapter/text/`, `internal/unit/`, `internal/segment/` | text adapterは全文を1 unitとして扱う |
| 保護・受理判定 | `internal/protect/`, `internal/validate/` | 復元と検証 |
| identity・翻訳メモリ | `internal/identity/`, `internal/tm/` | 永続identityと完全一致SQLite |
| 推論契約・backend | `internal/inference/`, `internal/inference/goinfer/` | backend非依存requestとloader |
| モデル・設定 | `internal/models/`, `internal/config/` | ローカルGGUFとXDGパス |
| artifact・公開 | `internal/artifact/`, `internal/publication/` | artifact検証とLinux公開復旧 |
| 基盤の契約 | `docs/foundation.md`, `docs/source-profile-identity.md`, `docs/sqlite-feasibility.md` | 実装の前提 |
| 公開方式の契約 | `docs/research/linux-publication.md` | Linux公開方式 |
| 調査・証跡 | `tools/`, `research/publication/`, `docs/evidence/` | 研究実装と実行結果 |

## CODE MAP

LSPで確認済みの主要シンボル。シンボル単位の参照数はダイジェストでは未確定のため、推定しない。

| Symbol | Type | Location | Refs | Role |
|--------|------|----------|------|------|
| `Core` | 型 | `internal/localize/pipeline.go` | 未計測 | 翻訳パイプラインの統合 |
| `Prepare` | 関数 | `internal/publication/preflight_linux.go` | 未計測 | Linux公開の事前準備 |
| `Engine` | interface | `internal/inference/engine.go` | 未計測 | backend非依存の推論境界 |

## CONVENTIONS

- Go moduleは `1.27.0`、CIは厳密に `1.27.1` を使用する。
- 通常のbuild・test・vetは `CGO_ENABLED=0 GOTOOLCHAIN=local` で実行する。
- 設定はXDG配下の `/yakuori` を使用し、相対overrideは無視する。パス解決だけではディレクトリを作成しない。
- モデルはローカルGGUFを登録・選択・照合する。download機能はない。
- 各対象サブディレクトリの `AGENTS.md` に固有契約を記載し、ルートには横断情報を置く。

## ANTI-PATTERNS (THIS PROJECT)

- 通常の診断に入力本文やpromptを含めない。
- 部分公開、retry、暗黙の移行、旧stageの自動公開を行わない。
- CUDAからCPUへのfallbackはない。
- `tools/w3spike` のGPL oracleを製品にリンク・同梱しない。

## UNIQUE STYLES

- Coreは全unitの保護・planを先に完成させ、unitごとに生成・復元・Validate（TM候補は再検証）を行う。全件成功後にartifact検証 → TM commit → 出力へ進む。
- CUDA経路は `cuda` tag、`linux/amd64`、NVIDIA driverを前提とする。
- arm64の確認はcross-buildのみ。unit testの成功は実推論の成功を意味しない。
- `tools/cpuspike`、`tools/w3spike`、`research/publication` は研究用として分離されている。

## COMMANDS

README・CIで確認されたコマンド。ガイド生成時には未実行。

```sh
CGO_ENABLED=0 GOTOOLCHAIN=local go build ./cmd/yakuori
CGO_ENABLED=0 GOTOOLCHAIN=local go test ./...
CGO_ENABLED=0 GOTOOLCHAIN=local go vet ./...
sh ci/verify.sh
docker build --progress=plain -f ci/Dockerfile .
```

## NOTES

- CLIで実装済みなのはhelp・doctor・models。Coreの存在とCLIの `localize` 対応を混同しない。
- `ci/verify.sh` はcgo負例・依存監査、gofmt・build・test・vet、arm64 cross-buildを検証する。
- `ci/Dockerfile` はclean gate用。CPU・CUDA・長文脈の調査資料と、`docs/evidence/` の結果を区別する。
- 主要依存はgoinfer/cuda `0.23.0`、x/sys `0.48.0`、modernc SQLite `1.60.1`。
- 第三者通知は `THIRD_PARTY_NOTICES.md` と `licenses/` を参照する。
