package artifact_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type fakeRecord struct{ id, text string }
type fakeKey struct{ id, key string }
type fakeImage struct {
	adapter, schema, language string
	records                   []fakeRecord
	keys                      []fakeKey
	metadata                  map[string][]byte
}

func encodeFake(image fakeImage) []byte {
	var raw []byte
	field := func(value string) {
		raw = binary.LittleEndian.AppendUint32(raw, uint32(len(value)))
		raw = append(raw, value...)
	}
	field(image.adapter)
	field(image.schema)
	field(image.language)
	raw = binary.LittleEndian.AppendUint32(raw, uint32(len(image.records)))
	for _, record := range image.records {
		field(record.id)
		field(record.text)
	}
	raw = binary.LittleEndian.AppendUint32(raw, uint32(len(image.keys)))
	for _, key := range image.keys {
		field(key.id)
		field(key.key)
	}
	names := make([]string, 0, len(image.metadata))
	for name := range image.metadata {
		names = append(names, name)
	}
	slices.Sort(names)
	raw = binary.LittleEndian.AppendUint32(raw, uint32(len(names)))
	for _, name := range names {
		field(name)
		field(string(image.metadata[name]))
	}
	return raw
}

func decodeFake(raw []byte) (fakeImage, error) {
	r := bytes.NewReader(raw)
	number := func() (uint32, error) {
		var n uint32
		err := binary.Read(r, binary.LittleEndian, &n)
		return n, err
	}
	field := func() (string, error) {
		n, err := number()
		if err != nil {
			return "", err
		}
		if uint64(n) > uint64(r.Len()) {
			return "", io.ErrUnexpectedEOF
		}
		value := make([]byte, n)
		if _, err := io.ReadFull(r, value); err != nil {
			return "", err
		}
		return string(value), nil
	}
	var image fakeImage
	for _, target := range []*string{&image.adapter, &image.schema, &image.language} {
		value, err := field()
		if err != nil {
			return fakeImage{}, err
		}
		*target = value
	}
	for section := range 3 {
		count, err := number()
		if err != nil {
			return fakeImage{}, err
		}
		for range count {
			first, err := field()
			if err != nil {
				return fakeImage{}, err
			}
			second, err := field()
			if err != nil {
				return fakeImage{}, err
			}
			switch section {
			case 0:
				image.records = append(image.records, fakeRecord{first, second})
			case 1:
				image.keys = append(image.keys, fakeKey{first, second})
			case 2:
				if image.metadata == nil {
					image.metadata = make(map[string][]byte)
				}
				image.metadata[first] = []byte(second)
			}
		}
	}
	if r.Len() != 0 {
		return fakeImage{}, errors.New("trailing fake bytes")
	}
	return image, nil
}

type fakeAdapter struct {
	image    fakeImage
	session  unit.Session
	profile  validate.Profile
	manifest artifact.Manifest
	exports  int
}

func importFake(raw []byte, profile validate.Profile) (*fakeAdapter, error) {
	image, err := decodeFake(raw)
	if err != nil {
		return nil, err
	}
	var units []unit.TranslationUnit
	for _, record := range image.records {
		id, err := unit.NewUnitID(image.adapter, image.schema, record.id)
		if err != nil {
			return nil, err
		}
		u, err := unit.NewTranslationUnit(id, []byte(record.text), image.language, "ja", nil)
		if err != nil {
			return nil, err
		}
		units = append(units, u)
	}
	session, err := unit.NewSession(raw, units)
	if err != nil {
		return nil, err
	}
	return &fakeAdapter{image: image, session: session, profile: profile}, nil
}

func (a *fakeAdapter) stage(session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) ([]byte, artifact.ExpectedTranslations, error) {
	if !a.session.SameImport(session) || a.profile.Digest() != profile.Digest() {
		return nil, artifact.ExpectedTranslations{}, errors.New("fake adapter state belongs to another import or profile")
	}
	expected, err := artifact.PrepareExport(session, profile, accepted)
	if err != nil {
		return nil, artifact.ExpectedTranslations{}, err
	}
	var records []fakeRecord
	var length uint32
	for _, u := range session.Units() {
		text, exists := expected.Text(u.ID())
		if !exists {
			return nil, artifact.ExpectedTranslations{}, artifact.ErrIncompleteExport
		}
		records = append(records, fakeRecord{u.ID().StableID(), text})
		length += uint32(len(text))
	}
	var keys []artifact.KeyRelation
	for _, key := range a.image.keys {
		id, err := unit.NewUnitID(a.image.adapter, a.image.schema, key.id)
		if err != nil {
			return nil, artifact.ExpectedTranslations{}, err
		}
		keys = append(keys, artifact.KeyRelation{UnitID: id, Key: key.key})
	}
	preserved := map[string][]byte{"header": a.image.metadata["header"]}
	recomputed := map[string][]byte{"length": binary.LittleEndian.AppendUint32(nil, length), "language": []byte("ja")}
	a.manifest, err = artifact.NewManifest(session, profile, a.image.adapter, a.image.schema, keys, preserved, recomputed)
	if err != nil {
		return nil, artifact.ExpectedTranslations{}, err
	}
	a.exports++
	return encodeFake(fakeImage{
		adapter: a.image.adapter, schema: a.image.schema, language: "ja", records: records, keys: a.image.keys,
		metadata: map[string][]byte{"header": preserved["header"], "length": recomputed["length"], "language": recomputed["language"]},
	}), expected, nil
}

func (a *fakeAdapter) checkStage(raw []byte, expected artifact.ExpectedTranslations) error {
	image, err := decodeFake(raw)
	if err != nil {
		return err
	}
	final := artifact.FinalObservation{Adapter: image.adapter, FormatSchema: image.schema, Metadata: image.metadata}
	for _, record := range image.records {
		id, err := unit.NewUnitID(image.adapter, image.schema, record.id)
		if err != nil {
			return err
		}
		final.Units = append(final.Units, artifact.ObservedUnit{ID: id, Text: record.text, TargetLanguage: image.language})
	}
	for _, key := range image.keys {
		id, err := unit.NewUnitID(image.adapter, image.schema, key.id)
		if err != nil {
			return err
		}
		final.Keys = append(final.Keys, artifact.KeyRelation{UnitID: id, Key: key.key})
	}
	return artifact.CompareFinal(a.manifest, expected, final)
}

func adapterFixture(t *testing.T) (*fakeAdapter, []validate.AcceptedTranslation) {
	t.Helper()
	profile, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	raw := encodeFake(fakeImage{
		adapter: "fixture", schema: "v1", language: "en",
		records: []fakeRecord{{"a", "Hello"}, {"b", ""}}, keys: []fakeKey{{"a", "key"}, {"a", "key"}},
		metadata: map[string][]byte{"header": {7}, "length": {5, 0, 0, 0}, "language": []byte("en")},
	})
	a, err := importFake(raw, profile)
	if err != nil {
		t.Fatal(err)
	}
	var accepted []validate.AcceptedTranslation
	for i, u := range a.session.Units() {
		candidate := "こんにちは"
		if i == 1 {
			candidate = ""
		}
		translation, err := validate.Validate(a.session, u.ID(), profile, candidate)
		if err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, translation)
	}
	return a, accepted
}

func TestAdapterBoundaryRoundTrip(t *testing.T) {
	a, accepted := adapterFixture(t)
	stage, expected, err := a.stage(a.session, a.profile, accepted)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.checkStage(stage, expected); err != nil {
		t.Fatal(err)
	}
	image, err := decodeFake(stage)
	if err != nil {
		t.Fatal(err)
	}
	if a.exports != 1 || image.language != "ja" || image.records[0].text != "こんにちは" || image.records[1].text != "" || bytes.Equal(stage, a.session.Artifact()) {
		t.Fatalf("wrong staged translation: %+v", image)
	}
	if err := a.manifest.CheckInput(a.session); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterBoundaryRejectsInvalidExport(t *testing.T) {
	for _, name := range []string{"zero accepted", "wrong session accepted", "wrong profile accepted", "missing", "duplicate", "extra", "foreign private state", "foreign profile"} {
		t.Run(name, func(t *testing.T) {
			a, accepted := adapterFixture(t)
			session, profile := a.session, a.profile
			other, otherAccepted := adapterFixture(t)
			switch name {
			case "zero accepted":
				accepted[0] = validate.AcceptedTranslation{}
			case "wrong session accepted":
				accepted = otherAccepted
			case "wrong profile accepted", "foreign profile":
				otherProfile, err := validate.NewProfile([32]byte{2})
				if err != nil {
					t.Fatal(err)
				}
				if name == "foreign profile" {
					profile = otherProfile
					break
				}
				for i, u := range session.Units() {
					accepted[i], err = validate.Validate(session, u.ID(), otherProfile, accepted[i].Text())
					if err != nil {
						t.Fatal(err)
					}
				}
			case "missing":
				accepted = accepted[:1]
			case "duplicate":
				accepted[1] = accepted[0]
			case "extra":
				extraSession, _, _, extra := exportFixture(t, "first", "second", "third")
				if extraSession.SameImport(session) {
					t.Fatal("unexpected import identity")
				}
				accepted = append(accepted, extra[2])
			case "foreign private state":
				a = other
			}
			stage, _, err := a.stage(session, profile, accepted)
			if err == nil || stage != nil || a.exports != 0 {
				t.Fatalf("invalid export reached writer: bytes=%d, calls=%d, error=%v", len(stage), a.exports, err)
			}
		})
	}
	for _, tt := range []struct {
		name   string
		mutate func(*fakeImage)
	}{
		{"ID text swap", func(f *fakeImage) { f.records[0].id, f.records[1].id = f.records[1].id, f.records[0].id }},
		{"changed text", func(f *fakeImage) { f.records[0].text = "unchecked" }},
		{"key removal", func(f *fakeImage) { f.keys = f.keys[:1] }},
		{"preserved bytes", func(f *fakeImage) { f.metadata["header"][0] = 8 }},
		{"wrong expected metadata", func(f *fakeImage) { f.metadata["length"][0]++ }},
		{"unknown field", func(f *fakeImage) { f.metadata["unknown"] = []byte{1} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, accepted := adapterFixture(t)
			stage, expected, err := a.stage(a.session, a.profile, accepted)
			if err != nil {
				t.Fatal(err)
			}
			image, err := decodeFake(stage)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(&image)
			if err := a.checkStage(encodeFake(image), expected); !errors.Is(err, artifact.ErrFinalMismatch) {
				t.Fatalf("corrupted actual stage accepted: %v", err)
			}
		})
	}
}
