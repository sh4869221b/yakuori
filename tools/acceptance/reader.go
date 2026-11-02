package acceptance

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strconv"

	"github.com/sh4869221b/yakuori/internal/localize"
)

// CompareOracle checks length-delimited records from the independent reader.
// The protocol preserves text boundaries and every key relation, including zero.
func CompareOracle(data []byte, units []localize.AcceptanceUnit, keys []localize.AcceptanceKey) error {
	r := bytes.NewReader(data)
	magic := make([]byte, 8)
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != "YAKW3O1\x00" {
		return errors.New("oracle record header mismatch")
	}
	readUint32 := func() (uint32, error) { var n uint32; err := binary.Read(r, binary.LittleEndian, &n); return n, err }
	version, err := readUint32()
	if err != nil || version != 162 {
		return errors.New("oracle version mismatch")
	}
	languageLength, err := readUint32()
	if err != nil || languageLength != 2 {
		return errors.New("oracle language missing")
	}
	language := make([]byte, languageLength)
	if _, err := io.ReadFull(r, language); err != nil || string(language) != "jp" {
		return errors.New("oracle language mismatch")
	}
	count, err := readUint32()
	if err != nil || uint64(count) != uint64(len(units)) {
		return errors.New("oracle unit count mismatch")
	}
	expected := make(map[string]string, len(units))
	for _, u := range units {
		if _, exists := expected[u.ID]; exists {
			return errors.New("duplicate accepted ID")
		}
		expected[u.ID] = u.Text
	}
	for range count {
		id, err := readUint32()
		if err != nil {
			return errors.New("oracle ID missing")
		}
		length, err := readUint32()
		if err != nil || uint64(length) > uint64(r.Len()) {
			return errors.New("oracle text length invalid")
		}
		text := make([]byte, int(length))
		if _, err := io.ReadFull(r, text); err != nil {
			return errors.New("oracle text missing")
		}
		stableID := strconv.FormatUint(uint64(id), 10)
		want, exists := expected[stableID]
		if !exists || string(text) != want {
			return errors.New("oracle ID or translation mismatch")
		}
		delete(expected, stableID)
	}
	keyCount, err := readUint32()
	if err != nil || uint64(keyCount) != uint64(len(keys)) {
		return errors.New("oracle key count mismatch")
	}
	type relation struct {
		id  uint32
		key uint32
	}
	expectedKeys := make(map[relation]int, len(keys))
	for _, key := range keys {
		id, err := strconv.ParseUint(key.ID, 10, 32)
		if err != nil || len(key.Key) != 4 {
			return errors.New("invalid W3 key relation")
		}
		expectedKeys[relation{uint32(id), binary.LittleEndian.Uint32(key.Key)}]++
	}
	for range keyCount {
		id, idErr := readUint32()
		key, keyErr := readUint32()
		pair := relation{id, key}
		if idErr != nil || keyErr != nil || expectedKeys[pair] == 0 {
			return errors.New("oracle key relation mismatch")
		}
		expectedKeys[pair]--
	}
	if len(expected) != 0 || r.Len() != 0 {
		return errors.New("oracle record mismatch")
	}
	return nil
}
