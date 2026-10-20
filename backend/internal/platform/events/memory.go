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
	patterns  map[chan Message]string // pattern subscribers
	published []string
}

func NewMemoryBus() *MemoryBus {
	return &MemoryBus{subs: map[string]map[chan string]struct{}{}, patterns: map[chan Message]string{}}
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
	for ch, pattern := range b.patterns {
		if matchPrefixGlob(pattern, channel) {
			select {
			case ch <- Message{Channel: channel, Payload: string(payload)}:
			default:
			}
		}
	}
	return nil
}

// matchPrefixGlob supports exactly what the callers use: a literal name, or a literal prefix followed by "*".
func matchPrefixGlob(pattern, name string) bool {
	if n := len(pattern); n > 0 && pattern[n-1] == '*' {
		prefix := pattern[:n-1]
		return len(name) >= len(prefix) && name[:len(prefix)] == prefix
	}
	return pattern == name
}

// SubscribePattern is Subscribe for every channel matching pattern.
func (b *MemoryBus) SubscribePattern(_ context.Context, pattern string) (<-chan Message, func() error, error) {
	ch := make(chan Message, 256)
	b.mu.Lock()
	b.patterns[ch] = pattern
	b.mu.Unlock()
	var once sync.Once
	return ch, func() error {
		once.Do(func() {
			b.mu.Lock()
			delete(b.patterns, ch)
			b.mu.Unlock()
			close(ch)
		})
		return nil
	}, nil
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
