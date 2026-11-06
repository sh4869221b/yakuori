# PLaMo integration evidence, 2026-11-06

See [integration and reproduction instructions](../../plamo3.md) for pinned hashes,
model-generated translations, license terms and limits. These are local Linux
amd64 checks, not CI or GPU acceptance results. Go 1.27.3, CGO_ENABLED=0,
GOTOOLCHAIN=local, GOMAXPROCS=5 were used.

- `plamo-cpu-cli.log`: real `TestPlamoCPUCLI`, tagged `cpusmoke`, original pinned
  checkpoint, production model registry/loader/Core/SQLite TM, both directions
  generated then accepted from TM. Command is in the integration document.
- `qwen-cpu.log`: existing `TestCPUWrapperSmoke` under `cpusmoke`, with
  `YAKUORI_CPU_MODEL` pointing at the pinned Qwen2.5-Coder 0.5B Q4_K_M
  fixture documented in `docs/cpu-inference-spike.md`.
- `internal-unit.log`: `go test ./internal/...`, final implementation.
- `cuda-unavailable.log`: tagged CLI `doctor --check-model --backend cuda`
  against the imported PLaMo bundle, exit 1, driver not installed.
- `unchanged-main-monitor-failure.log`: isolated detached unchanged main
  `4a2605a`, `go test -count=1 ./tools/acceptance -run
  '^TestSupervisorKeepsSampleWhenWorkerEndsDuringQuery$' -v`. Same failure as
  the full suite on the integration branch. The test prints pointer values;
  they are not resource measurements.

Additional successful checks: `go vet ./...`, `go mod verify`,
`go run ci/check-cgo.go`, normal CPU dependency inspections with cgo enabled
for linux/amd64 and linux/arm64 (no selected nonstandard cgo packages),
`go build ./cmd/yakuori`, `go build -tags cuda ./cmd/yakuori`, and
`GOOS=linux GOARCH=arm64 go build ./cmd/yakuori`.
The optional CUDA graph when cgo is enabled selects purego's alternate cgo
implementation; the actual CUDA build here explicitly used CGO_ENABLED=0.
These checks do not prove real GPU execution or real ARM inference.

The `localize` file attempt on workspace overlayfs returned exit 1,
`localize prepare failed; TMCommitted=false; publication=not-published`.
No output was published. Supported-filesystem publication acceptance remains open.
No throughput or peak-memory benchmark was performed.
