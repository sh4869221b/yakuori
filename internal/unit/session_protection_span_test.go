package unit

import (
	"reflect"
	"testing"
)

func TestSessionCopiesProtectionSpans(t *testing.T) {
	// Given
	id, err := NewUnitID("fixture", "v1", "session-protected")
	if err != nil {
		t.Fatal(err)
	}
	want := []ProtectionSpan{
		{Start: 0, End: 3, Kind: OpenTag, Pair: "b", Ordered: true},
		{Start: 4, End: 8, Kind: CloseTag, Pair: "b", Ordered: true},
	}
	u, err := NewTranslationUnitWithProtection(id, []byte("<b>x</b>"), "en", "ja", nil, want)
	if err != nil {
		t.Fatal(err)
	}
	units := []TranslationUnit{u}

	// When
	session, err := NewSession(nil, units)
	if err != nil {
		t.Fatal(err)
	}
	units[0].protectionSpans[0].Start = 1

	// Then
	got, err := session.Unit(id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.ProtectionSpans(), want) {
		t.Fatalf("session spans changed through caller unit: %#v", got.ProtectionSpans())
	}
}
