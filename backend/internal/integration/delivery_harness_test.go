package integration

import (
	"context"
	"encoding/base64"
	"net/netip"
	"strings"
	"sync"
	"time"

	"helios/backend/internal/controlplane/outbox"
	"helios/backend/internal/delivery"
	"helios/backend/internal/platform/mail"
	"helios/backend/internal/platform/netguard"
	"helios/backend/internal/platform/secretbox"
)

// The test key: 32 bytes of 0x42. Never a real one.
var testSecretKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("B", 32)))

// stubResolver answers every name with a public address unless told otherwise, so
// endpoint URLs validate without DNS. Connections still go through the guarded dialer.
type stubResolver struct {
	mu      sync.Mutex
	answers map[string][]netip.Addr
	calls   map[string]int
}

func newStubResolver() *stubResolver {
	return &stubResolver{answers: map[string][]netip.Addr{}, calls: map[string]int{}}
}

func (r *stubResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[host]++
	if a, ok := r.answers[host]; ok {
		return a, nil
	}
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

func (r *stubResolver) set(host string, ips ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var a []netip.Addr
	for _, ip := range ips {
		a = append(a, netip.MustParseAddr(ip))
	}
	r.answers[host] = a
}

// memMail records the messages it was asked to send (nothing leaves the process).
type memMail struct {
	mu   sync.Mutex
	msgs []mail.Message
	err  error
	mode string
}

func (m *memMail) Mode() string {
	if m.mode == "" {
		return "smtp"
	}
	return m.mode
}

func (m *memMail) Send(_ context.Context, msg mail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.msgs = append(m.msgs, msg)
	return nil
}

func (m *memMail) sent() []mail.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mail.Message{}, m.msgs...)
}

func (m *memMail) setErr(err error) {
	m.mu.Lock()
	m.err = err
	m.mu.Unlock()
}

// testDelivery is the default Deliver options of every test harness.
func testDelivery(res *stubResolver, m *memMail) delivery.Options {
	box, err := secretbox.New(testSecretKey)
	if err != nil {
		panic(err)
	}
	return delivery.Options{
		Box:        box,
		Guard:      netguard.New(netguard.Policy{Resolver: res}),
		Mail:       m,
		ConsoleURL: "https://console.example.test",
	}
}

// deliveryWorker builds a worker like the API's, on a clock the test controls.
func (h *harness) deliveryWorker(now func() time.Time, cfg outbox.Config) *outbox.Worker {
	o := h.deps.Delivery
	cfg.Now = now
	if cfg.Jitter == nil {
		cfg.Jitter = func() float64 { return 1 }
	}
	o.Outbox = cfg
	return delivery.NewWorker(h.pool, o)
}
