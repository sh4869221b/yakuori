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
YAKUORI_PULL_BINARY="$PWD/yakuori" YAKUORI_PULL_EVIDENCE="$offline_evidence" \
  go test -count=1 -timeout 15m -tags offlinesmoke ./internal/cli -run '^TestOfflineCLIMatrix$/^model-free$' -v
go test -count=1 ./...
go vet ./...
mkdir -p bin
GOOS=linux GOARCH=arm64 go build -o bin/yakuori-linux-arm64 ./cmd/yakuori
# The SQLite spike is test-only, so cross-build its driver-bearing test binary too.
GOOS=linux GOARCH=arm64 go test -c -o bin/sqliteprobe-linux-arm64.test ./internal/sqliteprobe
GOOS=linux GOARCH=arm64 go test -c -o bin/tm-linux-arm64.test ./internal/tm
