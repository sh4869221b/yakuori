package w3strings

import (
	"io"
	"strconv"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func (a *Adapter) Observe(reader io.Reader) (artifact.FinalObservation, error) {
	if err := a.session.Check(); err != nil {
		return artifact.FinalObservation{}, err
	}
	// The imported table counts and their encoded widths are already bounded.
	// Only the payload count width and P<=4095 may grow during translation.
	// Read one extra byte solely to detect overflow of this structural bound.
	bmax := a.source.payloadCountAt + 2 + 2*maxCount + 2
	raw, err := io.ReadAll(io.LimitReader(reader, int64(bmax)+1))
	if err != nil {
		return artifact.FinalObservation{}, err
	}
	stage, err := parse(raw, bmax, nil)
	if err != nil {
		return artifact.FinalObservation{}, err
	}
	language := stage.language
	if language == "jp" {
		language = "ja"
	}
	metadata := map[string][]byte{
		"fixed-header":      stage.raw[:8],
		"string-count":      stage.raw[10 : stage.keyCountAt-12*len(stage.entries)],
		"key-count":         stage.raw[stage.keyCountAt : stage.payloadCountAt-8*len(stage.keys)],
		"decoded-id-order":  {},
		"decoded-key-order": {},
		"language":          append(stage.raw[8:10:10], stage.raw[len(stage.raw)-2:]...),
		"string-records":    {},
		"key-records":       {},
		"payload-count":     stage.raw[stage.payloadCountAt:stage.start],
		"terminators":       {},
		"opaque-layout":     {},
	}
	observed := artifact.FinalObservation{
		Adapter: adapterName, FormatSchema: formatSchema,
		Units: make([]artifact.ObservedUnit, len(stage.entries)),
		Keys:  make([]artifact.KeyRelation, len(stage.keys)), Metadata: metadata,
	}
	ids := make(map[uint32]unit.UnitID, len(stage.entries))
	for i, entry := range stage.entries {
		id, err := unit.NewUnitID(adapterName, formatSchema, strconv.FormatUint(uint64(entry.id), 10))
		if err != nil {
			return artifact.FinalObservation{}, err
		}
		ids[entry.id] = id
		observed.Units[i] = artifact.ObservedUnit{ID: id, Text: entry.text, TargetLanguage: language}
		metadata["decoded-id-order"] = le.AppendUint32(metadata["decoded-id-order"], entry.id)
		metadata["string-records"] = append(metadata["string-records"], stage.raw[entry.table:entry.table+12]...)
		end := stage.start + 2*int(entry.offset+entry.length)
		metadata["terminators"] = append(metadata["terminators"], stage.raw[end:end+2]...)
	}
	for i, key := range stage.keys {
		observed.Keys[i] = artifact.KeyRelation{UnitID: ids[key.id], Key: string(stage.raw[key.table : key.table+4])}
		metadata["decoded-key-order"] = le.AppendUint32(metadata["decoded-key-order"], key.hash)
		metadata["decoded-key-order"] = le.AppendUint32(metadata["decoded-key-order"], key.id)
		metadata["key-records"] = append(metadata["key-records"], stage.raw[key.table:key.table+8]...)
	}
	for _, gap := range stage.gaps {
		metadata["opaque-layout"] = le.AppendUint32(metadata["opaque-layout"], gap.offset)
		metadata["opaque-layout"] = le.AppendUint32(metadata["opaque-layout"], gap.length)
		start, end := stage.start+2*int(gap.offset), stage.start+2*int(gap.offset+gap.length)
		metadata["opaque-layout"] = append(metadata["opaque-layout"], stage.raw[start:end]...)
	}
	return observed, nil
}
