package inference_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/inference"
)

func preparedFixture() inference.PreparedRequest {
	return inference.PreparedRequest{
		RenderedPrompt: "<control>literal<control>",
		Spans: []inference.PromptSpan{
			{Text: "<control>", Special: true}, {Text: "literal"}, {Text: "<control>", Special: true},
		},
		TokenIDs: []int{1, 2, 3},
		Identity: inference.RequestIdentity{
			ModelSHA256:  [32]byte{1},
			Tokenizer:    inference.TokenizerIdentity{ModelSHA256: [32]byte{1}, BackendPin: "backend-v1"},
			Template:     inference.TemplateIdentity{Family: "chat", Source: "template text", RendererVersion: "v1"},
			PromptSchema: 1,
		},
		StopIDs: []int{4, 5},
	}
}

func TestRequestSnapshot(t *testing.T) {
	prepared := preparedFixture()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 128, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request, err := inference.NewGenerationRequest(prepared, policy)
	if err != nil {
		t.Fatal(err)
	}
	if request.RenderedPrompt() != prepared.RenderedPrompt || !reflect.DeepEqual(request.Spans(), prepared.Spans) ||
		!slices.Equal(request.TokenIDs(), prepared.TokenIDs) || request.Identity() != prepared.Identity {
		t.Fatalf("request lost prepared data: %v", request)
	}
	var rendered string
	for _, span := range request.Spans() {
		rendered += span.Text
	}
	if rendered != request.RenderedPrompt() {
		t.Fatalf("joined spans = %q", rendered)
	}
}

func TestRequestCopies(t *testing.T) {
	prepared := preparedFixture()
	want := preparedFixture()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 128, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request, err := inference.NewGenerationRequest(prepared, policy)
	if err != nil {
		t.Fatal(err)
	}
	prepared.RenderedPrompt = "changed"
	prepared.Spans[0] = inference.PromptSpan{Text: "changed"}
	prepared.TokenIDs[0] = 999
	prepared.StopIDs[0] = 999
	prepared.Identity.Template.Source = "changed"
	policy, err = inference.NewGenerationPolicy(inference.PolicySchemaV1, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request.Spans()[1].Text = "changed getter"
	request.TokenIDs()[1] = 999
	identity := request.Identity()
	identity.ModelSHA256[0] = 9
	snapshot := request.Policy()
	snapshot.StopIDs[1] = 999
	snapshot.MaxOutputTokens = policy.MaxOutputTokens()
	if request.RenderedPrompt() != want.RenderedPrompt || !reflect.DeepEqual(request.Spans(), want.Spans) ||
		!slices.Equal(request.TokenIDs(), want.TokenIDs) || request.Identity() != want.Identity ||
		!slices.Equal(request.Policy().StopIDs, want.StopIDs) || request.Policy().MaxOutputTokens != 128 ||
		request.Policy().RequestTimeout != time.Second {
		t.Fatal("request shares mutable input or getter data")
	}
}

func TestRequestRejectsZeroPolicy(t *testing.T) {
	_, err := inference.NewGenerationRequest(preparedFixture(), inference.GenerationPolicy{})
	if !errors.Is(err, inference.ErrInvalidPolicy) {
		t.Fatalf("zero policy error = %v", err)
	}
}
