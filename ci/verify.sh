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
 label=$1
 names=$2
 test_log=$3
 pass_names="$pipeline_tmp/$label.pass"
 allowed="$pipeline_tmp/$label.allowed"
 roots="$pipeline_tmp/$label.roots"
 bad=0
 if grep -Eq '^[[:space:]]*--- (FAIL|SKIP)' "$test_log"; then
  echo "pipeline lane reported FAIL or SKIP" >&2
  return 1
 fi
 grep -E '^[[:space:]]*--- PASS: ' "$test_log" \
  | sed -E "s/^[[:space:]]*--- PASS: //; s/ [(].*//" | sort >"$pass_names"
 if [ ! -s "$pass_names" ]; then
  echo "pipeline $label selected no PASS output" >&2
  return 1
 fi
 if [ -n "$(uniq -d "$pass_names")" ]; then
  echo "pipeline $label duplicated a PASS name" >&2
  return 1
 fi
 : >"$allowed"
 : >"$roots"
 while IFS= read -r name; do
  [ -n "$name" ] || continue
  runs=$(grep -Ec "^[[:space:]]*=== RUN   $name[[:space:]]*$" "$test_log" || true)
  passes=$(grep -Ec "^[[:space:]]*--- PASS: $name[[:space:]]*[(]" "$test_log" || true)
  if [ "$runs" -ne 1 ] || [ "$passes" -ne 1 ]; then
   echo "pipeline $label terminal $name RUN=$runs PASS=$passes, want 1/1" >&2
   bad=1
  fi
  root=${name%%/*}
  if ! grep -Fqx "$root" "$roots"; then
   printf "%s\n" "$root" >>"$roots"
   root_runs=$(grep -Ec "^[[:space:]]*=== RUN   $root[[:space:]]*$" "$test_log" || true)
   root_passes=$(grep -Ec "^[[:space:]]*--- PASS: $root[[:space:]]*[(]" "$test_log" || true)
   if [ "$root_runs" -ne 1 ] || [ "$root_passes" -ne 1 ]; then
    echo "pipeline $label root $root RUN=$root_runs PASS=$root_passes, want 1/1" >&2
    bad=1
   fi
  fi
  printf "%s\n" "$name" >>"$allowed"
  prefix=$name
  while :; do
   case $prefix in
   */*) prefix=${prefix%/*} ;;
   *) break ;;
   esac
   printf "%s\n" "$prefix" >>"$allowed"
  done
 done <"$names"
 while IFS= read -r name; do
  grep -Fqx "$name" "$allowed" || {
   echo "pipeline $label unexpected PASS $name" >&2
   bad=1
  }
 done <"$pass_names"
 [ "$bad" -eq 0 ]
}

cat >"$pipeline_tmp/fixture-inputs.names" <<'EOF'
TestPipelineFixtureInputs/valid/text
TestPipelineFixtureInputs/valid/w3
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

pipeline_fs_independent() {
 label=$1
 selector=$2
 names=$3
 log="$pipeline_tmp/$label.log"
 if ! go test -count=1 -timeout 180s -tags pipelinefaults ./internal/localize \
  -run "$selector" -v >"$log" 2>&1; then
  cat "$log" >&2; echo "pipeline lane $label failed" >&2; exit 1; fi
 pipeline_require "$label" "$names" "$log"
 cat "$log"
}

pipeline_fs_independent fixture-inputs '^TestPipelineFixtureInputs$' "$pipeline_tmp/fixture-inputs.names"
pipeline_fs_independent barrier '^TestPipelineHarnessBarrier$' "$pipeline_tmp/barrier.names"
pipeline_fs_independent guards '^TestPipelineHarnessGuards$' "$pipeline_tmp/guards.names"

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
pipeline_require refusal "$pipeline_tmp/refusal.names" "$pipeline_tmp/refusal.log"
cat "$pipeline_tmp/refusal.log"
mkdir -p bin
GOOS=linux GOARCH=arm64 go build -o bin/yakuori-linux-arm64 ./cmd/yakuori
# The SQLite spike is test-only, so cross-build its driver-bearing test binary too.
GOOS=linux GOARCH=arm64 go test -c -o bin/sqliteprobe-linux-arm64.test ./internal/sqliteprobe
GOOS=linux GOARCH=arm64 go test -c -o bin/tm-linux-arm64.test ./internal/tm
