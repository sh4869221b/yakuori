package contextpack

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestApproveCanonicalRecordEquality(t *testing.T) {
	input := fixture(t, "approval-reviewed")
	base := mustRead(t, input)
	reviewed := mustRead(t, bytes.Replace(input, []byte(`"id": "relief-captain"`), []byte(`"id": "relief-captain", "tags": []`), 1))
	merged, err := Approve(base.Pack, reviewed.Pack, []string{"mod-c-greeting"})
	if err != nil || !bytes.Equal(merged.Canonical, base.Canonical) {
		t.Fatalf("merging equal records changed canonical bytes: %v", err)
	}
}

func TestApproveChecksCombinedLimits(t *testing.T) {
	makePack := func(prefix string) Pack {
		p := mustRead(t, fixture(t, "minimal")).Pack
		edited := false
		for i := 0; i < maxEntries/2; i++ {
			p.World = append(p.World, World{
				ID: fmt.Sprintf("%s-%d", prefix, i), Text: "A world entry.",
				Provenance: Provenance{Method: "manual", Version: "1", Status: "candidate", UserEdited: &edited},
			})
		}
		source := "notes"
		p.World[0].Provenance.Sources = []Source{{Kind: "manual", Reference: &source}}
		return mustRead(t, encode(t, p)).Pack
	}
	base, reviewed := makePack("base"), makePack("reviewed")
	merged, err := Approve(base, reviewed, []string{"reviewed-0"})
	if merged != nil || err == nil || !strings.Contains(err.Error(), "array entries exceed limit") {
		t.Fatalf("merged oversized pack: %v", err)
	}
}
