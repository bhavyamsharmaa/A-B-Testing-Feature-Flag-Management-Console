package events

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The channel name carries the workspace and the environment, so two tenants
// can never share one; and it is a plain exact name (no glob characters).
func TestFlagsChannelIsNamespacedByWorkspaceAndEnvironment(t *testing.T) {
	const ws, env = "aaaaaaaa-0000-4000-8000-000000000001", "3f2b6c1e-0000-4000-8000-000000000001"
	got := FlagsChannel(ws, env)
	if got != "flags:"+ws+":"+env {
		t.Errorf("FlagsChannel = %q", got)
	}
	if got == FlagsChannel("bbbbbbbb-0000-4000-8000-000000000002", env) {
		t.Error("the same environment id under two workspaces shares a channel")
	}
	if got == FlagsChannel(ws, "3f2b6c1e-0000-4000-8000-000000000002") {
		t.Error("two environments of one workspace share a channel")
	}
	if strings.ContainsAny(got, "*?[]") {
		t.Errorf("channel %q contains glob characters", got)
	}
}

func TestMemoryBusDeliversToExactChannelOnly(t *testing.T) {
	bus := NewMemoryBus()
	a, closeA, _ := bus.Subscribe(context.Background(), "flags:ws-a:env-a")
	b, closeB, _ := bus.Subscribe(context.Background(), "flags:ws-b:env-b")
	defer closeA()
	defer closeB()
	_ = bus.Publish(context.Background(), "flags:ws-a:env-a", []byte("hello"))
	select {
	case m := <-a:
		if m != "hello" {
			t.Errorf("got %q", m)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber of the channel got nothing")
	}
	select {
	case m := <-b:
		t.Errorf("another channel's subscriber got %q", m)
	case <-time.After(100 * time.Millisecond):
	}
	if err := closeA(); err != nil { // idempotent
		t.Error(err)
	}
}
