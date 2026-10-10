package goinfer

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/sh4869221b/yakuori/internal/inference"
)

func TestGenerateBusy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine, model, _, request := generationFixture(t)
		defer engine.Close()
		started, tokens := make(chan struct{}), make(chan int)
		calls := 0
		model.start = func(context.Context, generationInput) tokenStream {
			calls++
			close(started)
			return tokenStream{tokens: tokens, outcome: func() generationState { return generationState{budget: 2} }}
		}
		done := generateAsync(context.Background(), engine, request)
		<-started
		if _, err := engine.Generate(context.Background(), request); !errors.Is(err, ErrBusy) || calls != 1 {
			t.Fatalf("second Generate error=%v calls=%d", err, calls)
		}
		close(tokens)
		got := <-done
		if got.err != nil || got.result.Finish != inference.Stop {
			t.Fatalf("first Generate=%+v", got)
		}
	})
}

func TestUseAfterClose(t *testing.T) {
	engine, _, _, request := generationFixture(t)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Generate(context.Background(), request); !errors.Is(err, ErrClosed) {
		t.Fatalf("Generate after Close=%v", err)
	}
}

func TestGenerateForeignRequest(t *testing.T) {
	engine, model, _, _ := generationFixture(t)
	defer engine.Close()
	model.start = func(context.Context, generationInput) tokenStream {
		t.Fatal("foreign request reached backend")
		return tokenStream{}
	}
	if _, err := engine.Generate(context.Background(), inference.GenerationRequest{}); !errors.Is(err, ErrForeignRequest) {
		t.Fatalf("foreign request error=%v", err)
	}
}
