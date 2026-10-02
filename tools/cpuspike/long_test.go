package main

import "testing"

func TestLongProbeRejectsBeforeOpeningModel(t *testing.T) {
	for _, tc := range []struct {
		target int
		mode   string
	}{{0, "stop"}, {31, "stop"}, {32767, "stop"}, {32768, "stop"}, {1024, "unknown"}} {
		if err := longProbe("does-not-exist", tc.target, tc.mode); err == nil || err.Error() != "target must be 32..32766; mode stop or deadline" {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}
