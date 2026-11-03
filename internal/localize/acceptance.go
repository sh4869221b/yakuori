package localize

import (
	"time"

	"github.com/sh4869221b/yakuori/internal/validate"
)

// AcceptanceObservation is populated only during an explicitly observed run.
// Text is private evidence and must not be included in ordinary diagnostics.
type AcceptanceObservation struct {
	SourceSHA256         [32]byte
	Units                []AcceptanceUnit
	Keys                 []AcceptanceKey
	GenerateCalls        int
	GenerateCompleted    int
	GenerateFailed       int
	GenerationDuration   time.Duration
	Validation           ValidationCounts
	TMValidation         ValidationCounts
	FinalValidated       bool
	TMVerificationStatus string
	TMVerifiedUnits      int
}

// ValidationCounts counts actual Validate calls, independently of final unit
// acceptance. A rejected TM candidate may still be replaced by a valid generation.
type ValidationCounts struct {
	Attempted int `json:"attempted"`
	Passed    int `json:"passed"`
	Failed    int `json:"failed"`
}

func (v *ValidationCounts) record(err error) {
	v.Attempted++
	if err == nil {
		v.Passed++
	} else {
		v.Failed++
	}
}

type AcceptanceKey struct {
	ID  string `json:"id"`
	Key []byte `json:"key"`
}

type AcceptanceUnit struct {
	ID     string                `json:"id"`
	Text   string                `json:"text"`
	Kind   string                `json:"kind"`
	Review []validate.ReviewCode `json:"review"`
}

func (c Core) observeAccepted(a validate.AcceptedTranslation, kind string) {
	if c.observation == nil {
		return
	}
	u := AcceptanceUnit{ID: a.UnitID().StableID(), Text: a.Text(), Kind: kind}
	for _, f := range a.ReviewFindings() {
		u.Review = append(u.Review, f.Code)
	}
	c.observation.Units = append(c.observation.Units, u)
}
