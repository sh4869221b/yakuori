package inference_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/inference"
)

func TestPolicySnapshot(t *testing.T) {
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 128, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Schema() != inference.PolicySchemaV1 || policy.MaxOutputTokens() != 128 || policy.RequestTimeout() != time.Second {
		t.Fatalf("policy = %v", policy)
	}
	request, err := inference.NewGenerationRequest(preparedFixture(), policy)
	if err != nil {
		t.Fatal(err)
	}
	want := inference.EffectivePolicy{
		Schema: inference.PolicySchemaV1, MaxOutputTokens: 128, RequestTimeout: time.Second,
		Temperature: 0, TopK: 0, TopP: 0, MinP: 0, Seed: 0, SeedFixed: true,
		RepeatPenalty: 0, PresencePenalty: 0, FrequencyPenalty: 0, RepeatLastN: 0,
		LogitBiasEnabled: false, Logprobs: false, TopLogprobs: 0,
		LogitProcessorEnabled: false, LogitProcessorGateEnabled: false,
		SpeculationEnabled: false, SessionEnabled: false, BatchingEnabled: false,
		StopIDs: []int{4, 5},
	}
	if got := request.Policy(); !reflect.DeepEqual(got, want) {
		t.Fatalf("effective policy = %+v, want %+v", got, want)
	}
}

func TestInvalidPolicy(t *testing.T) {
	cases := []struct {
		name    string
		schema  int
		output  int
		timeout time.Duration
	}{
		{"zero output", 1, 0, time.Second},
		{"negative output", 1, -1, time.Second},
		{"zero timeout", 1, 128, 0},
		{"negative timeout", 1, 128, -time.Second},
		{"zero schema", 0, 128, time.Second},
		{"unknown schema", 2, 128, time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := inference.NewGenerationPolicy(tc.schema, tc.output, tc.timeout)
			if !errors.Is(err, inference.ErrInvalidPolicy) {
				t.Fatalf("invalid policy error = %v", err)
			}
		})
	}
}
