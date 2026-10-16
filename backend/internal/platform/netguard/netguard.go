// Package netguard is the outbound-request guard for webhooks: it decides which
// destinations the server may connect to on a customer's behalf, and builds an
// HTTP client that cannot be talked into connecting anywhere else.
//
// The threat is SSRF: a workspace admin supplies a URL and the server fetches
// it from inside our network, where it can reach the cloud metadata service
// (169.254.169.254), the database, Redis, or other tenants' internals. The
// defences, all of which have their own tests:
//
//   - ValidateURL (at creation, and again at delivery): https only, no
//     credentials, a bounded length, only ports 443 and 8443, no IP literal in
//     a blocked range (including IPv4-mapped IPv6), and no non-canonical
//     numeric host such as 2130706433, 0x7f000001 or 0177.0.0.1.
//   - The dialer: the hostname is resolved HERE, every address is checked, and
//     the connection is made to an address that was checked. A net.Dialer
//     Control hook re-checks the exact address being connected to, so even a
//     resolver that answers differently the second time (DNS rebinding) cannot
//     get a connection to a blocked address: there is no second resolution
//     between the check and the connect, and the connect itself is checked.
//   - No redirects are followed (a 3xx is a failed delivery): re-validating each
//     hop would work too, but "never follow" is simpler to reason about.
//   - No environment proxy (HTTP_PROXY would otherwise route around the dialer),
//     bounded connect, handshake, header and total timeouts, and a response
//     read cap.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Limits.
const (
	MaxURLLength   = 2048
	DialTimeout    = 5 * time.Second
	TotalTimeout   = 10 * time.Second
	MaxResponseCap = 64 << 10
)

// Resolver looks a host up. The real one is net.DefaultResolver; tests inject
// one that lies (rebinding).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Policy configures a Guard.
type Policy struct {
	// AllowLocalHTTP lets plain http and loopback destinations through, on any
	// port: a development switch for receivers on 127.0.0.1. Nothing else is
	// relaxed (private ranges and metadata stay blocked).
	AllowLocalHTTP bool
	Resolver       Resolver
}

// Guard validates URLs and builds clients under one Policy.
type Guard struct {
	p        Policy
	resolver Resolver
}

func New(p Policy) *Guard {
	r := p.Resolver
	if r == nil {
		r = net.DefaultResolver
	}
	return &Guard{p: p, resolver: r}
}

// ErrBlocked is returned (wrapped) for every refused destination.
var ErrBlocked = errors.New("destination not allowed")

func blocked(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBlocked, fmt.Sprintf(format, a...))
}

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // "this" network, unspecified
		"10.0.0.0/8",      // RFC 1918
		"100.64.0.0/10",   // CGNAT
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local, cloud metadata
		"172.16.0.0/12",   // RFC 1918
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"192.88.99.0/24",  // 6to4 relay (deprecated)
		"192.168.0.0/16",  // RFC 1918
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"224.0.0.0/4",     // multicast
		"240.0.0.0/4",     // reserved, includes broadcast
		"::/128",          // unspecified
		"::1/128",         // loopback
		"64:ff9b::/96",    // NAT64 (embeds an IPv4 address we cannot vouch for)
		"100::/64",        // discard-only
		"2001:db8::/32",   // documentation
		"fc00::/7",        // unique local
		"fe80::/10",       // link-local
		"fec0::/10",       // site-local (deprecated)
		"ff00::/8",        // multicast
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// Blocked reports why an address may not be connected to ("" if it may). An
// IPv4-mapped IPv6 address (::ffff:10.0.0.1) and IPv4-compatible/6to4/Teredo
// style embeddings are judged by the IPv4 address they carry.
func Blocked(ip netip.Addr) string {
	if !ip.IsValid() {
		return "invalid address"
	}
	ip = ip.WithZone("")
	if ip.Is4In6() {
		ip = ip.Unmap()
	}
	if ip.Is6() {
		b := ip.As16()
		// 6to4 (2002::/16) embeds an IPv4 address in bytes 2-5.
		if b[0] == 0x20 && b[1] == 0x02 {
			if why := Blocked(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]})); why != "" {
				return "6to4 address embedding " + why
			}
		}
		// IPv4-compatible (::a.b.c.d, deprecated) in the first 96 bits zero.
		allZero := true
		for _, x := range b[:12] {
			if x != 0 {
				allZero = false
			}
		}
		if allZero {
			if why := Blocked(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})); why != "" {
				return "IPv4-compatible address embedding " + why
			}
		}
		// Teredo (2001::/32) is a tunnel: never a legitimate webhook host.
		if b[0] == 0x20 && b[1] == 0x01 && b[2] == 0 && b[3] == 0 {
			return "Teredo tunnel address"
		}
	}
	switch {
	case ip.IsLoopback():
		return "loopback address"
	case ip.IsUnspecified():
		return "unspecified address"
	case ip.IsMulticast(), ip.IsInterfaceLocalMulticast(), ip.IsLinkLocalMulticast():
		return "multicast address"
	case ip.IsLinkLocalUnicast():
		return "link-local address"
	case ip.IsPrivate():
		return "private address"
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return "address in " + p.String()
		}
	}
	return ""
}

func isLoopback(ip netip.Addr) bool {
	ip = ip.WithZone("")
	if ip.Is4In6() {
		ip = ip.Unmap()
	}
	return ip.IsLoopback()
}

// AllowedPorts are the only ports a webhook may use.
func allowedPort(p string, local bool) bool {
	if local {
		return true
	}
	return p == "443" || p == "8443"
}

// looksNumeric: a host made only of digits, dots and hex markers is some IP
// spelling (decimal, hex, octal, short form). The system resolver would turn
// these into addresses, so they are refused unless they are a canonical IP
// literal we can judge ourselves.
func looksNumeric(host string) bool {
	h := strings.ToLower(host)
	if h == "" {
		return false
	}
	for _, r := range h {
		switch {
		case r >= '0' && r <= '9', r == '.', r == 'x', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	// A pure-hex-letter name like "bad" or "face.b00c" is a DNS name; the numeric
	// forms always start with a digit in some label.
	for _, label := range strings.Split(h, ".") {
		if label != "" && label[0] >= '0' && label[0] <= '9' {
			return true
		}
	}
	return false
}

// ValidateURL checks everything that can be known without the network.
func (g *Guard) ValidateURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > MaxURLLength {
		return nil, blocked("the URL must be 1 to %d characters", MaxURLLength)
	}
	if strings.ContainsAny(raw, "\r\n\t \\") {
		return nil, blocked("the URL contains whitespace or a backslash")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, blocked("the URL is not valid")
	}
	if u.User != nil {
		return nil, blocked("credentials in the URL are not allowed")
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return nil, blocked("a fragment is not allowed")
	}
	host := u.Hostname()
	if host == "" {
		return nil, blocked("the URL has no host")
	}
	local := false
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return nil, blocked("IPv6 zones are not allowed")
		}
		if why := Blocked(ip); why != "" {
			if !(g.p.AllowLocalHTTP && isLoopback(ip)) {
				return nil, blocked("%s", why)
			}
		}
		local = isLoopback(ip)
	} else {
		if strings.Contains(host, "%") || strings.Contains(host, ":") {
			return nil, blocked("the host is not valid")
		}
		if looksNumeric(host) {
			return nil, blocked("the host is a numeric address in an unusual form")
		}
		lh := strings.TrimSuffix(strings.ToLower(host), ".")
		if lh == "localhost" || strings.HasSuffix(lh, ".localhost") {
			if !g.p.AllowLocalHTTP {
				return nil, blocked("localhost is not allowed")
			}
			local = true
		}
		for _, suf := range []string{".local", ".internal", ".lan", ".home.arpa", ".intranet", ".corp"} {
			if strings.HasSuffix(lh, suf) {
				return nil, blocked("internal hostnames are not allowed")
			}
		}
		if !strings.Contains(lh, ".") && !local {
			return nil, blocked("the host must be a fully qualified domain name")
		}
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !(g.p.AllowLocalHTTP && local) {
			return nil, blocked("only https is allowed")
		}
	default:
		return nil, blocked("only https is allowed")
	}
	if port := u.Port(); port != "" {
		if !allowedPort(port, g.p.AllowLocalHTTP && local) {
			return nil, blocked("only ports 443 and 8443 are allowed")
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, blocked("the port is not valid")
		}
	}
	return u, nil
}

// resolve returns the addresses of host that are allowed, or an error if the
// host has none (or any blocked one: a name that points partly inside is refused
// whole, so a round-robin cannot hide a private record among public ones).
func (g *Guard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if why := Blocked(ip); why != "" && !(g.p.AllowLocalHTTP && isLoopback(ip)) {
			return nil, blocked("%s", why)
		}
		return []netip.Addr{ip}, nil
	}
	addrs, err := g.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return nil, errors.New("the host could not be resolved")
	}
	for _, a := range addrs {
		if why := Blocked(a); why != "" && !(g.p.AllowLocalHTTP && isLoopback(a)) {
			return nil, blocked("the host resolves to a %s", why)
		}
	}
	return addrs, nil
}

// Check is ValidateURL plus a resolution: what creation does, and what delivery
// does again before connecting (a hostname may later point somewhere else).
func (g *Guard) Check(ctx context.Context, raw string) (*url.URL, error) {
	u, err := g.ValidateURL(raw)
	if err != nil {
		return nil, err
	}
	if _, err := g.resolve(ctx, u.Hostname()); err != nil {
		return nil, err
	}
	return u, nil
}

// control runs inside the kernel connect path with the numeric address about to
// be connected: the last and authoritative check.
func (g *Guard) control(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return blocked("bad address")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return blocked("bad address")
	}
	if why := Blocked(ip); why != "" && !(g.p.AllowLocalHTTP && isLoopback(ip)) {
		return blocked("%s", why)
	}
	return nil
}

// DialContext resolves, checks and connects. The address dialled is the one
// that was checked (a literal IP), never re-resolved.
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, blocked("bad address")
	}
	addrs, err := g.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: DialTimeout, Control: g.control}
	var last error
	for _, a := range addrs {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(a.WithZone("").String(), port))
		if err == nil {
			return c, nil
		}
		last = err
		if errors.Is(err, ErrBlocked) {
			return nil, err
		}
	}
	return nil, last
}

// Client returns an HTTP client for one delivery: guarded dialer, no proxy, no
// redirects, bounded timeouts.
func (g *Guard) Client() *http.Client {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           g.DialContext,
		TLSHandshakeTimeout:   DialTimeout,
		ResponseHeaderTimeout: TotalTimeout,
		MaxIdleConns:          0,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     false,
	}
	return &http.Client{
		Transport:     tr,
		Timeout:       TotalTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
