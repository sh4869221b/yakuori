package goinfer

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

func TestGenerateDeadline(t *testing.T) {
	for _, callerEarlier := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			engine, model, _, _ := generationFixture(t)
			defer engine.Close()
			policyTimeout, callerTimeout := 5*time.Second, 15*time.Second
			if callerEarlier {
				callerTimeout = 3 * time.Second
			}
			policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 2, policyTimeout)
			if err != nil {
				t.Fatal(err)
			}
			request, err := engine.NewRequest(context.Background(), prompt.Input{Text: "hello"}, policy)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), callerTimeout)
			defer cancel()
			want := time.Now().Add(policyTimeout)
			if callerEarlier {
				want, _ = ctx.Deadline()
			}
			model.start = func(child context.Context, input generationInput) tokenStream {
				deadline, _ := child.Deadline()
				if deadline != want {
					t.Fatalf("backend deadline=%v want=%v", deadline, want)
				}
				tokens := make(chan int)
				go func() {
					<-child.Done()
					close(tokens)
				}()
				return tokenStream{tokens: tokens, outcome: func() generationState { return generationState{budget: input.max} }}
			}
			result, err := engine.Generate(ctx, request)
			if result.Finish != inference.Timeout || !errors.Is(err, context.DeadlineExceeded) || result.Deadline != want || result.Policy.RequestTimeout != policyTimeout {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestGenerateAlreadyCanceled(t *testing.T) {
	engine, model, _, request := generationFixture(t)
	defer engine.Close()
	calls := 0
	model.start = func(context.Context, generationInput) tokenStream {
		calls++
		t.Fatal("backend called for canceled context")
		return tokenStream{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := engine.Generate(ctx, request)
	if !errors.Is(err, context.Canceled) || result.Finish != inference.Canceled || calls != 0 {
		t.Fatalf("result=%+v error=%v calls=%d", result, err, calls)
	}
}
