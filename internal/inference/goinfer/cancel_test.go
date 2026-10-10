package goinfer

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/sh4869221b/yakuori/internal/inference"
)

type generationOutcome struct {
	result inference.GenerationResult
	err    error
}

func generateAsync(ctx context.Context, engine *Engine, request inference.GenerationRequest) <-chan generationOutcome {
	done := make(chan generationOutcome, 1)
	go func() {
		result, err := engine.Generate(ctx, request)
		done <- generationOutcome{result: result, err: err}
	}()
	return done
}

func TestCancelDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine, model, _, request := generationFixture(t)
		defer engine.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		started, tokens := make(chan struct{}), make(chan int)
		outcomeRead := make(chan struct{})
		model.start = func(context.Context, generationInput) tokenStream {
			close(started)
			return tokenStream{tokens: tokens, outcome: func() generationState {
				close(outcomeRead)
				return generationState{budget: 2}
			}}
		}
		done := generateAsync(ctx, engine, request)
		<-started
		tokens <- 20
		cancel()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("Generate returned before stream closed")
		case <-outcomeRead:
			t.Fatal("backend state read before stream closed")
		default:
		}
		tokens <- 21
		close(tokens)
		got := <-done
		if got.result.Finish != inference.Canceled || !errors.Is(got.err, context.Canceled) || got.result.OutputTokens != 2 {
			t.Fatalf("result=%+v error=%v", got.result, got.err)
		}
	})
}

func TestReuseAfterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine, model, _, request := generationFixture(t)
		defer engine.Close()
		start := model.start
		started, tokens := make(chan struct{}), make(chan int)
		model.start = func(context.Context, generationInput) tokenStream {
			close(started)
			return tokenStream{tokens: tokens, outcome: func() generationState { return generationState{budget: 2} }}
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := generateAsync(ctx, engine, request)
		<-started
		cancel()
		close(tokens)
		got := <-done
		if got.result.Finish != inference.Canceled || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("canceled result=%+v error=%v", got.result, got.err)
		}
		model.start = start
		result, err := engine.Generate(context.Background(), request)
		if err != nil || result.Finish != inference.Stop {
			t.Fatalf("reuse result=%+v error=%v", result, err)
		}
	})
}
