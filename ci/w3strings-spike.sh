#!/bin/sh
set -eu
: "${W3STRINGS_ORACLE:?set path to built independent oracle}"
export CGO_ENABLED=0 GOTOOLCHAIN=local
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
go build -o "$work/w3spike" ./tools/w3spike
for lang in en jp; do
 fixture=tools/w3spike/testdata/$lang
 "$W3STRINGS_ORACLE" encode "$fixture.csv" "$work/$lang.source"
 cmp "$fixture.w3strings" "$work/$lang.source"
 "$work/w3spike" "$work/$lang.source" "$work/$lang.output"
 cmp "$work/$lang.source" "$work/$lang.output"
 "$W3STRINGS_ORACLE" decode "$work/$lang.source" "$work/$lang.before.csv"
 "$W3STRINGS_ORACLE" decode "$work/$lang.output" "$work/$lang.after.csv"
 cmp "$work/$lang.before.csv" "$work/$lang.after.csv"
 # The independent CSV reader right-aligns IDs and returns hashed-ID table order.
 # Only leading ID-field padding and row order are ignored, never text whitespace.
 sed 's/^ *//' "$fixture.csv" | LC_ALL=C sort > "$work/expected"
 sed 's/^ *//' "$work/$lang.after.csv" | LC_ALL=C sort > "$work/actual"
 cmp "$work/expected" "$work/actual"
done
# Record, but do not hide, the independent crate's reproducible zero-key-count bug.
printf ';meta[language=en]\n; id|key(hex)|key(str)|text\n1001|00000000||test\n' > "$work/zero.csv"
"$W3STRINGS_ORACLE" encode "$work/zero.csv" "$work/zero.w3strings"
if "$W3STRINGS_ORACLE" decode "$work/zero.w3strings" "$work/zero.decoded"; then
 echo 'Expected pinned oracle zero-count limitation changed; investigate' >&2; exit 1
fi
echo 'Independent en/jp exact-byte and text/key checks passed; zero-key oracle limitation reproduced.'
