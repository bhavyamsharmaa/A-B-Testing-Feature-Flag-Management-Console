// Package redisx is the only place in the backend that imports go-redis.
// Everything else depends on the interfaces in internal/platform/events, so
// swapping or removing Redis touches this package alone.
package redisx

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Connect parses a redis:// or rediss:// connection URL — rediss:// turns on
// TLS automatically, which is what Upstash and most managed Redis providers
// require — and verifies it with a timed PING before returning. Connect
// itself never blocks startup indefinitely; callers decide whether a failure
// here is fatal (it should not be, for this backend: see package events).
func Connect(ctx context.Context, url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redisx: parse REDIS_URL: %w", err)
	}
	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redisx: ping: %w", err)
	}
	return client, nil
}

// Publisher implements events.Publisher against a live client.
type Publisher struct{ Client *redis.Client }

func (p Publisher) Publish(ctx context.Context, channel string, payload []byte) error {
	return p.Client.Publish(ctx, channel, payload).Err()
}

// Subscriber implements events.Subscriber against a live client.
type Subscriber struct{ Client *redis.Client }

// Subscribe confirms the subscription is actually live (Receive) before
// returning, so a broken connection surfaces immediately as an error rather
// than as a channel that silently never produces anything. The returned
// close function closes the underlying PubSub, which ends the forwarding
// goroutine's range loop and closes msgs — callers get a channel that closes
// cleanly on Close instead of needing to know about the PubSub at all.
func (s Subscriber) Subscribe(ctx context.Context, channel string) (<-chan string, func() error, error) {
	pubsub := s.Client.Subscribe(ctx, channel)
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, nil, fmt.Errorf("redisx: subscribe to %s: %w", channel, err)
	}

	out := make(chan string)
	go func() {
		defer close(out)
		for msg := range pubsub.Channel() {
			select {
			case out <- msg.Payload:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, pubsub.Close, nil
}
