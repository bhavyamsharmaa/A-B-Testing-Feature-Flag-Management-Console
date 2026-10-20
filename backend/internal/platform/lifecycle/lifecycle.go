// Package lifecycle is the process's shutdown signal, shared by everything that has to behave differently while the
// server is going away: readiness (flips to "not ready" first), new streams (refused), open streams (told to
// reconnect and closed) and the job endpoints (stop claiming work, finish or release what they hold).
//
// A nil *State is valid and never drains, so code and tests that do not care need no wiring.
package lifecycle

import (
	"context"
	"sync"
	"sync/atomic"
)

type State struct {
	once     sync.Once
	done     chan struct{}
	draining atomic.Bool
}

func New() *State { return &State{done: make(chan struct{})} }

// Drain starts the shutdown: from now on Draining is true and Done is closed. It is safe to call more than once.
func (s *State) Drain() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		s.draining.Store(true)
		close(s.done)
	})
}

func (s *State) Draining() bool { return s != nil && s.draining.Load() }

// Done is closed when the drain starts. For a nil State it is a nil channel: it blocks forever, which is what a select wants.
func (s *State) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.done
}

// Bind returns a context that ends when ctx does or when the drain starts. Job endpoints run under it: a run asked
// to stop claims nothing new, finishes the work it has begun and releases the rest (see scheduling.Runner and
// outbox.Worker).
func (s *State) Bind(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	if s == nil {
		return ctx, cancel
	}
	release := make(chan struct{})
	var once sync.Once
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		case <-release:
		}
	}()
	return ctx, func() { once.Do(func() { close(release) }); cancel() }
}
