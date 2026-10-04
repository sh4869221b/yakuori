package localize

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type fakeRecord struct{ ID, Text string }
type fakeImage struct {
	Adapter, Schema, Language string
	Records                   []fakeRecord
	Keys                      []string
	Header                    []byte
	Length                    uint32
}

type fakeTM struct {
	active bool
	calls  int
	rows   []validate.AcceptedTranslation
	err    error
	events []string
}

func (tm *fakeTM) Commit(_ context.Context, session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) error {
	tm.active = true
	defer func() { tm.active = false }()
	tm.calls++
	tm.events = append(tm.events, "commit")
	for _, row := range accepted {
		if err := validate.CheckBinding(session, row.UnitID(), profile, row); err != nil {
			return err
		}
	}
	if tm.err != nil {
		return tm.err
	}
	tm.rows = slices.Clone(accepted)
	return nil
}

type fakeAdapter struct {
	t               *testing.T
	tm              *fakeTM
	input           fakeImage
	session         unit.Session
	observed        []byte
	corrupt         func(*fakeImage)
	exportErr       error
	observeErr      error
	invalidManifest bool
}

func (a *fakeAdapter) step(name string) {
	a.t.Helper()
	if a.tm.active {
		a.t.Fatalf("transaction active during %s", name)
	}
	a.tm.events = append(a.tm.events, name)
}

func (a *fakeAdapter) Import(raw []byte, _ validate.Profile) (unit.Session, error) {
	a.step("import")
	if err := json.Unmarshal(raw, &a.input); err != nil {
		return unit.Session{}, err
	}
	var units []unit.TranslationUnit
	for _, record := range a.input.Records {
		id, err := unit.NewUnitID(a.input.Adapter, a.input.Schema, record.ID)
		if err != nil {
			return unit.Session{}, err
		}
		u, err := unit.NewTranslationUnit(id, []byte(record.Text), a.input.Language, "ja", nil)
		if err != nil {
			return unit.Session{}, err
		}
		units = append(units, u)
	}
	var err error
	a.session, err = unit.NewSession(raw, units)
	return a.session, err
}

func (a *fakeAdapter) Export(writer io.Writer, session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) (artifact.Manifest, error) {
	a.step("export")
	if a.exportErr != nil {
		return artifact.Manifest{}, a.exportErr
	}
	if !a.session.SameImport(session) {
		return artifact.Manifest{}, unit.ErrInvalidSession
	}
	image := fakeImage{Adapter: a.input.Adapter, Schema: a.input.Schema, Language: "ja", Keys: slices.Clone(a.input.Keys), Header: bytes.Clone(a.input.Header)}
	for _, translation := range accepted {
		image.Records = append(image.Records, fakeRecord{translation.UnitID().StableID(), translation.Text()})
		image.Length += uint32(len(translation.Text()))
	}
	keys := make([]artifact.KeyRelation, len(a.input.Keys))
	for i, key := range a.input.Keys {
		keys[i] = artifact.KeyRelation{UnitID: session.Units()[0].ID(), Key: key}
	}
	manifest, err := artifact.NewManifest(session, profile, a.input.Adapter, a.input.Schema, keys,
		map[string][]byte{"header": a.input.Header}, map[string][]byte{"length": binary.LittleEndian.AppendUint32(nil, image.Length)})
	if err != nil {
		return artifact.Manifest{}, err
	}
	if a.corrupt != nil {
		a.corrupt(&image)
	}
	if err := json.NewEncoder(writer).Encode(image); err != nil {
		return artifact.Manifest{}, err
	}
	if a.invalidManifest {
		return artifact.Manifest{}, nil
	}
	return manifest, nil
}

func (a *fakeAdapter) Observe(reader io.Reader) (artifact.FinalObservation, error) {
	a.step("observe")
	var err error
	a.observed, err = io.ReadAll(reader)
	if err != nil {
		return artifact.FinalObservation{}, err
	}
	if a.observeErr != nil {
		return artifact.FinalObservation{}, a.observeErr
	}
	var image fakeImage
	if err := json.Unmarshal(a.observed, &image); err != nil {
		return artifact.FinalObservation{}, err
	}
	final := artifact.FinalObservation{Adapter: image.Adapter, FormatSchema: image.Schema,
		Metadata: map[string][]byte{"header": image.Header, "length": binary.LittleEndian.AppendUint32(nil, image.Length)}}
	for _, record := range image.Records {
		id, err := unit.NewUnitID(image.Adapter, image.Schema, record.ID)
		if err != nil {
			return artifact.FinalObservation{}, err
		}
		final.Units = append(final.Units, artifact.ObservedUnit{ID: id, Text: record.Text, TargetLanguage: image.Language})
	}
	for _, key := range image.Keys {
		id, err := unit.NewUnitID(image.Adapter, image.Schema, "a")
		if err != nil {
			return artifact.FinalObservation{}, err
		}
		final.Keys = append(final.Keys, artifact.KeyRelation{UnitID: id, Key: key})
	}
	return final, nil
}

func finalizeFixture(t *testing.T) (*fakeAdapter, *finalization) {
	t.Helper()
	a := &fakeAdapter{t: t, tm: &fakeTM{}}
	input, err := json.Marshal(fakeImage{Adapter: "fixture", Schema: "v1", Language: "en", Records: []fakeRecord{{"a", "Hello"}, {"b", "World"}}, Keys: []string{"key", "key"}, Header: []byte{7}, Length: 10})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	session, err := a.Import(input, profile)
	if err != nil {
		t.Fatal(err)
	}
	engine := fakeEngine(func(_ context.Context, id unit.UnitID, _ string) (Generation, error) {
		a.step("generate " + id.StableID())
		return Generation{Text: "訳" + id.StableID(), Finish: Stop}, nil
	})
	accepted, err := NewCore(engine).Generate(context.Background(), session, profile)
	if err != nil {
		t.Fatal(err)
	}
	f, err := prepareFinalization(session, profile, accepted)
	if err != nil {
		t.Fatal(err)
	}
	return a, f
}

func TestFinalizeOrder(t *testing.T) {
	a, f := finalizeFixture(t)
	if err := f.commit(context.Background(), a.tm); !errors.Is(err, ErrFinalUnvalidated) || a.tm.calls != 0 {
		t.Fatalf("commit before validation = %v, calls = %d", err, a.tm.calls)
	}
	var buffer bytes.Buffer
	if err := f.export(&buffer, a); err != nil {
		t.Fatal(err)
	}
	if err := f.check(bytes.NewReader(buffer.Bytes()), a); err != nil {
		t.Fatal(err)
	}
	if a.tm.calls != 0 || len(a.tm.rows) != 0 || !bytes.Equal(a.observed, buffer.Bytes()) {
		t.Fatal("rows committed before exact final bytes validation")
	}
	if err := f.commit(context.Background(), a.tm); err != nil {
		t.Fatal(err)
	}
	if a.tm.calls != 1 || a.tm.active || len(a.tm.rows) != 2 || a.tm.rows[0].Text() != "訳a" || a.tm.rows[1].Text() != "訳b" ||
		!slices.Equal(a.tm.events, []string{"import", "generate a", "generate b", "export", "observe", "commit"}) {
		t.Fatalf("calls = %d, active = %v, rows = %v, events = %v", a.tm.calls, a.tm.active, a.tm.rows, a.tm.events)
	}
}

func TestFinalizeRejects(t *testing.T) {
	injected := errors.New("injected failure")
	for _, tt := range []struct {
		name   string
		change func(*fakeAdapter)
		want   error
		calls  int
	}{
		{"missing", func(a *fakeAdapter) { a.corrupt = func(i *fakeImage) { i.Records = i.Records[:1] } }, artifact.ErrFinalMismatch, 0},
		{"duplicate", func(a *fakeAdapter) { a.corrupt = func(i *fakeImage) { i.Records[1] = i.Records[0] } }, artifact.ErrFinalMismatch, 0},
		{"translation replaced", func(a *fakeAdapter) { a.corrupt = func(i *fakeImage) { i.Records[0].Text = "差替え" } }, artifact.ErrFinalMismatch, 0},
		{"preserved metadata", func(a *fakeAdapter) { a.corrupt = func(i *fakeImage) { i.Header[0] = 8 } }, artifact.ErrFinalMismatch, 0},
		{"expected metadata", func(a *fakeAdapter) { a.corrupt = func(i *fakeImage) { i.Length++ } }, artifact.ErrFinalMismatch, 0},
		{"key multiplicity", func(a *fakeAdapter) { a.corrupt = func(i *fakeImage) { i.Keys = i.Keys[:1] } }, artifact.ErrFinalMismatch, 0},
		{"target language", func(a *fakeAdapter) { a.corrupt = func(i *fakeImage) { i.Language = "en" } }, artifact.ErrFinalMismatch, 0},
		{"observe error", func(a *fakeAdapter) { a.observeErr = injected }, injected, 0},
		{"export error", func(a *fakeAdapter) { a.exportErr = injected }, injected, 0},
		{"manifest error", func(a *fakeAdapter) { a.invalidManifest = true }, artifact.ErrInvalidManifest, 0},
		{"commit error", func(a *fakeAdapter) { a.tm.err = injected }, injected, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a, f := finalizeFixture(t)
			tt.change(a)
			var buffer bytes.Buffer
			err := f.export(&buffer, a)
			if err == nil {
				err = f.check(bytes.NewReader(buffer.Bytes()), a)
			}
			if err == nil {
				err = f.commit(context.Background(), a.tm)
			}
			if !errors.Is(err, tt.want) || a.tm.calls != tt.calls || len(a.tm.rows) != 0 || a.tm.active {
				t.Fatalf("error = %v, calls = %d, rows = %v, active = %v", err, a.tm.calls, a.tm.rows, a.tm.active)
			}
		})
	}
}
