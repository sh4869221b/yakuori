#!/bin/sh
set -eu
export CGO_ENABLED=0 GOTOOLCHAIN=local
[ "$(go env GOOS)/$(go env GOARCH)" = linux/amd64 ]
[ "$(go env GOVERSION)" = go1.27.3 ]
go version
go run ci/check-cgo.go
# Prove the guard catches even a build-tag-excluded import.
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
printf '//go:build never\n\npackage forbidden\nimport "C"\n' > "$fixture/forbidden.go"
if go run ci/check-cgo.go "$fixture"; then
 echo 'cgo guard self-test unexpectedly passed' >&2; exit 1
fi
# Inspect selected third-party dependencies with cgo enabled; go list does not compile.
for arch in amd64 arm64; do
 deps=$(CGO_ENABLED=1 GOOS=linux GOARCH=$arch go list -deps -test -f '{{if and (not .Standard) .CgoFiles}}{{.ImportPath}}{{end}}' ./...)
 if [ -n "$deps" ]; then echo "cgo dependencies: $deps" >&2; exit 1; fi
done
[ -z "$(gofmt -l cmd internal ci/check-cgo.go)" ]
go build ./cmd/yakuori
# Test-only offline observer self-test (strace and python3 come from the CI image).
python3 ci/test-test-events.py
python3 ci/test-offline.py
# Model-free acquisition failures against the built binary; no MODEL env is set,
# so normal CI never fetches or places a model.
YAKUORI_PULL_BINARY="$PWD/yakuori" go test -count=1 -tags modelsmoke ./internal/cli -run '^TestPullCLIFailure$' -v
# Model-free CLI/error matrix under the same offline observer.
offline_evidence=$(mktemp -d)
trap 'rm -rf "$fixture" "$offline_evidence"' EXIT
# Keep only the observed Go command on one CPU for the existing tracer.
# Unit/concurrency tests below retain the caller's normal affinity.
YAKUORI_PULL_BINARY="$PWD/yakuori" YAKUORI_PULL_EVIDENCE="$offline_evidence" \
  python3 -c '
import os
import sys
os.sched_setaffinity(0, {min(os.sched_getaffinity(0))})
print("offline model-free affinity:", sorted(os.sched_getaffinity(0)), flush=True)
os.execvp(sys.argv[1], sys.argv[1:])
' go test -count=1 -timeout 15m -tags offlinesmoke ./internal/cli -run '^TestOfflineCLIMatrix$/^model-free$' -v
go test -count=1 ./...
go vet ./...
# Issue #68/#69/#70 pipeline fault harness. Vet and compilation check the test-only
# surface; they are not a behavior PASS. The filesystem-independent lanes below
# run real child lifecycles. Qualified publication positives require a non-root
# ext4/Btrfs mount and run in .github/workflows/ci.yml (non-root-filesystems).
pipeline_tmp=$(mktemp -d)
trap 'rm -rf "$fixture" "$offline_evidence" "$pipeline_tmp"' EXIT
go vet -tags pipelinefaults ./internal/localize ./internal/publication ./internal/cli ./internal/tm
mkdir "$pipeline_tmp/normal"
go test -c -o "$pipeline_tmp/normal/" ./internal/localize ./internal/publication ./internal/cli ./internal/tm
go test -c -tags pipelinefaults -o "$pipeline_tmp/" ./internal/localize ./internal/publication ./internal/cli ./internal/tm
echo 'pipelinefaults compile-only: normal/tagged test binaries built; not a behavior PASS'

pipeline_require() {
 python3 ci/check-test-events.py "$2" "$3"
}

printf '%s\n' TestSignalReadyCancellation >"$pipeline_tmp/signal-ready.names"
if ! sh ci/run-test-binary.sh "$pipeline_tmp/cli.test" internal/cli 180s '^TestSignalReadyCancellation$' \
 >"$pipeline_tmp/signal-ready.log" 2>&1; then
 cat "$pipeline_tmp/signal-ready.log" >&2; exit 1; fi
pipeline_require signal-ready "$pipeline_tmp/signal-ready.names" "$pipeline_tmp/signal-ready.log"
cat "$pipeline_tmp/signal-ready.log"

cat >"$pipeline_tmp/fixture-inputs.names" <<'EOF'
TestPipelineFixtureInputs/valid
TestPipelineFixtureInputs/valid/text
TestPipelineFixtureInputs/valid/w3
TestPipelineFixtureInputs/reject-authored-mismatch
TestPipelineFixtureInputs/reject-authored-mismatch/text
TestPipelineFixtureInputs/reject-authored-mismatch/w3
EOF
cat >"$pipeline_tmp/barrier.names" <<'EOF'
TestPipelineHarnessBarrier
EOF
cat >"$pipeline_tmp/guards.names" <<'EOF'
TestPipelineHarnessGuards/healthy-positive-control
TestPipelineHarnessGuards/wrong-boundary
TestPipelineHarnessGuards/unreached
TestPipelineHarnessGuards/uninjected
TestPipelineHarnessGuards/early-return
TestPipelineHarnessGuards/timeout-explicit-cancel
TestPipelineHarnessGuards/unreaped
TestPipelineHarnessGuards/malformed-input
TestPipelineHarnessGuards/cleanup-rescue
EOF
cat >"$pipeline_tmp/refusal.names" <<'EOF'
TestPipelineUnsupportedFilesystem
EOF

# Real SQLite component cases require no supported publication filesystem.
cat >"$pipeline_tmp/tm-component.names" <<'EOF'
TestPipelineTMCommitCheckpoint/error
TestPipelineTMCommitCheckpoint/cancel
TestPipelineTMCommitCheckpoint/release
TestPipelineTMCommitStoppedProcess/before-commit
TestPipelineTMCommitStoppedProcess/after-commit
TestCommitConcurrentKey/miss
TestCommitConcurrentKey/valid_changed
TestCommitConcurrentKey/invalid_changed
TestCommitConcurrentKey/invalid_removed
TestCommitConcurrentKey/valid_removed
TestCommitBusyAndDeadline/writer
TestCommitBusyAndDeadline/reader
TestCommitBusyAndDeadline/expired
TestCommitBusyAndDeadline/pool_wait
TestCommitCleanupFailure
EOF
cat >"$pipeline_tmp/tm-hooks.names" <<'EOF'
TestPipelineTMCommitHooks/success
TestPipelineTMCommitHooks/isolation
TestPipelineTMCommitHooks/next-run-isolation
TestPipelineTMCommitHooks/before-error
TestPipelineTMCommitHooks/before-cancel
TestPipelineTMCommitHooks/after-cancel
TestPipelineTMCommitHooks/private-callback
TestPipelineTMCommitHooks/ineligible-run
TestPipelineTMCommitHooks/dml-error
EOF
if ! sh ci/run-test-binary.sh "$pipeline_tmp/normal/tm.test" internal/tm 10m '^(TestPipelineTMCommitCheckpoint|TestPipelineTMCommitStoppedProcess|TestCommitConcurrentKey|TestCommitBusyAndDeadline|TestCommitCleanupFailure)$' \
 >"$pipeline_tmp/tm-component.log" 2>&1; then
 cat "$pipeline_tmp/tm-component.log" >&2; exit 1; fi
pipeline_require tm-component "$pipeline_tmp/tm-component.names" "$pipeline_tmp/tm-component.log"
cat "$pipeline_tmp/tm-component.log"
if ! sh ci/run-test-binary.sh "$pipeline_tmp/tm.test" internal/tm 10m '^TestPipelineTMCommitHooks$' >"$pipeline_tmp/tm-hooks.log" 2>&1; then
 cat "$pipeline_tmp/tm-hooks.log" >&2; exit 1; fi
pipeline_require tm-hooks "$pipeline_tmp/tm-hooks.names" "$pipeline_tmp/tm-hooks.log"
cat "$pipeline_tmp/tm-hooks.log"

# Issue #69 Core stdout paths do not call publication.Prepare. CLI fixtures,
# including stdout, require qualified storage and stay in the qualified lane.
cat >"$pipeline_tmp/precommit-contract-stdout.names" <<'EOF'
TestPipelinePrecommitContract/normal-controls
TestPipelinePrecommitContract/normal-controls/stdout
EOF
for lane in inference segmented final; do
 : >"$pipeline_tmp/precommit-$lane-stdout.names"
done
for backend in cpu cuda; do
 for row in happy builder-error builder-cancel count-error count-cancel \
  effective-equal effective-plus-one policy-over-limit foreign-request \
  partial-plus-error max-tokens context-limit timeout-finish canceled-finish \
  decode-error invalid-output unknown-finish prompt-count-mismatch deadline \
  cancel-after-stop core-deadline; do
  printf '%s\n' "TestPipelinePrecommitInference/$backend-text-stdout-none-$row" \
   >>"$pipeline_tmp/precommit-inference-stdout.names"
 done
 for row in happy all-admission second-partial second-max second-unknown \
  second-decode second-count-mismatch second-cancel second-deadline \
  protected-token-invalid parent-validation-invalid; do
  printf '%s\n' "TestPipelinePrecommitSegmented/$backend-text-stdout-none-$row" \
   >>"$pipeline_tmp/precommit-segmented-stdout.names"
 done
 for row in happy observe-read-error cancel-observe-read cancel-observe-after; do
  printf '%s\n' "TestPipelinePrecommitFinal/$backend-text-stdout-none-$row" \
   >>"$pipeline_tmp/precommit-final-stdout.names"
 done
done

pipeline_fs_independent() {
 label=$1
 selector=$2
 names=$3
 log="$pipeline_tmp/$label.log"
 if ! sh ci/run-test-binary.sh "$pipeline_tmp/localize.test" internal/localize 180s "$selector" >"$log" 2>&1; then
  cat "$log" >&2; echo "pipeline lane $label failed" >&2; exit 1; fi
 pipeline_require "$label" "$names" "$log"
 cat "$log"
}

printf '%s\n' TestPipelineStdoutBoundaries/normal TestPipelineStdoutBoundaries/cancel-before TestPipelineStdoutBoundaries/cancel-after >"$pipeline_tmp/stdout-boundaries.names"
pipeline_fs_independent stdout-boundaries '^TestPipelineStdoutBoundaries$' "$pipeline_tmp/stdout-boundaries.names"
pipeline_fs_independent fixture-inputs '^TestPipelineFixtureInputs$' "$pipeline_tmp/fixture-inputs.names"
pipeline_fs_independent barrier '^TestPipelineHarnessBarrier$' "$pipeline_tmp/barrier.names"
pipeline_fs_independent guards '^TestPipelineHarnessGuards$' "$pipeline_tmp/guards.names"
pipeline_fs_independent precommit-contract-stdout '^TestPipelinePrecommitContract$/^normal-controls$/^stdout$' "$pipeline_tmp/precommit-contract-stdout.names"
pipeline_fs_independent precommit-inference-stdout '^TestPipelinePrecommitInference$/^(cpu|cuda)-text-stdout-none-' "$pipeline_tmp/precommit-inference-stdout.names"
pipeline_fs_independent precommit-segmented-stdout '^TestPipelinePrecommitSegmented$/^(cpu|cuda)-text-stdout-none-' "$pipeline_tmp/precommit-segmented-stdout.names"
pipeline_fs_independent precommit-final-stdout '^TestPipelinePrecommitFinal$/^(cpu|cuda)-text-stdout-none-' "$pipeline_tmp/precommit-final-stdout.names"

# The clean image's /tmp must be a real tmpfs/overlay, so this lane proves the
# typed unsupported-filesystem refusal rather than passing by accident.
tmp_fs=$(stat -f -c %T /tmp)
case "$tmp_fs" in
tmpfs|overlayfs) ;;
*) echo "/tmp is $tmp_fs, not a verified tmpfs/overlay" >&2; exit 1 ;;
esac
echo "unsupported-filesystem refusal lane filesystem: $tmp_fs"
if ! TMPDIR=/tmp GOTMPDIR=/tmp sh ci/run-test-binary.sh "$pipeline_tmp/localize.test" internal/localize 180s '^TestPipelineUnsupportedFilesystem$' \
 >"$pipeline_tmp/refusal.log" 2>&1; then
 cat "$pipeline_tmp/refusal.log" >&2; echo 'unsupported-filesystem refusal lane failed' >&2; exit 1; fi
pipeline_require refusal "$pipeline_tmp/refusal.names" "$pipeline_tmp/refusal.log"
cat "$pipeline_tmp/refusal.log"
mkdir -p bin
GOOS=linux GOARCH=arm64 go build -o bin/yakuori-linux-arm64 ./cmd/yakuori
# The SQLite spike is test-only, so cross-build its driver-bearing test binary too.
GOOS=linux GOARCH=arm64 go test -c -o bin/sqliteprobe-linux-arm64.test ./internal/sqliteprobe
GOOS=linux GOARCH=arm64 go test -c -o bin/tm-linux-arm64.test ./internal/tm
