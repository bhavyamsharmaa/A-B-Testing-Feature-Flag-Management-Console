package lifecycle

import (
	"context"
	"testing"
	"time"
)

func TestANilStateNeverDrains(t *testing.T) {
	var s *State
	s.Drain() // no panic
	if s.Draining() {
		t.Error("a nil state is draining")
	}
	select {
	case <-s.Done():
		t.Error("a nil state's Done fired")
	case <-time.After(20 * time.Millisecond):
	}
	ctx, cancel := s.Bind(context.Background())
	defer cancel()
	if ctx.Err() != nil {
		t.Error("bound to a nil state and already done")
	}
}

func TestDrainIsOnceAndEndsBoundContexts(t *testing.T) {
	s := New()
	if s.Draining() {
		t.Fatal("draining at birth")
	}
	ctx, cancel := s.Bind(context.Background())
	defer cancel()
	other, cancelOther := s.Bind(context.Background())
	cancelOther() // a context released early must not leak or fire anything
	s.Drain()
	s.Drain()
	if !s.Draining() {
		t.Error("not draining after Drain")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the bound context did not end when the drain began")
	}
	_ = other
	select {
	case <-s.Done():
	default:
		t.Error("Done is not closed")
	}
}

func TestABoundContextStillEndsWithItsParent(t *testing.T) {
	s := New()
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, cancel := s.Bind(parent)
	defer cancel()
	cancelParent()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("did not follow its parent")
	}
	if s.Draining() {
		t.Error("the parent ending started a drain")
	}
}
