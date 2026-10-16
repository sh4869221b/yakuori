# localize

## OVERVIEW
全unitの生成から最終artifact検証・TM commit・出力までを統合する独立ドメイン（score 12）。

## WHERE TO LOOK
| 変更対象 | 場所 | 契約 |
|---|---|---|
| 全件pipeline | `pipeline.go` | input / protect / plan / generate |
| 通常推論adapter | `inference.go` | requestとfinishの対応 |
| 長文分割 | `segmented.go` | 全segmentの事前admission |
| canonical条件 | `profile.go` | model・実効request・fixture条件 |
| 最終検証 | `finalize.go` | Export / Observe / CompareFinal / commit |
| 出力経路 | `text.go`, `file_linux.go` | stdout一括出力 / publication |
| TM連携 | `tm.go`, `tm*_test.go` | candidate再検証と一括commit |

## CONVENTIONS
- 全unitの保護・planを、最初の生成より前に完成させる。
- `Generate` は1unitでも失敗すれば利用可能な部分結果を返さない。
- import全体の上限とunit・segment上限をplan前後で照合する。
- canonical profileはentry条件・model条件・request条件を照合する。
- 生成不要unitでも共通の復元・検証契約を通す。
- TM候補は現在のsession・unit・profileで再検証する。
- AdapterのImportは保持量を自身で制限する契約を持つ。
- CoreはImport内部で既に行われたallocationを制限できない。
- export後にManifestのinputを照合し、閉じたstageをObserveする。
- `CompareFinal` とTMのmanifest照合が成功してからcommitを許可する。
- DB操作用timeoutと全体生成timeoutは別の上限。
- `Error` のPhaseとUnitIDで失敗地点を識別する。

## ANTI-PATTERNS
- finalizationのvalid状態をexport/checkの失敗を越えて持ち越さない。
- 未検査artifactや部分的な翻訳をTMへcommitしない。
- TM commitが失敗した後にfileをpublishしない。
- stdoutへ途中の訳文を書き出さない。
- 自動retryや無効候補の救済置換を生成経路へ追加しない。
