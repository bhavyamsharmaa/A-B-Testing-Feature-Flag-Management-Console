package events

import (
	"context"
	"sync"
)

// MemoryBus is an in-process Publisher and Subscriber with the same exact-name
// semantics as Redis pub/sub. It exists so tests can exercise /sdk/stream and
// flag-change fan-out without Redis; production uses internal/platform/redisx.
type MemoryBus struct {
	mu        sync.Mutex
	subs      map[string]map[chan string]struct{}
	published []string
}

func NewMemoryBus() *MemoryBus {
	return &MemoryBus{subs: map[string]map[chan string]struct{}{}}
}

func (b *MemoryBus) Publish(_ context.Context, channel string, payload []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.published = append(b.published, channel)
	for ch := range b.subs[channel] { // exact name only
		select {
		case ch <- string(payload):
		default: // slow subscriber: drop, like a lossy broker
		}
	}
	return nil
}

func (b *MemoryBus) Subscribe(_ context.Context, channel string) (<-chan string, func() error, error) {
	ch := make(chan string, 16)
	b.mu.Lock()
	if b.subs[channel] == nil {
		b.subs[channel] = map[chan string]struct{}{}
	}
	b.subs[channel][ch] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return ch, func() error {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs[channel], ch)
			b.mu.Unlock()
			close(ch)
		})
		return nil
	}, nil
}

// Published returns every channel name published to so far, in order.
func (b *MemoryBus) Published() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.published...)
}

// Subscribers returns the channel names that currently have a subscriber.
func (b *MemoryBus) Subscribers() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var names []string
	for name, set := range b.subs {
		if len(set) > 0 {
			names = append(names, name)
		}
	}
	return names
}
