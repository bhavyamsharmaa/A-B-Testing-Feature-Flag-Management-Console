package config

import "testing"

func TestTrustedProxyHopsDefaultsToOneOnRenderOnly(t *testing.T) {
	for name, tc := range map[string]struct{ explicit, render, want string }{
		"nothing set":             {"", "", ""},
		"on Render":               {"", "true", "1"},
		"explicit wins on Render": {"2", "true", "2"},
		"explicit zero wins":      {"0", "true", "0"},
		"explicit elsewhere":      {"1", "", "1"},
		"RENDER is not true":      {"", "false", ""},
	} {
		t.Setenv("TRUSTED_PROXY_HOPS", tc.explicit)
		t.Setenv("RENDER", tc.render)
		if got := Load().TrustedProxyHops; got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
