package goinfer

func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closing {
		done := e.closeDone
		e.mu.Unlock()
		<-done
		return e.closeErr
	}
	e.closing = true
	e.closeDone = make(chan struct{})
	cancel, done := e.activeCancel, e.activeDone
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	e.tokenizerMu.Lock()
	err := e.model.Close()
	e.tokenizerMu.Unlock()
	e.mu.Lock()
	e.closeErr = err
	close(e.closeDone)
	e.mu.Unlock()
	return err
}
