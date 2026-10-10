package config

import (
	"errors"
	"time"
)

var ErrInvalidLimits = errors.New("invalid operational limits")

// Limits bounds one artifact. Byte counts are UTF-8 bytes; Segments includes
// pieces that need no generation. Durations are cooperative operation budgets.
type Limits struct {
	ArtifactBytes      int
	Units              int
	UnitTextBytes      int
	TotalTextBytes     int
	Segments           int
	ContextTokens      int
	MaxOutputTokens    int
	RequestTimeout     time.Duration
	GenerationTimeout  time.Duration
	DBOperationTimeout time.Duration
	DBCleanupTimeout   time.Duration
}

// DefaultLimits is the measured candidate A adopted for Index-Translate-2B.
// Retry remains disabled by the generation contract.
func DefaultLimits() Limits {
	return Limits{
		ArtifactBytes: 1495, Units: 76, UnitTextBytes: 980,
		TotalTextBytes: 1036, Segments: 76,
		ContextTokens: 4096, MaxOutputTokens: 2048,
		RequestTimeout: 30 * time.Second, GenerationTimeout: 300 * time.Second,
		DBOperationTimeout: time.Second, DBCleanupTimeout: time.Second,
	}
}

func (l Limits) Check() error {
	if l.ArtifactBytes <= 0 || l.Units <= 0 || l.UnitTextBytes <= 0 || l.TotalTextBytes <= 0 || l.Segments <= 0 ||
		l.ContextTokens <= 0 || l.MaxOutputTokens <= 0 || l.ContextTokens <= l.MaxOutputTokens ||
		l.RequestTimeout <= 0 || l.GenerationTimeout <= 0 || l.DBOperationTimeout <= 0 || l.DBCleanupTimeout <= 0 {
		return ErrInvalidLimits
	}
	return nil
}
