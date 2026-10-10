package goinfer

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/townsendmerino/goinfer/tokenizer"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

func TestConcurrentClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine, model, _, _ := generationFixture(t)
		entered, release := make(chan struct{}), make(chan struct{})
		closeErr := errors.New("backend close failed")
		model.onClose = func() error {
			close(entered)
			<-release
			return closeErr
		}
		results := make(chan error, 3)
		go func() { results <- engine.Close() }()
		<-entered
		for range 2 {
			go func() { results <- engine.Close() }()
		}
		synctest.Wait()
		select {
		case <-results:
			t.Fatal("concurrent Close returned before backend Close completed")
		default:
		}
		close(release)
		for range 3 {
			if err := <-results; !errors.Is(err, closeErr) {
				t.Fatalf("Close lost backend error: %v", err)
			}
		}
		if model.closes != 1 {
			t.Fatalf("backend closes=%d", model.closes)
		}
	})
}

func TestCloseDuringPreparation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		engine, model, tok, request := generationFixture(t)
		started, tokens := make(chan context.Context, 1), make(chan int)
		model.start = func(ctx context.Context, input generationInput) tokenStream {
			started <- ctx
			return tokenStream{tokens: tokens, outcome: func() generationState { return generationState{budget: input.max} }}
		}
		generated := generateAsync(context.Background(), engine, request)
		child := <-started
		entered, release := make(chan struct{}), make(chan struct{})
		tok.encode = func([]tokenizer.Segment, bool) ([]int, error) {
			close(entered)
			<-release
			return []int{1}, nil
		}
		prepared := make(chan error, 1)
		policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, request.Policy().MaxOutputTokens, request.Policy().RequestTimeout)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			_, err := engine.NewRequest(context.Background(), prompt.Input{Text: "hello"}, policy)
			prepared <- err
		}()
		<-entered
		closed := make(chan error, 1)
		go func() { closed <- engine.Close() }()
		<-child.Done()
		synctest.Wait()
		if _, err := engine.Info(context.Background()); !errors.Is(err, ErrClosed) || model.closes != 0 {
			t.Fatalf("closing preparation error=%v backend closes=%d", err, model.closes)
		}
		close(release)
		if err := <-prepared; err != nil {
			t.Fatalf("already admitted preparation failed: %v", err)
		}
		close(tokens)
		if got := <-generated; got.result.Finish != inference.Canceled || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("generation during preparation Close=%+v", got)
		}
		if err := <-closed; err != nil || model.closes != 1 {
			t.Fatalf("Close error=%v closes=%d", err, model.closes)
		}
	})
}
