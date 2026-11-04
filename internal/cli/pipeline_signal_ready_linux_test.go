//go:build linux && pipelinefaults

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/inference"
)

type cancelOnSignalReady struct{ cancel context.CancelFunc }

func (w cancelOnSignalReady) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"event":"generate-ready"`)) {
		w.cancel()
	}
	return len(p), nil
}

func TestSignalReadyCancellation(t *testing.T) {
	t.Setenv(offlinePipelineEnv, "1")
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 8, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request, err := inference.NewGenerationRequest(inference.PreparedRequest{
		RenderedPrompt: "Hello. " + offlinePromptCanary,
	}, policy)
	if err != nil {
		t.Fatal(err)
	}
	// Cancel synchronously in the ready notification, before the worker can
	// proceed. This exercises the earliest cancellation the parent can request.
	for attempt := range 100 {
		ctx, cancel := context.WithCancel(t.Context())
		release := make(chan struct{})
		close(release)
		engine := &signalStreamEngine{
			cliPrecommitEngine: &cliPrecommitEngine{translationEngine: &translationEngine{}},
			sink:               &signalNoticeSink{encoder: json.NewEncoder(cancelOnSignalReady{cancel})},
			events:             newSignalEventLog(), release: release,
			ready: make(chan struct{}), drainWait: make(chan struct{}),
		}
		result, err := engine.Generate(ctx, request)
		cancel()
		if !errors.Is(err, context.Canceled) || result.Text != cliAuthoredTranslation || engine.events.occurrences["generate-return"] != 1 {
			t.Fatalf("attempt %d: text=%q error=%v events=%v", attempt, result.Text, err, engine.events.occurrences)
		}
	}
}
