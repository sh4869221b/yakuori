// w3spike is a bounded research tool, not the production Adapter.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"os"
	"unicode/utf16"
)

const maxFile = 1 << 20

var le = binary.LittleEndian

type entry struct {
	ID, Offset, Length uint32
	Text               string
	table              int
}
type keyEntry struct {
	Hash, ID uint32
	table    int
}
type artifact struct {
	Language string
	Entries  []entry
	Keys     []keyEntry
	raw      []byte
	magic    uint32
	start    int
}

// This spike intentionally accepts only the observed one/two-byte Bit6 subset.
// Larger encodings and the independent writer's anomalous 0x80 zero are rejected.
func count(b []byte, p *int) (uint32, error) {
	if *p >= len(b) {
		return 0, io.ErrUnexpectedEOF
	}
	first := b[*p]
	*p++
	if first < 64 {
		return uint32(first), nil
	}
	if first >= 128 || *p >= len(b) {
		return 0, errors.New("unsupported Bit6 encoding")
	}
	second := b[*p]
	*p++
	if second >= 64 || second == 0 {
		return 0, errors.New("unsupported/nonminimal Bit6 encoding")
	}
	return uint32(first&63) | uint32(second)<<6, nil
}
func parse(b []byte) (*artifact, error) {
	if len(b) < 15 || len(b) > maxFile || string(b[:4]) != "RTSW" || le.Uint32(b[4:8]) != 162 {
		return nil, errors.New("unsupported header/size/version")
	}
	a := &artifact{raw: bytes.Clone(b)}
	key := uint32(le.Uint16(b[8:10]))<<16 | uint32(le.Uint16(b[len(b)-2:]))
	switch key {
	case 0x43975139:
		a.Language = "en"
		a.magic = 0x79321793
	case 0x54834893:
		a.Language = "jp"
		a.magic = 0x59825646
	default:
		return nil, errors.New("unsupported language key")
	}
	p := 10
	n, err := count(b, &p)
	if err != nil {
		return nil, err
	}
	if uint64(n)*12 > uint64(len(b)-p) {
		return nil, io.ErrUnexpectedEOF
	}
	ids := map[uint32]bool{}
	for range n {
		id := le.Uint32(b[p:]) ^ a.magic
		if ids[id] {
			return nil, errors.New("duplicate ID")
		}
		ids[id] = true
		a.Entries = append(a.Entries, entry{ID: id, Offset: le.Uint32(b[p+4:]), Length: le.Uint32(b[p+8:]), table: p})
		p += 12
	}
	n, err = count(b, &p)
	if err != nil {
		return nil, err
	}
	if uint64(n)*8 > uint64(len(b)-p) {
		return nil, io.ErrUnexpectedEOF
	}
	hashes := map[uint32]bool{}
	for range n {
		h, id := le.Uint32(b[p:]), le.Uint32(b[p+4:])^a.magic
		if !ids[id] || hashes[h] {
			return nil, errors.New("dangling ID/duplicate key hash")
		}
		hashes[h] = true
		a.Keys = append(a.Keys, keyEntry{Hash: h, ID: id, table: p})
		p += 8
	}
	n, err = count(b, &p)
	if err != nil {
		return nil, err
	}
	if uint64(p)+uint64(n)*2+2 != uint64(len(b)) {
		return nil, errors.New("payload count/trailing bytes mismatch")
	}
	a.start = p
	occupied := make([]bool, n)
	for i := range a.Entries {
		e := &a.Entries[i]
		end := uint64(e.Offset) + uint64(e.Length) + 1
		if end > uint64(n) {
			return nil, errors.New("string range outside payload")
		}
		for j := uint64(e.Offset); j < end; j++ {
			if occupied[j] {
				return nil, errors.New("overlapping strings")
			}
			occupied[j] = true
		}
		pos := p + int(e.Offset)*2
		if le.Uint16(b[pos+int(e.Length)*2:]) != 0 {
			return nil, errors.New("nonzero terminator")
		}
		units := make([]uint16, e.Length)
		k := uint16(a.magic >> 8)
		for j := range units {
			units[j] = le.Uint16(b[pos+j*2:]) ^ uint16((e.Length+1)*uint32(k))
			k = bits.RotateLeft16(k, 1)
		}
		for j := 0; j < len(units); j++ {
			u := units[j]
			if u == 0 {
				return nil, errors.New("embedded NUL")
			}
			if u >= 0xd800 && u <= 0xdbff {
				if j+1 >= len(units) || units[j+1] < 0xdc00 || units[j+1] > 0xdfff {
					return nil, errors.New("invalid UTF-16")
				}
				j++
			} else if u >= 0xdc00 && u <= 0xdfff {
				return nil, errors.New("invalid UTF-16")
			}
		}
		e.Text = string(utf16.Decode(units))
	}
	return a, nil
}

// Rebuild every parsed table record and encrypted text from decoded values.
// Header, encoded counts, terminators and unclaimed bytes retain their exact layout.
func (a *artifact) roundtrip() ([]byte, error) {
	out := bytes.Clone(a.raw)
	for _, e := range a.Entries {
		units := utf16.Encode([]rune(e.Text))
		if uint32(len(units)) != e.Length {
			return nil, errors.New("text length changed")
		}
		le.PutUint32(out[e.table:], e.ID^a.magic)
		le.PutUint32(out[e.table+4:], e.Offset)
		le.PutUint32(out[e.table+8:], e.Length)
		k := uint16(a.magic >> 8)
		pos := a.start + int(e.Offset)*2
		for j, u := range units {
			le.PutUint16(out[pos+j*2:], u^uint16((e.Length+1)*uint32(k)))
			k = bits.RotateLeft16(k, 1)
		}
	}
	for _, k := range a.Keys {
		le.PutUint32(out[k.table:], k.Hash)
		le.PutUint32(out[k.table+4:], k.ID^a.magic)
	}
	if !bytes.Equal(out, a.raw) {
		return nil, errors.New("no-translation bytes changed")
	}
	return out, nil
}
func run(args []string, w io.Writer) error {
	if len(args) != 2 {
		return errors.New("usage: w3spike INPUT OUTPUT (new path only)")
	}
	f, err := os.Open(args[0])
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	a, err := parse(b)
	if err != nil {
		return err
	}
	out, err := a.roundtrip()
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(args[1], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := dst.Write(out)
	closeErr = dst.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	h := sha256.Sum256(out)
	return json.NewEncoder(w).Encode(struct {
		Language      string
		Entries, Keys int
		SHA256        string
		ExactBytes    bool
	}{a.Language, len(a.Entries), len(a.Keys), hex.EncodeToString(h[:]), true})
}
func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
