package goinfer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/townsendmerino/goinfer/tokenizer"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

func TestPrepareRequestFailure(t *testing.T) {
	config, load, _, tok := loadFixture(t)
	engine, err := open(context.Background(), config, load)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	input := prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: "Hello"}
	calls := 0
	encodeErr := errors.New("encode failed")
	tok.encode = func([]tokenizer.Segment, bool) ([]int, error) {
		calls++
		return nil, encodeErr
	}
	if _, err := engine.NewRequest(context.Background(), input, inference.GenerationPolicy{}); !errors.Is(err, inference.ErrInvalidPolicy) {
		t.Fatalf("zero policy error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := engine.NewRequest(ctx, input, policy); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("canceled preparation error=%v calls=%d", err, calls)
	}
	if _, err := engine.NewRequest(context.Background(), input, policy); !errors.Is(err, encodeErr) {
		t.Fatalf("encode error=%v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	tok.encode = func([]tokenizer.Segment, bool) ([]int, error) {
		cancel()
		return []int{1}, nil
	}
	if _, err := engine.NewRequest(ctx, input, policy); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel during encode error=%v", err)
	}
}

func TestRequestAfterClose(t *testing.T) {
	config, load, _, _ := loadFixture(t)
	engine, err := open(context.Background(), config, load)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request, err := engine.NewRequest(context.Background(), prompt.Input{Text: "hello"}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.NewRequest(context.Background(), prompt.Input{}, policy); !errors.Is(err, ErrClosed) {
		t.Fatalf("NewRequest after Close error=%v", err)
	}
	if _, err := engine.CountTokens(context.Background(), request); !errors.Is(err, ErrClosed) {
		t.Fatalf("CountTokens after Close error=%v", err)
	}
}
