// Package events defines the propagation interfaces flag mutations publish
// through and /sdk/stream subscribes to. It depends on nothing Redis-specific
// on purpose: Redis is a propagation optimization, never a correctness
// dependency (PRD — evaluation must keep working from Postgres regardless of
// Redis's state), so the Noop implementations here are what the rest of the
// backend falls back to when Redis is unreachable, rather than a nil check
// scattered through every call site.
package events

import "context"

// Publisher fans out a flag-change notification. Implementations must not
// block the caller on a slow or unreachable broker — Publish is called
// after a transaction has already committed, so a failure here must never
// be allowed to look like the mutation itself failed.
type Publisher interface {
	Publish(ctx context.Context, channel string, payload []byte) error
}

// Subscriber receives messages published to a channel. Close must be safe to
// call exactly once and must fully release the subscription (no goroutine
// leak) — callers defer it immediately after a successful Subscribe.
type Subscriber interface {
	Subscribe(ctx context.Context, channel string) (msgs <-chan string, closeFn func() error, err error)
}

// FlagsChannel is the single source of truth for how a flag-change channel
// name is derived, so the publish side (flags package) and the subscribe side
// (stream package) can't drift out of sync. The channel carries BOTH the
// workspace id and the environment id: an environment id is already unique to
// one workspace, but a name that contains only it would let a bug (or a shared
// Redis) put two tenants on one channel. Subscriptions are always to this exact
// name, never a pattern.
func FlagsChannel(workspaceID, environmentID string) string {
	return "flags:" + workspaceID + ":" + environmentID
}

// RevocationChannel carries "this SDK key was revoked" to every API instance,
// so each drops it from its cache and closes the streams that use it at once.
// The payload is {"prefix":"hsdk_xxxxxxxx"}: the key's DISPLAY prefix, which is
// not a secret (the console shows it) and says nothing about any tenant's data.
// It is a deliberate exception to the per-workspace channel names: instances
// must hear about every workspace's revocations.
const RevocationChannel = "helios:apikey-revoked"

type NoopPublisher struct{}

func (NoopPublisher) Publish(context.Context, string, []byte) error { return nil }

type NoopSubscriber struct{}

// Subscribe always fails: with no broker configured, /sdk/stream should
// refuse immediately (503) so an SDK falls back to polling, rather than
// accept a connection that will never deliver anything.
func (NoopSubscriber) Subscribe(context.Context, string) (<-chan string, func() error, error) {
	return nil, nil, ErrUnavailable
}

type errString string

func (e errString) Error() string { return string(e) }

const ErrUnavailable = errString("events: no broker configured")
