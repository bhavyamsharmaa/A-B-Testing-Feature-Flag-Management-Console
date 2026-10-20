package config

import (
	"strings"
	"testing"
)

func TestListenAddressPrecedenceIsListenAddrThenPortThenTheDefault(t *testing.T) {
	cases := []struct {
		name, listen, port, want string
	}{
		{"neither set: no change from before", "", "", ":8080"},
		{"PORT alone, as on Render", "", "10000", ":10000"},
		{"LISTEN_ADDR alone", "127.0.0.1:8080", "", "127.0.0.1:8080"},
		{"LISTEN_ADDR wins over PORT", "127.0.0.1:9000", "10000", "127.0.0.1:9000"},
		{"all interfaces, explicit", ":9000", "10000", ":9000"},
		{"IPv6 loopback", "[::1]:8080", "", "[::1]:8080"},
		{"a host name", "localhost:3000", "", "localhost:3000"},
		{"surrounding spaces", "  127.0.0.1:8080 ", "", "127.0.0.1:8080"},
		{"blank PORT counts as unset", "", "   ", ":8080"},
		{"blank LISTEN_ADDR counts as unset", "  ", "7000", ":7000"},
	}
	for _, c := range cases {
		got, err := ResolveListenAddr(c.listen, c.port)
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

func TestAWrongValueIsAnErrorNotAFallbackToTheNextOne(t *testing.T) {
	for _, bad := range []string{
		"8080", "127.0.0.1", "127.0.0.1:", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:-1", "127.0.0.1:80a", "127.0.0.1:080", "http://127.0.0.1:8080",
		"1.2.3.4:80:90", "exa mple:80", "bad_host:80", ":", "[::1:8080", "host%eth0:80", "-host:80", strings.Repeat("a", 300) + ":80",
	} {
		// PORT is valid: a bad LISTEN_ADDR must not quietly give way to it
		got, err := ResolveListenAddr(bad, "10000")
		if err == nil {
			t.Errorf("LISTEN_ADDR %q accepted as %q", bad, got)
		} else if !strings.Contains(err.Error(), "LISTEN_ADDR") {
			t.Errorf("LISTEN_ADDR %q: the error does not name the setting: %v", bad, err)
		}
	}
	for _, bad := range []string{"0", "65536", "eighty", "-1", "80.5", "8080;", "0x1f90", "08080"} {
		if got, err := ResolveListenAddr("", bad); err == nil {
			t.Errorf("PORT %q accepted as %q", bad, got)
		} else if !strings.Contains(err.Error(), "PORT") {
			t.Errorf("PORT %q: the error does not name the setting: %v", bad, err)
		}
	}
}

func TestLoadReadsBothFromTheEnvironment(t *testing.T) {
	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("PORT", "")
	if got, err := Load().ListenAddress(); err != nil || got != ":8080" {
		t.Errorf("neither: %q %v", got, err)
	}
	t.Setenv("PORT", "9100")
	if got, _ := Load().ListenAddress(); got != ":9100" {
		t.Errorf("PORT: %q", got)
	}
	t.Setenv("LISTEN_ADDR", "127.0.0.1:9200")
	if got, _ := Load().ListenAddress(); got != "127.0.0.1:9200" {
		t.Errorf("LISTEN_ADDR: %q", got)
	}
	t.Setenv("LISTEN_ADDR", "nonsense")
	if _, err := Load().ListenAddress(); err == nil {
		t.Error("a bad LISTEN_ADDR was accepted by Load")
	}
	if Load().Port != "9100" {
		t.Errorf("Config.Port is no longer the PORT: %q", Load().Port)
	}
}
