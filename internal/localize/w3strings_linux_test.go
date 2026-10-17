//go:build linux

package localize_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
	"golang.org/x/sys/unix"
)

type w3Engine struct {
	calls int
	texts map[string]string
}

func (e *w3Engine) Generate(_ context.Context, id unit.UnitID, _ string) (localize.Generation, error) {
	e.calls++
	text, exists := e.texts[id.StableID()]
	if !exists {
		return localize.Generation{}, errors.New("unexpected generation ID")
	}
	return localize.Generation{Text: text, Finish: localize.Stop}, nil
}

type w3ObservedAdapter struct {
	*w3strings.Adapter
	t        *testing.T
	tamper   bool
	observed bool
}

func (a *w3ObservedAdapter) Observe(reader io.Reader) (artifact.FinalObservation, error) {
	a.t.Helper()
	if a.tamper {
		raw, err := io.ReadAll(reader)
		if err != nil {
			return artifact.FinalObservation{}, err
		}
		if len(raw) < 35 || raw[10] != 4 {
			a.t.Fatal("unexpected authored table layout")
		}
		before, err := a.Adapter.Observe(bytes.NewReader(raw))
		if err != nil {
			a.t.Fatal(err)
		}
		// Swap only two complete records. ID/text/key relations stay valid,
		// so only the ordered metadata can reject this stage.
		first := bytes.Clone(raw[11:23])
		copy(raw[11:23], raw[23:35])
		copy(raw[23:35], first)
		after, err := a.Adapter.Observe(bytes.NewReader(raw))
		if err != nil {
			a.t.Fatal(err)
		}
		texts := func(o artifact.FinalObservation) map[unit.UnitID]string {
			m := make(map[unit.UnitID]string)
			for _, u := range o.Units {
				m[u.ID] = u.Text
			}
			return m
		}
		if !reflect.DeepEqual(texts(before), texts(after)) || !reflect.DeepEqual(before.Keys, after.Keys) ||
			bytes.Equal(before.Metadata["decoded-id-order"], after.Metadata["decoded-id-order"]) {
			a.t.Fatal("mutation was not a valid table-order-only change")
		}
		a.observed = true
		return after, nil
	}
	observed, err := a.Adapter.Observe(reader)
	a.observed = err == nil
	return observed, err
}

type w3TM struct {
	t       *testing.T
	adapter *w3ObservedAdapter
	texts   map[string]string
	calls   int
}

func (m *w3TM) Commit(_ context.Context, _ unit.Session, _ validate.Profile, accepted []validate.AcceptedTranslation) error {
	m.calls++
	if !m.adapter.observed {
		m.t.Fatal("TM commit preceded final file observation")
	}
	got := make(map[string]string)
	for _, translation := range accepted {
		got[translation.UnitID().StableID()] = translation.Text()
	}
	if !reflect.DeepEqual(got, m.texts) {
		m.t.Fatal("TM did not receive the complete fixed translations")
	}
	return nil
}

func w3File(t *testing.T, malformed, tamper bool) (localize.Result, error, *w3Engine, *w3TM, publication.Options, []byte) {
	t.Helper()
	limits := config.DefaultLimits()
	limits.ArtifactBytes, limits.Units = 65536, 4095
	limits.UnitTextBytes, limits.TotalTextBytes, limits.Segments = 16384, 32768, 4095
	a, err := w3strings.NewWithLimits(limits)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &w3ObservedAdapter{Adapter: a, t: t, tamper: tamper}
	texts := map[string]string{
		"1001": "短い訳文😀", "1002": "", "1003": strings.Repeat("長", 2050),
		"4294967295": "  <b>{player}</b>|%s  ",
	}
	engine := &w3Engine{texts: texts}
	memory := &w3TM{t: t, adapter: adapter, texts: texts}
	core, err := localize.NewCoreWithLimits(engine, limits)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../tools/w3spike/testdata/en.w3strings")
	if err != nil {
		t.Fatal(err)
	}
	if malformed {
		raw = raw[:len(raw)-1]
	}
	dir := t.TempDir()
	options := publication.Options{Source: filepath.Join(dir, "source"), Output: filepath.Join(dir, "output"), Mode: publication.Create}
	if err := os.WriteFile(options.Source, raw, 0600); err != nil {
		t.Fatal(err)
	}

	result, err := core.File(context.Background(), options, adapter, profile, memory)

	source, readErr := os.ReadFile(options.Source)
	if readErr != nil || !bytes.Equal(source, raw) {
		t.Fatal("Core changed source bytes")
	}
	return result, err, engine, memory, options, raw
}

func w3UnsupportedFilesystem(t *testing.T, result localize.Result, err error, engine *w3Engine, memory *w3TM, options publication.Options) bool {
	t.Helper()
	var st unix.Statfs_t
	if statErr := unix.Statfs(filepath.Dir(options.Output), &st); statErr != nil {
		t.Fatal(statErr)
	}
	if st.Type == unix.EXT4_SUPER_MAGIC || st.Type == unix.BTRFS_SUPER_MAGIC {
		return false
	}
	var phase *localize.Error
	if !errors.Is(err, publication.ErrUnsupportedFilesystem) || !errors.As(err, &phase) || phase.Phase != "prepare" ||
		engine.calls != 0 || memory.calls != 0 || memory.adapter.observed || result.TMCommitted ||
		result.BytesWritten != 0 || result.Publication.State != publication.NotPublished {
		t.Fatalf("unsupported filesystem: result=%+v error=%v Generate=%d Commit=%d observed=%v", result, err, engine.calls, memory.calls, memory.adapter.observed)
	}
	if _, err := os.Lstat(options.Output); !os.IsNotExist(err) {
		t.Fatal("unsupported filesystem produced output")
	}
	t.Log("unsupported-filesystem negative coverage: ErrUnsupportedFilesystem at prepare, Generate=0 Commit=0 NotPublished; source unchanged, output absent; intended Core scenario not reached")
	return true
}

func TestW3StringsFile(t *testing.T) {
	result, err, engine, memory, options, _ := w3File(t, false, false)
	if w3UnsupportedFilesystem(t, result, err, engine, memory, options) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if result.Publication.State != publication.Published || !result.TMCommitted || memory.calls != 1 || engine.calls != 3 {
		t.Fatalf("result=%+v Generate=%d Commit=%d", result, engine.calls, memory.calls)
	}
	file, err := os.Open(options.Output)
	if err != nil {
		t.Fatal(err)
	}
	observed, observeErr := memory.adapter.Adapter.Observe(file)
	if err := errors.Join(observeErr, file.Close()); err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, u := range observed.Units {
		if u.TargetLanguage != "ja" {
			t.Fatal("published language is not Japanese")
		}
		got[u.ID.StableID()] = u.Text
	}
	if !reflect.DeepEqual(got, memory.texts) {
		t.Fatal("published file differs from fixed translations")
	}
	t.Log("actual Core.File: Generate=3, final file observed, Commit=1, Published; four fixed texts and source bytes verified")
}

func TestW3StringsFileRejectsMalformed(t *testing.T) {
	result, err, engine, memory, options, _ := w3File(t, true, false)
	if w3UnsupportedFilesystem(t, result, err, engine, memory, options) {
		return
	}
	var phase *localize.Error
	if !errors.As(err, &phase) || phase.Phase != "import" || engine.calls != 0 || memory.calls != 0 ||
		result.TMCommitted || result.Publication.State != publication.NotPublished {
		t.Fatalf("result=%+v error=%v Generate=%d Commit=%d", result, err, engine.calls, memory.calls)
	}
	if _, err := os.Lstat(options.Output); !os.IsNotExist(err) {
		t.Fatal("malformed input produced output")
	}
	t.Log("malformed import: Generate=0 Commit=0 NotPublished; source unchanged, output absent")
}

func TestW3StringsStageTamper(t *testing.T) {
	result, err, engine, memory, options, _ := w3File(t, false, true)
	if w3UnsupportedFilesystem(t, result, err, engine, memory, options) {
		return
	}
	if !errors.Is(err, artifact.ErrFinalMismatch) || !memory.adapter.observed || engine.calls != 3 ||
		memory.calls != 0 || result.TMCommitted || result.Publication.State != publication.NotPublished {
		t.Fatalf("result=%+v error=%v Generate=%d Commit=%d", result, err, engine.calls, memory.calls)
	}
	if _, err := os.Lstat(options.Output); !os.IsNotExist(err) {
		t.Fatal("tampered stage was published")
	}
	t.Log("valid table-order-only stage tamper: Observe succeeded, CompareFinal rejected, Commit=0 NotPublished; source unchanged, output absent")
}
