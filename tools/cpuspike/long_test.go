package main

import (
	"testing"
	"time"
)

func TestLongProbeRejectsBeforeOpeningModel(t *testing.T) {
	for _, tc := range []struct {
		target int
		mode   string
	}{{0, "stop"}, {31, "stop"}, {32767, "stop"}, {32768, "stop"}, {1024, "unknown"}} {
		if err := longProbe("does-not-exist", tc.target, tc.mode, 5*time.Minute); err == nil || err.Error() != "target must be 32..32766; mode stop or deadline" {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestLongMainRejectsInvalidFlagsBeforeOpeningModel(t *testing.T) {
	for _, args := range [][]string{
		{"--request-timeout=0s"},
		{"--request-timeout=-1s"},
		{"--request-timeout=invalid"},
		{"--request-timeout"},
		{"--unknown=1"},
		{"extra"},
		{"--request-timeout=1s", "extra"},
	} {
		if code := longMain(append([]string{"does-not-exist", "1024", "stop"}, args...)); code != 2 {
			t.Fatalf("%v: got exit %d, want usage error before model I/O", args, code)
		}
	}
}
