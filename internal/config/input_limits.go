package config

import (
	"errors"
	"fmt"
)

var ErrLimitExceeded = errors.New("operational input limit exceeded")

// LimitError identifies an exceeded bound without retaining input text.
type LimitError struct {
	Limit           string
	Actual, Maximum int
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("%s: %d exceeds %d", e.Limit, e.Actual, e.Maximum)
}
func (e *LimitError) Unwrap() error { return ErrLimitExceeded }

func checkLimit(name string, actual, maximum int) error {
	if actual > maximum {
		return &LimitError{Limit: name, Actual: actual, Maximum: maximum}
	}
	return nil
}
func (l Limits) CheckArtifactBytes(n int) error {
	return checkLimit("artifact bytes", n, l.ArtifactBytes)
}
func (l Limits) CheckUnits(n int) error { return checkLimit("units", n, l.Units) }
func (l Limits) CheckUnitTextBytes(n int) error {
	return checkLimit("unit text bytes", n, l.UnitTextBytes)
}
func (l Limits) CheckTotalTextBytes(n int) error {
	return checkLimit("total text bytes", n, l.TotalTextBytes)
}
func (l Limits) CheckSegments(n int) error { return checkLimit("segments", n, l.Segments) }
