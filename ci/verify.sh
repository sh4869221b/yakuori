#!/bin/sh
set -eu
export CGO_ENABLED=0 GOTOOLCHAIN=local
[ "$(go env GOOS)/$(go env GOARCH)" = linux/amd64 ]
[ "$(go env GOVERSION)" = go1.27.1 ]
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
# Issue #68 pipeline fault harness. Tagged vet and build check the test-only
# surface; they are not a behavior PASS. The filesystem-independent lanes below
# run real child lifecycles. Qualified publication positives require a non-root
# ext4/Btrfs mount and run in .github/workflows/publication-probe.yml.
pipeline_tmp=$(mktemp -d)
trap 'rm -rf "$fixture" "$offline_evidence" "$pipeline_tmp"' EXIT
go vet -tags pipelinefaults ./internal/localize ./internal/publication ./internal/cli
for pipeline_pkg in localize publication cli; do
 go test -c -tags pipelinefaults -o "$pipeline_tmp/$pipeline_pkg.test" "./internal/$pipeline_pkg"
done
echo 'pipelinefaults compile-only: tagged test binaries built; not a behavior PASS'

pipeline_require() {
 test_log=$1
 test_name=$2
 want_leaves=$3
 grep -Eq "^[[:space:]]*=== RUN   $test_name[[:space:]]*$" "$test_log" || {
  echo "pipeline selector matched no $test_name" >&2; return 1; }
 grep -Eq "^[[:space:]]*--- PASS: $test_name[[:space:]]*[(]" "$test_log" || {
  echo "pipeline $test_name did not PASS" >&2; return 1; }
 if grep -Eq '^[[:space:]]*--- (FAIL|SKIP)' "$test_log"; then
  echo "pipeline lane reported FAIL or SKIP" >&2; return 1; fi
 leaves=$(grep -Ec "^[[:space:]]*--- PASS: $test_name/" "$test_log" || true)
 [ "$leaves" -eq "$want_leaves" ] || {
  echo "pipeline $test_name leaves=$leaves, want $want_leaves" >&2; return 1; }
}

pipeline_fs_independent() {
 label=$1
 test_name=$2
 selector=$3
 want_leaves=$4
 log="$pipeline_tmp/$label.log"
 if ! go test -count=1 -timeout 180s -tags pipelinefaults ./internal/localize \
  -run "$selector" -v >"$log" 2>&1; then
  cat "$log" >&2; echo "pipeline lane $label failed" >&2; exit 1; fi
 pipeline_require "$log" "$test_name" "$want_leaves"
 cat "$log"
}

pipeline_fs_independent fixture-inputs TestPipelineFixtureInputs '^TestPipelineFixtureInputs$' 6
pipeline_fs_independent barrier TestPipelineHarnessBarrier '^TestPipelineHarnessBarrier$' 0
pipeline_fs_independent guards TestPipelineHarnessGuards '^TestPipelineHarnessGuards$' 9

# The clean image's /tmp must be a real tmpfs/overlay, so this lane proves the
# typed unsupported-filesystem refusal rather than passing by accident.
tmp_fs=$(stat -f -c %T /tmp)
case "$tmp_fs" in
tmpfs|overlayfs) ;;
*) echo "/tmp is $tmp_fs, not a verified tmpfs/overlay" >&2; exit 1 ;;
esac
echo "unsupported-filesystem refusal lane filesystem: $tmp_fs"
if ! TMPDIR=/tmp GOTMPDIR=/tmp go test -count=1 -timeout 180s -tags pipelinefaults \
 ./internal/localize -run '^TestPipelineUnsupportedFilesystem$' -v \
 >"$pipeline_tmp/refusal.log" 2>&1; then
 cat "$pipeline_tmp/refusal.log" >&2; echo 'unsupported-filesystem refusal lane failed' >&2; exit 1; fi
pipeline_require "$pipeline_tmp/refusal.log" TestPipelineUnsupportedFilesystem 0
cat "$pipeline_tmp/refusal.log"
mkdir -p bin
GOOS=linux GOARCH=arm64 go build -o bin/yakuori-linux-arm64 ./cmd/yakuori
# The SQLite spike is test-only, so cross-build its driver-bearing test binary too.
GOOS=linux GOARCH=arm64 go test -c -o bin/sqliteprobe-linux-arm64.test ./internal/sqliteprobe
GOOS=linux GOARCH=arm64 go test -c -o bin/tm-linux-arm64.test ./internal/tm
