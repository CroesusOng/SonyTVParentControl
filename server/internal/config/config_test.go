package config

import "testing"

func TestCookiesSecureFollowsThePublicOriginNotLocalTLS(t *testing.T) {
	// Behind Cloudflare Tunnel: no local certificate, but the browser is on HTTPS, so
	// the session cookie must still be marked Secure.
	tunnel := Config{Origins: []string{"https://tv.example.com"}}
	if !tunnel.CookiesSecure() {
		t.Error("an https origin with no local cert must still produce Secure cookies")
	}
	if !tunnel.BehindProxy() {
		t.Error("https origin + no local cert is the behind-a-proxy case")
	}

	// Bringing the TV side up over plain HTTP: nothing is secure, say so.
	plain := Config{Origins: []string{"http://192.168.1.10:8443"}}
	if plain.CookiesSecure() {
		t.Error("a plain http origin must not produce Secure cookies")
	}
	if plain.BehindProxy() {
		t.Error("plain http everywhere is not the proxy case")
	}

	// Terminating TLS here directly.
	direct := Config{
		Origins:  []string{"https://tv.example.com:8443"},
		CertFile: "/c.pem", KeyFile: "/k.pem",
	}
	if !direct.CookiesSecure() || !direct.TLSEnabled() || direct.BehindProxy() {
		t.Error("local TLS should be secure cookies, TLS enabled, and not behind a proxy")
	}

	// One plain origin in the list poisons it: the cookie would be sent in the clear.
	mixed := Config{Origins: []string{"https://tv.example.com", "http://192.168.1.10:8443"}}
	if mixed.CookiesSecure() {
		t.Error("a single non-https origin must disable Secure cookies")
	}

	if (Config{}).CookiesSecure() {
		t.Error("no origins is not secure")
	}
}
