// Package config reads the handful of environment variables the daemon needs.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	DataDir   string
	Listen    string
	CertFile  string
	KeyFile   string
	RPID      string
	Origins   []string
	SetupCode string
	Zone      *time.Location
}

func (c Config) TLSEnabled() bool { return c.CertFile != "" && c.KeyFile != "" }

// CookiesSecure reports whether session cookies should carry the Secure flag.
//
// It keys off the public origin, not off local TLS. Behind Cloudflare Tunnel or any
// reverse proxy, this process serves plain HTTP while the browser is on HTTPS — judging
// by TLSEnabled() there would drop the Secure flag on a cookie that travels over HTTPS
// the whole way the browser can see.
func (c Config) CookiesSecure() bool {
	if len(c.Origins) == 0 {
		return false
	}
	for _, origin := range c.Origins {
		if !strings.HasPrefix(origin, "https://") {
			return false
		}
	}
	return true
}

// BehindProxy is true when TLS is terminated somewhere in front of this process.
func (c Config) BehindProxy() bool { return !c.TLSEnabled() && c.CookiesSecure() }

// Load reads BG_* variables. Everything has a working default except the origin, which
// cannot be guessed: WebAuthn ties every passkey to one exact origin, and a passkey
// registered against the wrong one simply will not work.
func Load() (Config, error) {
	cfg := Config{
		DataDir:   env("BG_DATA_DIR", "/var/lib/braviaguard"),
		Listen:    env("BG_LISTEN", ":8443"),
		CertFile:  os.Getenv("BG_TLS_CERT"),
		KeyFile:   os.Getenv("BG_TLS_KEY"),
		RPID:      os.Getenv("BG_RP_ID"),
		SetupCode: os.Getenv("BG_SETUP_CODE"),
	}

	raw := env("BG_ORIGINS", "")
	if raw == "" {
		return cfg, errors.New(
			"BG_ORIGINS is required, e.g. BG_ORIGINS=https://tv.example.com:8443 — " +
				"it must be exactly the address you open in Safari, because WebAuthn " +
				"binds each passkey to one origin")
	}
	for _, part := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		parsed, err := url.Parse(trimmed)
		if err != nil || parsed.Host == "" {
			return cfg, fmt.Errorf("BG_ORIGINS entry %q is not a URL like https://host:port", trimmed)
		}
		cfg.Origins = append(cfg.Origins, strings.TrimRight(trimmed, "/"))
		if cfg.RPID == "" {
			cfg.RPID = parsed.Hostname()
		}
	}
	if len(cfg.Origins) == 0 {
		return cfg, errors.New("BG_ORIGINS had no usable entries")
	}

	zoneName := env("BG_TZ", "Local")
	zone, err := time.LoadLocation(zoneName)
	if err != nil {
		return cfg, fmt.Errorf("BG_TZ %q is not a known timezone: %w", zoneName, err)
	}
	cfg.Zone = zone

	if (cfg.CertFile == "") != (cfg.KeyFile == "") {
		return cfg, errors.New("set both BG_TLS_CERT and BG_TLS_KEY, or neither")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
