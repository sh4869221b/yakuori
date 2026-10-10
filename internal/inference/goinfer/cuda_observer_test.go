//go:build cuda && cudasmoke

package goinfer

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/inference"
)

// Each observation belongs to one serial Generate; outcome is read only after drain.
type cudaObservation struct {
	PostFirstTokenTokensPerSecond float64 `json:"post_first_token_tokens_per_second"`
	TTFTMS                        float64 `json:"ttft_ms"`
	PostFirstTokenMS              float64 `json:"post_first_token_stream_ms"`
	StreamTokens                  int     `json:"stream_tokens"`
}
type cudaObserver struct {
	modelBackend
	mu          sync.Mutex
	observation cudaObservation
	firstToken  chan struct{}
	calls       int
	started     time.Time
}

func (m *cudaObserver) generate(ctx context.Context, input generationInput) tokenStream {
	started := time.Now()
	m.mu.Lock()
	m.calls++
	if !m.started.IsZero() {
		started = m.started
		m.started = time.Time{}
	}
	signal := m.firstToken
	m.firstToken = nil
	m.mu.Unlock()
	stream := m.modelBackend.generate(ctx, input)
	forwarded := make(chan int)
	var observed cudaObservation
	go func() {
		defer close(forwarded)
		var first time.Time
		for id := range stream.tokens {
			if first.IsZero() {
				first = time.Now()
				observed.TTFTMS = float64(first.Sub(started)) / float64(time.Millisecond)
				if signal != nil {
					close(signal)
				}
			}
			observed.StreamTokens++
			forwarded <- id
		}
		if !first.IsZero() {
			observed.PostFirstTokenMS = float64(time.Since(first)) / float64(time.Millisecond)
			if observed.PostFirstTokenMS > 0 {
				observed.PostFirstTokenTokensPerSecond = float64(max(0, observed.StreamTokens-1)) / (observed.PostFirstTokenMS / 1000)
			}
		}
	}()
	return tokenStream{tokens: forwarded, outcome: func() generationState {
		state := stream.outcome()
		m.mu.Lock()
		m.observation = observed
		m.mu.Unlock()
		return state
	}}
}
func (m *cudaObserver) observe() cudaObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.observation
}
func (m *cudaObserver) notifyFirstToken() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.firstToken = make(chan struct{})
	return m.firstToken
}

func TestCUDAEvaluationObserver(t *testing.T) {
	sentinel := errors.New("backend failure")
	model := &stubModel{start: func(context.Context, generationInput) tokenStream {
		tokens := make(chan int, 2)
		tokens <- 11
		tokens <- 22
		close(tokens)
		return tokenStream{tokens: tokens, outcome: func() generationState { return generationState{budget: 3, clamped: true, err: sentinel} }}
	}}
	observer := &cudaObserver{modelBackend: model}
	signal := observer.notifyFirstToken()
	for range 2 {
		stream := observer.generate(context.Background(), generationInput{})
		var ids []int
		for id := range stream.tokens {
			ids = append(ids, id)
		}
		state := stream.outcome()
		if !slices.Equal(ids, []int{11, 22}) || state.budget != 3 || !state.clamped || !errors.Is(state.err, sentinel) {
			t.Fatalf("ids=%v state=%+v", ids, state)
		}
		observation := observer.observe()
		if observation.StreamTokens != 2 || observation.TTFTMS <= 0 || observation.PostFirstTokenMS <= 0 {
			t.Fatalf("observation=%+v", observation)
		}
	}
	select {
	case <-signal:
	default:
		t.Fatal("first token signal missing")
	}
}

// Timing begins immediately before the unchanged Engine.Generate invocation.
func cudaGenerate(ctx context.Context, engine *Engine, request inference.GenerationRequest) (inference.GenerationResult, error) {
	observer := engine.model.(*cudaObserver)
	observer.mu.Lock()
	observer.started = time.Now()
	observer.mu.Unlock()
	return engine.Generate(ctx, request)
}
