package w3strings

import (
	"errors"
	"io"
	"math/bits"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func (a *Adapter) Export(writer io.Writer, session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) (artifact.Manifest, error) {
	if !a.session.SameImport(session) {
		return artifact.Manifest{}, unit.ErrInvalidSession
	}
	expected, err := artifact.PrepareExport(session, profile, accepted)
	if err != nil {
		return artifact.Manifest{}, err
	}
	source := a.source
	texts := make([]string, len(source.entries))
	lengths := make([]uint32, len(source.entries))
	// Import already bounded counts and every gap. Reserve all terminators and
	// opaque units before counting text, so each addition is checked by subtraction.
	payload := uint32(len(source.entries))
	for _, gap := range source.gaps {
		payload += gap.length
	}
	for i, u := range session.Units() {
		text, exists := expected.Text(u.ID())
		if !exists {
			return artifact.Manifest{}, artifact.ErrIncompleteExport
		}
		if !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
			return artifact.Manifest{}, errors.New("invalid w3strings output UTF-8/NUL")
		}
		for _, r := range text {
			width := uint32(1)
			if r > 0xffff {
				width = 2
			}
			if width > maxCount-payload {
				return artifact.Manifest{}, errors.New("unsupported w3strings output payload count")
			}
			payload += width
			lengths[i] += width
		}
		texts[i] = text
	}

	preserved := map[string][]byte{
		"fixed-header":      source.raw[:8],
		"string-count":      source.raw[10 : source.keyCountAt-12*len(source.entries)],
		"key-count":         source.raw[source.keyCountAt : source.payloadCountAt-8*len(source.keys)],
		"decoded-id-order":  {},
		"decoded-key-order": {},
	}
	payloadCount := []byte{byte(payload)}
	if payload >= 64 {
		payloadCount = []byte{byte(payload&63) | 64, byte(payload >> 6)}
	}
	metadata := map[string][]byte{
		"language":       {0x83, 0x54, 0x93, 0x48},
		"string-records": {},
		"key-records":    {},
		"payload-count":  payloadCount,
		"terminators":    make([]byte, 2*len(source.entries)),
		"opaque-layout":  {},
	}
	// Ns,Nk,P <=4095 bound all record fields and this complete allocation by
	// 10+2+12*4095+2+8*4095+2+2*4095+2 bytes, fitting int and uint32.
	start := source.payloadCountAt + len(payloadCount)
	out := make([]byte, start+2*int(payload)+2)
	copy(out, source.raw[:source.payloadCountAt])
	le.PutUint16(out[8:], 0x5483)
	copy(out[source.payloadCountAt:], payloadCount)
	le.PutUint16(out[len(out)-2:], 0x4893)
	offsets := make([]uint32, len(source.entries))
	cursor := uint32(0)
	for j, gap := range source.gaps {
		rawGap := source.raw[source.start+2*int(gap.offset) : source.start+2*int(gap.offset+gap.length)]
		metadata["opaque-layout"] = le.AppendUint32(metadata["opaque-layout"], cursor)
		metadata["opaque-layout"] = le.AppendUint32(metadata["opaque-layout"], gap.length)
		metadata["opaque-layout"] = append(metadata["opaque-layout"], rawGap...)
		copy(out[start+2*int(cursor):], rawGap)
		cursor += gap.length
		if j == len(source.physical) {
			break
		}
		i := source.physical[j]
		offsets[i] = cursor
		state := uint16(0x8256)
		for k, u := range utf16.Encode([]rune(texts[i])) {
			// Only this cipher multiplication intentionally wraps modulo 2^16.
			le.PutUint16(out[start+2*(int(cursor)+k):], u^uint16((lengths[i]+1)*uint32(state)))
			state = bits.RotateLeft16(state, 1)
		}
		cursor += lengths[i] + 1
	}
	for i, entry := range source.entries {
		preserved["decoded-id-order"] = le.AppendUint32(preserved["decoded-id-order"], entry.id)
		record := le.AppendUint32(nil, entry.id^0x59825646)
		record = le.AppendUint32(record, offsets[i])
		record = le.AppendUint32(record, lengths[i])
		copy(out[entry.table:], record)
		metadata["string-records"] = append(metadata["string-records"], record...)
	}
	for _, key := range source.keys {
		preserved["decoded-key-order"] = le.AppendUint32(preserved["decoded-key-order"], key.hash)
		preserved["decoded-key-order"] = le.AppendUint32(preserved["decoded-key-order"], key.id)
		record := le.AppendUint32(nil, key.hash)
		record = le.AppendUint32(record, key.id^0x59825646)
		copy(out[key.table:], record)
		metadata["key-records"] = append(metadata["key-records"], record...)
	}
	manifest, err := artifact.NewManifest(session, profile, adapterName, formatSchema, a.keys, preserved, metadata)
	if err != nil {
		return artifact.Manifest{}, err
	}
	n, err := writer.Write(out)
	if err != nil {
		return artifact.Manifest{}, err
	}
	if n != len(out) {
		return artifact.Manifest{}, io.ErrShortWrite
	}
	return manifest, nil
}
