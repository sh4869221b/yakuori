#!/bin/sh
# Binaries are freshly built from this checkout. Keep package cwd and the Go
# test2json framing protocol used by go test -json, including child separation.
set -eu
binary=$1
package=$2
timeout=$3
selector=$4
cd "$package"
exec go tool test2json -t -p "github.com/sh4869221b/yakuori/$package" \
 "$binary" -test.v=test2json -test.count=1 "-test.timeout=$timeout" "-test.run=$selector"
