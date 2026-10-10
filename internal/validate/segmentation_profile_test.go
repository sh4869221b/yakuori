package validate

import (
	"errors"
	"testing"
)

func TestSegmentationProfileSnapshot(t *testing.T) {
	digest := [32]byte{1, 2, 3}
	input := SegmentationInput{Schema: "schema-v1", BoundaryFixtures: "fixtures-v1"}
	profile, err := NewProfileWithSegmentation(digest, input)
	if err != nil {
		t.Fatal(err)
	}

	input.Schema = "changed"
	returned := profile.Segmentation()
	if returned != (SegmentationInput{Schema: "schema-v1", BoundaryFixtures: "fixtures-v1"}) {
		t.Fatalf("segmentation input = %#v", returned)
	}
	returned.BoundaryFixtures = "changed"
	if profile.Segmentation() != (SegmentationInput{Schema: "schema-v1", BoundaryFixtures: "fixtures-v1"}) {
		t.Fatalf("profile segmentation changed: %#v", profile.Segmentation())
	}
}

func TestSegmentationProfileRejectsInvalidInput(t *testing.T) {
	for _, tt := range []struct {
		name   string
		digest [32]byte
		input  SegmentationInput
	}{
		{"zero digest", [32]byte{}, SegmentationInput{Schema: "schema-v1", BoundaryFixtures: "fixtures-v1"}},
		{"empty schema", [32]byte{1}, SegmentationInput{BoundaryFixtures: "fixtures-v1"}},
		{"empty boundary fixtures", [32]byte{1}, SegmentationInput{Schema: "schema-v1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			profile, err := NewProfileWithSegmentation(tt.digest, tt.input)
			if !errors.Is(err, ErrInvalidProfile) || profile != (Profile{}) {
				t.Fatalf("profile = %#v, error = %v", profile, err)
			}
		})
	}
}

func TestSegmentationProfileBinding(t *testing.T) {
	session, ids, _ := fixture(t, "source")
	digest := [32]byte{1}
	input := SegmentationInput{Schema: "schema-v1", BoundaryFixtures: "fixtures-v1"}
	profile, err := NewProfileWithSegmentation(digest, input)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := Validate(session, ids[0], profile, "訳文")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckBinding(session, ids[0], profile, accepted); err != nil {
		t.Fatalf("matching profile binding: %v", err)
	}

	for _, tt := range []struct {
		name  string
		input SegmentationInput
	}{
		{"schema mismatch", SegmentationInput{Schema: "schema-v2", BoundaryFixtures: "fixtures-v1"}},
		{"fixture mismatch", SegmentationInput{Schema: "schema-v1", BoundaryFixtures: "fixtures-v2"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			otherProfile, err := NewProfileWithSegmentation(digest, tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if err := CheckBinding(session, ids[0], otherProfile, accepted); !errors.Is(err, ErrBindingMismatch) {
				t.Fatalf("error = %v, want %v", err, ErrBindingMismatch)
			}
		})
	}
	legacyProfile, err := NewProfile(digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckBinding(session, ids[0], legacyProfile, accepted); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("legacy profile error = %v, want %v", err, ErrBindingMismatch)
	}
}
