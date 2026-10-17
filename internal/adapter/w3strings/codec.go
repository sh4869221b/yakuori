package w3strings

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"io"
	"math/bits"
	"slices"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/sh4869221b/yakuori/internal/config"
)

const maxCount = 4095

var le = binary.LittleEndian

type stringRecord struct {
	id, offset, length uint32
	table              int
	text               string
}

type keyRecord struct {
	hash, id uint32
	table    int
}

type payloadGap struct {
	offset, length uint32
}

// Table order and physical order are distinct. Gaps refer into the owned raw
// snapshot, in UTF-16 units, including the prefix and suffix payload gaps.
type parsedFile struct {
	language                          string
	magic                             uint32
	entries                           []stringRecord
	keys                              []keyRecord
	raw                               []byte
	start, keyCountAt, payloadCountAt int
	payloadUnits                      uint32
	physical                          []int
	gaps                              []payloadGap
}

// count accepts only the documented minimal one/two-byte Bit6 subset.
func count(raw []byte, p *int) (uint32, error) {
	if *p >= len(raw) {
		return 0, io.ErrUnexpectedEOF
	}
	first := raw[*p]
	*p++
	if first < 64 {
		return uint32(first), nil
	}
	if first >= 128 || *p >= len(raw) {
		return 0, errors.New("unsupported Bit6 encoding")
	}
	second := raw[*p]
	*p++
	if second == 0 || second >= 64 {
		return 0, errors.New("unsupported/nonminimal Bit6 encoding")
	}
	return uint32(first&63) | uint32(second)<<6, nil
}

// parse validates every relation and text budget before retaining any decoded
// text or snapshot. A nil inputLimits is for bounded output observation only:
// maxBytes remains mandatory and all structural/Unicode checks still apply.
func parse(raw []byte, maxBytes int, inputLimits *config.Limits) (*parsedFile, error) {
	if inputLimits != nil {
		if err := inputLimits.CheckArtifactBytes(len(raw)); err != nil {
			return nil, err
		}
	}
	if maxBytes <= 0 || len(raw) > maxBytes {
		return nil, errors.New("w3strings raw byte bound exceeded")
	}
	if len(raw) < 15 || string(raw[:4]) != "RTSW" || le.Uint32(raw[4:8]) != 162 {
		return nil, errors.New("unsupported w3strings header/size/version")
	}
	a := &parsedFile{}
	switch uint32(le.Uint16(raw[8:10]))<<16 | uint32(le.Uint16(raw[len(raw)-2:])) {
	case 0x43975139:
		a.language, a.magic = "en", 0x79321793
	case 0x54834893:
		a.language, a.magic = "jp", 0x59825646
	default:
		return nil, errors.New("unsupported w3strings language")
	}
	// Exclude the language suffix even while reading table/count fields.
	body, p := raw[:len(raw)-2], 10
	ns, err := count(body, &p)
	if err != nil {
		return nil, err
	}
	if uint64(ns) > uint64(len(body)-p)/12 {
		return nil, io.ErrUnexpectedEOF
	}
	if inputLimits != nil {
		if err := inputLimits.CheckUnits(int(ns)); err != nil {
			return nil, err
		}
	}
	stringsAt := p
	p += int(ns) * 12
	a.keyCountAt = p
	nk, err := count(body, &p)
	if err != nil {
		return nil, err
	}
	if uint64(nk) > uint64(len(body)-p)/8 {
		return nil, io.ErrUnexpectedEOF
	}
	keysAt := p
	p += int(nk) * 8
	a.payloadCountAt = p
	payload, err := count(body, &p)
	if err != nil {
		return nil, err
	}
	if len(body)-p != int(payload)*2 {
		return nil, errors.New("w3strings payload count/trailing bytes mismatch")
	}
	a.start, a.payloadUnits = p, payload

	// Only bounded inspection indexes exist at this point. Counts are <=4095,
	// table widths fit the supplied bytes, and the exact payload size is known.
	a.entries = make([]stringRecord, int(ns))
	a.keys = make([]keyRecord, int(nk))
	a.physical = make([]int, int(ns))
	ids := make(map[uint32]bool, int(ns))
	for i := range a.entries {
		pos := stringsAt + i*12
		e := stringRecord{id: le.Uint32(raw[pos:]) ^ a.magic, offset: le.Uint32(raw[pos+4:]), length: le.Uint32(raw[pos+8:]), table: pos}
		if ids[e.id] {
			return nil, errors.New("duplicate w3strings ID")
		}
		ids[e.id] = true
		// Subtraction avoids overflow even for maximum uint32 input fields.
		if e.offset >= payload || e.length >= payload-e.offset {
			return nil, errors.New("w3strings extent outside payload")
		}
		a.entries[i], a.physical[i] = e, i
	}
	hashes := make(map[uint32]bool, int(nk))
	for i := range a.keys {
		pos := keysAt + i*8
		k := keyRecord{hash: le.Uint32(raw[pos:]), id: le.Uint32(raw[pos+4:]) ^ a.magic, table: pos}
		if !ids[k.id] || hashes[k.hash] {
			return nil, errors.New("dangling w3strings key/duplicate hash")
		}
		hashes[k.hash], a.keys[i] = true, k
	}
	slices.SortFunc(a.physical, func(i, j int) int { return cmp.Compare(a.entries[i].offset, a.entries[j].offset) })
	end := uint32(0)
	for _, i := range a.physical {
		e := a.entries[i]
		if e.offset < end {
			return nil, errors.New("overlapping w3strings extents")
		}
		end = e.offset + e.length + 1
		if le.Uint16(raw[a.start+int(end-1)*2:]) != 0 {
			return nil, errors.New("nonzero w3strings terminator")
		}
	}

	var scratch [maxCount]uint16
	total := 0
	for _, e := range a.entries {
		units := scratch[:int(e.length)]
		decrypt(units, raw[a.start+int(e.offset)*2:], a.magic)
		n, err := textBytes(units)
		if err != nil {
			return nil, err
		}
		total += n // Nonoverlap bounds this by 3*4095, on every int width.
		if inputLimits != nil {
			if err := inputLimits.CheckUnitTextBytes(n); err != nil {
				return nil, err
			}
			if err := inputLimits.CheckTotalTextBytes(total); err != nil {
				return nil, err
			}
		}
	}
	// No failure after this point: materialize only fully validated text/layout.
	a.raw = bytes.Clone(raw)
	for i := range a.entries {
		e := &a.entries[i]
		units := scratch[:int(e.length)]
		decrypt(units, raw[a.start+int(e.offset)*2:], a.magic)
		e.text = string(utf16.Decode(units))
	}
	a.gaps = make([]payloadGap, len(a.physical)+1)
	end = 0
	for j, i := range a.physical {
		e := a.entries[i]
		a.gaps[j] = payloadGap{offset: end, length: e.offset - end}
		end = e.offset + e.length + 1
	}
	a.gaps[len(a.physical)] = payloadGap{offset: end, length: payload - end}
	return a, nil
}

func decrypt(dst []uint16, raw []byte, magic uint32) {
	state := uint16(magic >> 8)
	for i := range dst {
		// The format deliberately uses arithmetic modulo 2^16, not size math.
		dst[i] = le.Uint16(raw[i*2:]) ^ uint16(uint32(len(dst)+1)*uint32(state))
		state = bits.RotateLeft16(state, 1)
	}
}

func textBytes(units []uint16) (int, error) {
	n := 0
	for i := 0; i < len(units); i++ {
		u, r := units[i], rune(units[i])
		switch {
		case u == 0:
			return 0, errors.New("embedded w3strings NUL")
		case u >= 0xd800 && u <= 0xdbff:
			if i+1 == len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
				return 0, errors.New("invalid w3strings UTF-16")
			}
			i++
			r = utf16.DecodeRune(r, rune(units[i]))
		case u >= 0xdc00 && u <= 0xdfff:
			return 0, errors.New("invalid w3strings UTF-16")
		}
		n += utf8.RuneLen(r)
	}
	return n, nil
}
