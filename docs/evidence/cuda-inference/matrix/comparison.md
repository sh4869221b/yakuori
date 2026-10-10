# CPU/CUDA 実測比較

4 fixtures × 3 pairs × CPU/CUDA × cold/warm の48 runsはすべて成功した。24 process casesは全件exit0、監視上限による停止と未実行はない。各CUDA processは対応CPU reportの全planned request（rendered prompt、spans、token IDs、identity、effective policy／StopIDs、count）との一致を生成前に検証した。各backendで132/132 unitsがmechanical validationを通過した。各fixture/backendの出力は全3pairsのcold/warm間で一致した。構造的な成功は訳文の意味品質を認定しない。

詳細集計は [summary.json](summary.json)、全ケースの状態は [schedule.json](schedule.json)。同じ公式Go1.27.1 CGO=0 binary、同じIndex GGUF/int4、4096 context、2048 output、greedy/seed0/thinking=false。CPUとCUDAはfixture/pairごとにCPU→CUDAの順で逐次実行した。first16 allowed CPUs、GOMAXPROCS16/GOMEMLIMIT14336MiB、監視RSS16384MiB、host-available floor1024MiB、process wall1800s、request300s、generation1500s。hard cgroup boundsはなく、過去#51との環境完全一致は未確認。

| Fixture | CPU cold/warm generation中央値ms | CUDA cold/warm generation中央値ms | CPU/CUDA request TTFT中央値ms |
| --- | ---: | ---: | ---: |
| ラベル19units | 61943.90 / 61143.93 | 1419.92 / 1367.79 | 3035.18 / 47.93 |
| 短文 | 3701.48 / 3632.17 | 131.87 / 105.68 | 3199.65 / 58.27 |
| 中文 | 7342.70 / 7143.25 | 391.95 / 376.38 | 4473.95 / 69.98 |
| 長文 | 19921.57 / 20065.42 | 1393.37 / 1336.65 | 8911.05 / 124.41 |

Generationは各Engine.Generateの呼び出しから返却までの時間の合計。load、planning、validationは含めない。TTFTはGenerate呼び出しから最初の実stream tokenの観測までで、正確なprefill時間ではない。post-first-token intervalは最初のtoken観測から実stream drainまでで、正確なdecode phase時間ではない。generation throughputは実output tokensの合計／generation時間の合計、post-first-token throughputは各requestのmax(stream tokens−1,0)の合計／対応intervalの合計。CPU/CUDAの実出力token数は異なるため、同一出力長の速度比較ではない。

load_msはprivate snapshot／既存identity計算、decoder load、tokenizer setupを含む。coldは同じloaded modelでの最初のrunで、OS page cacheはflushしていない。run total_msはplanning、protection、Core生成、full-parent Restore／Validationを含む。process_total_msはfixture読込からsetupとcold/warm終了までで、Close、report serialization、外側のtest wrapperを除く。load／RSS／device/PID VRAM sampled maxima／throughputはsummaryと各raw reportにある。VRAMの間隔は名目100msにquery時間を加えたもの。各sampleの実elapsedを記録しており、連続的な厳密ピークを主張しない。PrefillPathは静的capabilityであり、各requestの実GPU経路やfallbackは未証明。

## 品質所見

以下は各pair1 cold出力を原文と対照した所見。全pair/cold-warmでbackend内の出力一致を実測確認した。ゲーム固有の公式用語集や実MODでの人手受入れは対象外。

- **ラベル**：[CPU](01-fixture-pair1-cpu.json)／[CUDA](01-fixture-pair1-cuda.json)。両方とも`Cast Aard`、`Cast Axii`、`Sheathe Auto`が英語のままで、日本語UIとして未完の箇所がある。CPUの`エイメイをキャンセルする`に対してCUDAは`エイミングをキャンセル`。`Cast Igni`はCPU`カスト・イグニ`、CUDA`Igniをキャストする`。`Draw Auto Sword`は両方`自動剣を引く`で不自然。`Discard Target`は両方`ターゲットを無視する`で、選択解除を意図するラベルなら意味がずれる。名前の日本語表記／英字混在と動作語の統一は別途判断が必要。unitsの順番と19件という構造は保持された。
- **短文**：[CPU](00-short-pair1-cpu.json)／[CUDA](00-short-pair1-cuda.json)。両方`北の門のそばに灯りを置いてください。`。依頼、位置、北の門の意味を保持している。`lantern`を`灯り`としたため、具体物の表現が広くなっている。backend間の品質差は観測されない。
- **中文**：[CPU](01-medium-pair1-cpu.json)／[CUDA](01-medium-pair1-cuda.json)。橋の閉鎖条件、待機場所、スープと毛布、日の出前の偵察、村の評議会への知らせ、今夜の支払い不要を両方保持している。CPUは`閉鎖されたままである`、CUDAは`閉鎖されたままです`で、主な差は文体。両方`old mill`を`古い水車場`と具体化しており、原文が明示しない水車という選択が入る。大きな文や条件の欠落は見られない。
- **長文**：[CPU](02-long-pair1-cpu.json)／[CUDA](02-long-pair1-cuda.json)。手紙を持った港への到着、霜、故障したコンパス、閉ざされた道、地図と石壁、船での物資輸送、灯台、パン／薬品／毛布／穀物2袋、救命胴衣、荷車修理、別れ、次の雪より前の手紙という展開を両方保持している。両方`物資を船で送し`と`狭い水路をクルーを案内する`という不自然な文法がある。`sister`を`姉`に限定し、`stayed behind`を`残された`と受身化している。CPU`若い大工`に対しCUDA`若い木職人`、CPU`手を振る`に対しCUDA`手を振う`。CUDAに明確な大規模省略は見られないが、両方の日本語表現に修正余地があり、意味品質の無条件合格とはしない。

速度向上はこの機材・モデル・上記条件の観測結果である。精密phase clock、request-specific prefill path、他の機材、OOM、MODの人手受入れ、製品GPU選択やfallbackは今回の成功から導かない。
