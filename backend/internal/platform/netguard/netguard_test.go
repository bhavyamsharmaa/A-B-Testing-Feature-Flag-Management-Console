package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type fakeResolver struct {
	mu    sync.Mutex
	calls int
	// answer returns the addresses for the nth call (1-based) for host.
	answer func(n int, host string) []netip.Addr
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	a := f.answer(n, host)
	if a == nil {
		return nil, errors.New("no such host")
	}
	return a, nil
}

func addrs(ss ...string) []netip.Addr {
	var out []netip.Addr
	for _, s := range ss {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

// countingServer counts connections accepted, so "refused with no connection made" is checkable.
func countingServer(t *testing.T) (addr string, accepted *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			n.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String(), &n
}

func TestEveryBlockedRangeIsRefusedAsALiteral(t *testing.T) {
	g := New(Policy{})
	for _, host := range []string{
		"127.0.0.1", "127.255.255.254", "10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "169.254.0.1", "100.64.0.1", "100.127.255.255", "0.0.0.0", "0.1.2.3", "224.0.0.1", "239.255.255.255",
		"255.255.255.255", "240.0.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.7", "203.0.113.9",
		"[::1]", "[::]", "[fe80::1]", "[fc00::1]", "[fd12:3456:789a::1]", "[ff02::1]", "[fec0::1]", "[2001:db8::1]", "[64:ff9b::7f00:1]",
		// IPv4-mapped IPv6 of each of the above families
		"[::ffff:127.0.0.1]", "[::ffff:10.0.0.1]", "[::ffff:192.168.0.1]", "[::ffff:169.254.169.254]", "[::ffff:100.64.0.1]",
		"[::ffff:0.0.0.0]", "[::ffff:224.0.0.1]", "[::ffff:7f00:1]", "[::ffff:a00:1]", "[::ffff:a9fe:a9fe]",
		// embeddings
		"[2002:7f00:1::]", "[2002:a9fe:a9fe::1]", "[::7f00:1]", "[2001:0:4136:e378:8000:63bf:3fff:fdd2]",
	} {
		if u, err := g.ValidateURL("https://" + host + "/hook"); err == nil {
			t.Errorf("https://%s/hook was accepted (%v)", host, u)
		} else if !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: not an ErrBlocked: %v", host, err)
		}
	}
}

func TestUnusualEncodingsAndFormsAreRefused(t *testing.T) {
	g := New(Policy{})
	for _, raw := range []string{
		"https://2130706433/", "https://0x7f000001/", "https://0x7f.0.0.1/", "https://0177.0.0.1/", "https://017700000001/",
		"https://127.1/", "https://127.0.1/", "https://10.1/", "https://0/", "https://1.1.1/", "https://0xa9fea9fe/", "https://3232235777/",
		"https://[fe80::1%25eth0]/", "https://localhost/", "https://LOCALHOST/", "https://app.localhost/", "https://localhost./",
		"https://db.internal/", "https://printer.local/", "https://nas.lan/", "https://intranet/", "https://redis/",
		"http://example.com/", "ftp://example.com/", "file:///etc/passwd", "gopher://example.com/", "javascript:alert(1)", "//example.com/",
		"https://user:pw@example.com/", "https://user@example.com/", "https://:pw@example.com/",
		"https://example.com:80/", "https://example.com:22/", "https://example.com:5432/", "https://example.com:6379/", "https://example.com:0/", "https://example.com:99999/",
		"https://example.com/#frag", "https://example.com/a b", "https://example.com/a\r\nHost: evil", "https://exa mple.com/", "https://example.com\\@evil.com/",
		"", "https:///path", "https://", "https://" + strings.Repeat("a", 2100) + ".com/",
	} {
		if _, err := g.ValidateURL(raw); err == nil {
			t.Errorf("%q was accepted", raw)
		}
	}
	for _, ok := range []string{"https://example.com/hook", "https://hooks.example.com:8443/a/b?x=1", "https://93.184.216.34/h", "https://[2606:2800:220:1::1]/h", "https://sub.domain.example.co.uk/"} {
		if _, err := g.ValidateURL(ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
}

func TestADevelopmentSwitchAllowsOnlyLoopbackOverHTTP(t *testing.T) {
	g := New(Policy{AllowLocalHTTP: true})
	for _, ok := range []string{"http://127.0.0.1:9999/h", "http://localhost:3000/h", "http://[::1]:8080/h", "https://127.0.0.1:4443/h"} {
		if _, err := g.ValidateURL(ok); err != nil {
			t.Errorf("%q should be allowed in development: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://10.0.0.1/h", "http://169.254.169.254/latest/meta-data", "http://192.168.1.1:8080/", "http://example.com/h", "http://[::ffff:10.0.0.1]/", "http://db.internal/", "https://10.0.0.5:4443/"} {
		if _, err := g.ValidateURL(bad); err == nil {
			t.Errorf("%q must stay blocked even in development", bad)
		}
	}
}

func TestAHostnameThatResolvesToABlockedAddressIsRefusedWithNoConnection(t *testing.T) {
	addr, accepted := countingServer(t)
	_, port, _ := net.SplitHostPort(addr)
	for name, ips := range map[string][]netip.Addr{
		"loopback.example.com":  addrs("127.0.0.1"),
		"private.example.com":   addrs("10.1.2.3"),
		"metadata.example.com":  addrs("169.254.169.254"),
		"mapped.example.com":    addrs("::ffff:127.0.0.1"),
		"mixed.example.com":     addrs("93.184.216.34", "127.0.0.1"), // one bad record spoils the name
		"cgnat.example.com":     addrs("100.64.1.1"),
		"ula.example.com":       addrs("fd00::1"),
		"multicast.example.com": addrs("224.0.0.9"),
	} {
		ips := ips
		g := New(Policy{Resolver: &fakeResolver{answer: func(int, string) []netip.Addr { return ips }}})
		c := g.Client()
		_ = port
		req, _ := http.NewRequest("GET", "https://"+name+":8443/h", nil)
		if _, err := c.Do(req); err == nil {
			t.Errorf("%s: the request succeeded", name)
		} else if !errors.Is(err, ErrBlocked) && !strings.Contains(err.Error(), "destination not allowed") {
			t.Errorf("%s: refused for the wrong reason: %v", name, err)
		}
	}
	if accepted.Load() != 0 {
		t.Errorf("%d connections reached the server", accepted.Load())
	}
}

// DNS rebinding: the name resolves to a public address when the endpoint is
// created and to a private one when the delivery connects. The delivery must
// not connect.
func TestDNSRebindingBetweenCheckAndConnectCannotReachAnInternalAddress(t *testing.T) {
	_, accepted := countingServer(t)
	res := &fakeResolver{answer: func(n int, host string) []netip.Addr {
		if n == 1 {
			return addrs("93.184.216.34") // public, at check time
		}
		return addrs("127.0.0.1") // private, at connect time
	}}
	g := New(Policy{Resolver: res})
	if _, err := g.Check(context.Background(), "https://rebind.example.com/h"); err != nil {
		t.Fatalf("creation-time check: %v", err)
	}
	req, _ := http.NewRequest("GET", "https://rebind.example.com/h", nil)
	if _, err := g.Client().Do(req); err == nil || !strings.Contains(err.Error(), "destination not allowed") {
		t.Fatalf("delivery: %v", err)
	}
	if accepted.Load() != 0 {
		t.Fatalf("a connection was made to the rebound address")
	}
	if res.calls != 2 {
		t.Errorf("resolver calls: %d (check + one at connect, never a third)", res.calls)
	}
}

// The Control hook is the authority: even if the resolution step were bypassed,
// the address actually being connected is checked.
func TestTheDialerControlHookChecksTheAddressActuallyConnected(t *testing.T) {
	addr, accepted := countingServer(t)
	g := New(Policy{})
	d := net.Dialer{Control: g.control}
	if c, err := d.DialContext(context.Background(), "tcp", addr); err == nil {
		c.Close()
		t.Fatal("connected to loopback through the guarded dialer")
	} else if !errors.Is(err, ErrBlocked) {
		t.Fatalf("wrong error: %v", err)
	}
	if accepted.Load() != 0 {
		t.Fatal("a connection was accepted")
	}
	// and a literal blocked IP through the client
	for _, lit := range []string{"https://127.0.0.1:" + strings.Split(addr, ":")[1] + "/", "https://[::1]:8443/", "https://169.254.169.254:8443/"} {
		req, _ := http.NewRequest("GET", lit, nil)
		if _, err := g.Client().Do(req); err == nil {
			t.Errorf("%s was fetched", lit)
		}
	}
	if accepted.Load() != 0 {
		t.Fatal("a connection was accepted")
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var hits atomic.Int64
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer internal.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/secret", http.StatusFound)
	}))
	defer redirector.Close()
	g := New(Policy{AllowLocalHTTP: true}) // both are on loopback and allowed: the point is that the hop is not taken
	req, _ := http.NewRequest("GET", redirector.URL, nil)
	res, err := g.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 302 {
		t.Errorf("status %d: the redirect was followed", res.StatusCode)
	}
	if hits.Load() != 0 {
		t.Error("the redirect target was contacted")
	}
}

func TestTheClientDoesNotUseAnEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:3128")
	t.Setenv("HTTP_PROXY", "http://10.0.0.1:3128")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	res, err := New(Policy{AllowLocalHTTP: true}).Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("went through a proxy? %v", err)
	}
	res.Body.Close()
}

func TestADevelopmentReceiverOnLoopbackWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	g := New(Policy{AllowLocalHTTP: true})
	if _, err := g.Check(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	res, err := g.Client().Get(srv.URL)
	if err != nil || res.StatusCode != 204 {
		t.Fatalf("%v %v", res, err)
	}
	res.Body.Close()
	// the same server is unreachable without the development switch
	if _, err := New(Policy{}).Client().Get(srv.URL); err == nil {
		t.Error("loopback reachable without the switch")
	}
}
