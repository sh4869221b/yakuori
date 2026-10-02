package main

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestFinishFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name               string
		err, ctxErr        error
		count, max, budget int
		clamped            bool
		want               string
	}{
		{"stop", nil, nil, 1, 8, 8, false, "Stop"},
		{"empty_stop_needs_content_validation", nil, nil, 0, 8, 8, false, "Stop"},
		{"max", nil, nil, 8, 8, 8, false, "MaxTokens"},
		{"context_clamp", nil, nil, 1, 8, 4, true, "ContextLimit"},
		{"zero_context_clamp", nil, nil, 0, 8, 0, true, "ContextLimit"},
		{"unknown_budget", nil, nil, 1, 8, 0, false, "InvalidOutput"},
		{"unknown_budget_nonzero", nil, nil, 1, 8, 7, false, "InvalidOutput"},
		{"bad_limit", nil, nil, 0, 0, 0, false, "InvalidOutput"},
		{"too_many", nil, nil, 9, 8, 8, false, "InvalidOutput"},
		{"partial_error", errors.New("injected decode error"), nil, 1, 8, 8, false, "DecodeError"},
		{"canceled", context.Canceled, nil, 1, 8, 8, false, "Canceled"},
		{"cancel_race", nil, context.Canceled, 1, 8, 8, false, "Canceled"},
		{"deadline", context.DeadlineExceeded, nil, 1, 8, 8, false, "Timeout"},
		{"deadline_race", nil, context.DeadlineExceeded, 1, 8, 8, false, "Timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := finish(tc.err, tc.ctxErr, tc.count, tc.max, tc.budget, tc.clamped); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
func TestContextBudget(t *testing.T) {
	for _, tc := range []struct {
		p, o, l int
		want    bool
	}{{1, 1, 2, true}, {32767, 1, 32768, true}, {32768, 1, 32768, false}, {32769, 1, 32768, false}, {0, 1, 8, false}, {1, 0, 8, false}, {1, 1, 0, false}, {-1, 1, 8, false}, {1, math.MaxInt, 32768, false}} {
		if got := withinContext(tc.p, tc.o, tc.l); got != tc.want {
			t.Errorf("%+v got %v", tc, got)
		}
	}
}
func TestModelGate(t *testing.T) {
	dir := t.TempDir()
	if verifyModel(dir) == nil {
		t.Fatal("directory accepted")
	}
	p := filepath.Join(dir, "wrong.gguf")
	if err := os.WriteFile(p, []byte("not a model"), 0600); err != nil {
		t.Fatal(err)
	}
	if verifyModel(p) == nil {
		t.Fatal("wrong fixture accepted")
	}
	if verifyModel(filepath.Join(dir, "missing")) == nil {
		t.Fatal("missing fixture accepted")
	}
}
