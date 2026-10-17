//go:build offlinesmoke

package goinfer

import (
	"context"
	"fmt"
	"sync"
)

// OfflineSmokeCancelAfterToken arms a one-shot cancellation on a real decoder
// token, not on Generate entry or context setup. The caller must restore the
// backend after Generate returns, before closing or otherwise using the engine.
// This cross-package fixture is absent from ordinary production builds.
func (e *Engine) OfflineSmokeCancelAfterToken(cancel context.CancelFunc) (<-chan struct{}, func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return nil, nil, ErrClosed
	}
	if e.activeDone != nil {
		return nil, nil, ErrBusy
	}
	model, ok := e.model.(*decoderModel)
	if !ok {
		return nil, nil, fmt.Errorf("offline smoke requires a real decoder model, got %T", e.model)
	}
	observed := make(chan struct{})
	e.model = &offlineSmokeCancelModel{decoderModel: model, cancel: cancel, observed: observed}
	return observed, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.model = model
	}, nil
}

type offlineSmokeCancelModel struct {
	*decoderModel
	cancel   context.CancelFunc
	observed chan struct{}
	once     sync.Once
}

func (m *offlineSmokeCancelModel) generate(ctx context.Context, input generationInput) tokenStream {
	stream := m.decoderModel.generate(ctx, input)
	tokens := make(chan int)
	go func() {
		defer close(tokens)
		for id := range stream.tokens {
			m.once.Do(func() {
				m.cancel()
				close(m.observed)
			})
			// Forward every actual token even after cancellation. Generate must
			// drain this stream before reading the real backend outcome.
			tokens <- id
		}
	}()
	return tokenStream{tokens: tokens, outcome: stream.outcome}
}
