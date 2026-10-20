package apikey

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A verifier whose key check is replaced: no database, no hashing, only the coordination under test.
func testVerifier(fn func(ctx context.Context, plaintext string) (Scope, error)) *Verifier {
	v := NewVerifier(nil, time.Hour)
	v.verifyFn = fn
	return v
}

const testKey = "hlt_sdk_abcdef0123456789_secretsecretsecretsecretsecretsecret"

func TestConcurrentMissesForOneKeyVerifyItOnce(t *testing.T) {
	var runs atomic.Int64
	v := testVerifier(func(ctx context.Context, plaintext string) (Scope, error) {
		runs.Add(1)
		time.Sleep(60 * time.Millisecond) // the Argon2id hash
		return Scope{WorkspaceID: "w", EnvironmentID: "e", Prefix: "p"}, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sc, err := v.scopeFor(context.Background(), testKey)
			if err != nil || sc.WorkspaceID != "w" {
				t.Errorf("%+v %v", sc, err)
			}
		}()
	}
	wg.Wait()
	if n := runs.Load(); n != 1 {
		t.Fatalf("%d verifications for 100 concurrent requests, want 1 (each is 64 MiB of Argon2id memory)", n)
	}
	// and the answer is cached afterwards
	if _, err := v.scopeFor(context.Background(), testKey); err != nil || runs.Load() != 1 {
		t.Errorf("not cached: runs %d err %v", runs.Load(), err)
	}
}

func TestDifferentKeysAreVerifiedIndependently(t *testing.T) {
	var runs atomic.Int64
	v := testVerifier(func(ctx context.Context, plaintext string) (Scope, error) {
		runs.Add(1)
		time.Sleep(20 * time.Millisecond)
		return Scope{Prefix: plaintext}, nil
	})
	var wg sync.WaitGroup
	for _, k := range []string{testKey + "1", testKey + "2", testKey + "3"} {
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = v.scopeFor(context.Background(), k) }()
		}
	}
	wg.Wait()
	if runs.Load() != 3 {
		t.Errorf("%d verifications for 3 keys", runs.Load())
	}
}

func TestAFailedVerificationIsSharedButNeverCached(t *testing.T) {
	var runs atomic.Int64
	v := testVerifier(func(ctx context.Context, plaintext string) (Scope, error) {
		runs.Add(1)
		time.Sleep(30 * time.Millisecond)
		return Scope{}, errInvalidKey
	})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.scopeFor(context.Background(), testKey); err != errInvalidKey {
				t.Errorf("err %v", err)
			}
		}()
	}
	wg.Wait()
	first := runs.Load()
	_, _ = v.scopeFor(context.Background(), testKey)
	if first != 1 || runs.Load() != 2 {
		t.Errorf("shared failures: %d runs, then %d after another try (a failure must not be cached)", first, runs.Load())
	}
}

// A key revoked while it is being verified must not come back into the cache, and a request that arrives after the
// revocation must not join the verification that began before it.
func TestARevocationDuringVerificationIsNotUndone(t *testing.T) {
	gate := make(chan struct{})
	var runs atomic.Int64
	v := testVerifier(func(ctx context.Context, plaintext string) (Scope, error) {
		n := runs.Add(1)
		if n == 1 {
			<-gate // the first verification read the row before the revocation and finishes after it
			return Scope{WorkspaceID: "w", Prefix: "p"}, nil
		}
		return Scope{}, errInvalidKey // the key is revoked by the time anyone verifies again
	})
	first := make(chan error, 1)
	go func() { _, err := v.scopeFor(context.Background(), testKey); first <- err }()
	for runs.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	v.Invalidate("p") // the revocation is announced
	// a request after the revocation starts its own verification instead of joining the old one
	if _, err := v.scopeFor(context.Background(), testKey); err != errInvalidKey {
		t.Errorf("a request after the revocation got %v", err)
	}
	close(gate)
	<-first
	if _, ok := v.cached(testKey); ok {
		t.Error("the verification that began before the revocation put the revoked key back in the cache")
	}
}

func TestACallerThatGivesUpDoesNotFailTheOthersWaiting(t *testing.T) {
	v := testVerifier(func(ctx context.Context, plaintext string) (Scope, error) {
		time.Sleep(80 * time.Millisecond)
		return Scope{WorkspaceID: "w"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := v.scopeFor(ctx, testKey); first <- err }()
	time.Sleep(10 * time.Millisecond)
	second := make(chan error, 1)
	go func() { _, err := v.scopeFor(context.Background(), testKey); second <- err }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-first; err == nil {
		t.Error("the cancelled caller got an answer")
	}
	if err := <-second; err != nil {
		t.Errorf("the other caller failed: %v", err)
	}
}
