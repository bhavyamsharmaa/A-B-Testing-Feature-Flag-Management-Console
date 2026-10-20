package integration

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The real binary, a real SIGTERM, real connections: what the platform does at a deploy. (The pieces are tested one by
// one elsewhere; this proves they are wired into cmd/api.)
func TestTheRealBinaryShutsDownOnSIGTERMInsideItsBudget(t *testing.T) {
	redisURL := os.Getenv("HELIOS_TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("HELIOS_TEST_REDIS_URL not set")
	}
	x := trackWorld(t, 4, 2*time.Second)
	h := x.h

	// a signing-key endpoint the API accepts at startup
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	jwks, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": "test",
		"x": base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))),
		"y": base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32))),
	}}})
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwks)
	}))
	defer auth.Close()

	bin := filepath.Join(t.TempDir(), "api")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/api").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	var logs bytes.Buffer
	cmd := exec.Command(bin)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "DATABASE_URL=" + h.dbURL, "SUPABASE_URL=" + auth.URL, "REDIS_URL=" + redisURL,
		"LISTEN_ADDR=" + addr, "SHUTDOWN_TIMEOUT=5s", "SHUTDOWN_READY_DELAY=300ms", "RATE_LIMIT_DISABLED=true",
		"HELIOS_ENV=development",
	}
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	base := "http://" + addr
	deadline := time.Now().Add(30 * time.Second)
	for {
		if code, _ := probe(t, base, "/readyz"); code == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the binary never became ready:\n%s", logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if code, body := probe(t, base, "/readyz"); code != 200 || !strings.Contains(body, `"database":"ok"`) || !strings.Contains(body, `"redis":"ok"`) {
		t.Fatalf("readyz: %d %s", code, body)
	}
	stream := readStream(t, base, x.sdkKeyA)
	if stream.status != 200 {
		t.Fatalf("stream: %d\n%s", stream.status, logs.String())
	}

	h.slowDown("events", "INSERT", 700*time.Millisecond)
	inflight := make(chan trackResp, 1)
	go func() {
		inflight <- trackRaw(t, base, x.sdkKeyA, []byte(`{"events":[{"id":"proc-1","name":"purchase","subjectKey":"s"}]}`))
	}()
	time.Sleep(250 * time.Millisecond)

	signalAt := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	until(t, time.Second, "readiness flips", func() bool {
		c, b := probe(t, base, "/readyz")
		return c == 503 && strings.Contains(b, "shutting_down")
	})
	select {
	case <-stream.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the open stream was not closed")
	}
	if !strings.Contains(stream.got(), "event: reconnect") {
		t.Errorf("stream was not told to reconnect: %q", stream.got())
	}
	if r := <-inflight; r.Status != 200 || r.statuses()[0] != "accepted" {
		t.Errorf("the request in flight when SIGTERM arrived: %d %s", r.Status, r.Raw)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("exit: %v\n%s", err, logs.String())
		}
	case <-time.After(6 * time.Second):
		t.Fatalf("still running 6 s after SIGTERM\n%s", logs.String())
	}
	took := time.Since(signalAt)
	if took > 5*time.Second {
		t.Errorf("exit took %v, budget 5s", took)
	}
	if !strings.Contains(logs.String(), "shutdown: drained") {
		t.Errorf("no clean-drain line in the log:\n%s", logs.String())
	}
	if h.count(`SELECT count(*) FROM events WHERE event_id = 'proc-1'`) != 1 {
		t.Error("the in-flight event was acknowledged but is not stored")
	}
	t.Logf("SIGTERM to exit: %v (budget 5s, in-flight insert 700ms)", took.Round(time.Millisecond))
}
