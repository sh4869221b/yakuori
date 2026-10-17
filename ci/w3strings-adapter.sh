#!/bin/sh
set -eu
: "${W3STRINGS_ORACLE:?set path to built independent oracle}"
[ -x "$W3STRINGS_ORACLE" ] || { echo 'Independent oracle executable unavailable' >&2; exit 1; }
export CGO_ENABLED=0 GOTOOLCHAIN=local
work=$(mktemp -d)
cleanup() {
 rm -rf "$work"
 [ ! -e "$work" ] || { echo 'Adapter QA cleanup failed' >&2; exit 1; }
 echo "Adapter QA temporary directory removed: $work"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
go build -o "$work/w3adapterqa" ./tools/w3adapterqa
for name in translated jp better-keybinds monster-of-the-week; do
 case "$name" in
 translated)
  source=tools/w3spike/testdata/en.w3strings
  csv=internal/adapter/w3strings/testdata/translated-jp.csv
  ;;
 jp)
  source=tools/w3spike/testdata/jp.w3strings
  csv=tools/w3spike/testdata/jp.csv
  ;;
 *)
  source=tools/w3spike/testdata/$name/en.w3strings
  csv=tools/w3spike/testdata/$name/en.expected.csv
  ;;
 esac
 "$work/w3adapterqa" --input "$source" --translations "$csv" --output "$work/$name.w3strings"
 "$W3STRINGS_ORACLE" decode "$work/$name.w3strings" "$work/$name.csv"
 # The independent reader pads IDs and emits hashed-ID table order.
 # Only that padding/order and the explicit target-language header differ.
 sed -e 's/^;meta\[language=en\]$/;meta[language=jp]/' -e 's/^ *//' "$csv" | LC_ALL=C sort > "$work/expected"
 sed 's/^ *//' "$work/$name.csv" | LC_ALL=C sort > "$work/actual"
 cmp "$work/expected" "$work/actual"
 if [ "$name" = jp ]; then
  cmp "$source" "$work/$name.w3strings"
 fi
 echo "Independent production $name ID/text/key projection and Japanese language matched."
done
echo 'Oracle CSV does not distinguish keyless/hash0 or prove multiple-key relations; Go structural tests cover those.'
