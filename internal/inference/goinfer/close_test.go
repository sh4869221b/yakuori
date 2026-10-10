package goinfer

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

func TestCloseDuringGenerate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine, model, tok, request := generationFixture(t)
		started, tokens := make(chan context.Context, 1), make(chan int)
		decodeStarted, finishDecode := make(chan struct{}), make(chan struct{})
		model.start = func(ctx context.Context, input generationInput) tokenStream {
			started <- ctx
			return tokenStream{tokens: tokens, outcome: func() generationState { return generationState{budget: input.max} }}
		}
		tok.decode = func([]int) (string, error) {
			close(decodeStarted)
			<-finishDecode
			return "partial", nil
		}
		generated := generateAsync(context.Background(), engine, request)
		child := <-started
		closed := make(chan error, 1)
		go func() { closed <- engine.Close() }()
		<-child.Done()
		synctest.Wait()
		select {
		case <-generated:
			t.Fatal("Generate returned before drain")
		case <-closed:
			t.Fatal("Close returned before drain")
		default:
		}
		if model.closes != 0 {
			t.Fatal("backend closed before drain")
		}
		close(tokens)
		<-decodeStarted
		if _, err := engine.NewRequest(context.Background(), prompt.Input{}, inference.GenerationPolicy{}); !errors.Is(err, ErrClosed) {
			t.Fatalf("NewRequest admitted while closing: %v", err)
		}
		if _, err := engine.Info(context.Background()); !errors.Is(err, ErrClosed) {
			t.Fatalf("Info while closing: %v", err)
		}
		if _, err := engine.CountTokens(context.Background(), request); !errors.Is(err, ErrClosed) {
			t.Fatalf("CountTokens while closing: %v", err)
		}
		if _, err := engine.Generate(context.Background(), request); !errors.Is(err, ErrClosed) {
			t.Fatalf("Generate admitted while closing: %v", err)
		}
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("Close returned before decode completed")
		default:
		}
		if model.closes != 0 {
			t.Fatal("backend closed before decode completed")
		}
		close(finishDecode)
		got := <-generated
		if got.result.Finish != inference.Canceled || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("result=%+v error=%v", got.result, got.err)
		}
		if err := <-closed; err != nil || model.closes != 1 {
			t.Fatalf("Close error=%v closes=%d", err, model.closes)
		}
	})
}

func TestCloseBeforeCancelClassification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine, model, _, request := generationFixture(t)
		started, tokens := make(chan struct{}), make(chan int)
		model.start = func(context.Context, generationInput) tokenStream {
			close(started)
			return tokenStream{tokens: tokens, outcome: func() generationState { return generationState{budget: 2} }}
		}
		generated := generateAsync(context.Background(), engine, request)
		<-started
		cancelStarted, finishCancel := make(chan struct{}), make(chan struct{})
		engine.mu.Lock()
		cancel := engine.activeCancel
		engine.activeCancel = func() {
			close(cancelStarted)
			<-finishCancel
			cancel()
		}
		engine.mu.Unlock()
		closed := make(chan error, 1)
		go func() { closed <- engine.Close() }()
		<-cancelStarted
		close(tokens)
		got := <-generated
		if got.result.Finish != inference.Canceled || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("closing race result=%+v error=%v", got.result, got.err)
		}
		close(finishCancel)
		if err := <-closed; err != nil || model.closes != 1 {
			t.Fatalf("Close error=%v closes=%d", err, model.closes)
		}
	})
}
