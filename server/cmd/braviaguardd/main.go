// Command braviaguardd is the household backend: one static binary, one data directory.
//
// It serves the parent app (passkeys, Face ID) and the device API the televisions poll.
// Designed to sit on a Raspberry Pi under podman and be left alone.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Embeds the IANA timezone database in the binary so BG_TZ works in a FROM scratch
	// image with no /usr/share/zoneinfo. Costs about 450 KB; worth it to keep the
	// container at one file.
	_ "time/tzdata"

	"github.com/homelab/braviaguard/internal/api"
	"github.com/homelab/braviaguard/internal/auth"
	"github.com/homelab/braviaguard/internal/config"
	"github.com/homelab/braviaguard/internal/store"
	"github.com/homelab/braviaguard/web"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("open data dir %s: %v", cfg.DataDir, err)
	}

	setupCode := cfg.SetupCode
	needsSetup := true
	st.Read(func(s *store.State, _ store.Usage) { needsSetup = len(s.Parents) == 0 })
	if needsSetup && setupCode == "" {
		setupCode = randomDigits(8)
	}

	authManager, err := auth.NewManager(
		st, cfg.RPID, "BraviaGuard", cfg.Origins, setupCode, cfg.CookiesSecure(),
	)
	if err != nil {
		log.Fatalf("webauthn: %v", err)
	}

	server := api.New(st, authManager, web.Assets, cfg.Zone)

	var handler http.Handler = server.Handler()
	if cfg.BehindProxy() {
		// Inside the logging wrapper, so a redirect still shows up in the log — if the
		// phone is bouncing, that is the first thing worth seeing.
		handler = api.RequireHTTPS(cfg.Origins[0], handler)
	}

	// A device sync request is held open for up to api.MaxSyncWait, and Go starts both
	// the read and the write deadline when the request header is read rather than when the
	// handler returns. So both have to clear the hold with room to spare, or long polling
	// would die as an i/o timeout exactly when it was working. ReadHeaderTimeout stays
	// short: that is the one that turns away a client dribbling out its headers.
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           withLogging(handler),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       api.MaxSyncWait + 30*time.Second,
		WriteTimeout:      api.MaxSyncWait + 30*time.Second,
		IdleTimeout:       120 * time.Second,
	}

	done := make(chan struct{})
	// Usage and last-seen updates are flushed on this timer rather than per request.
	st.StartFlusher(60*time.Second, done)

	banner(cfg, setupCode, needsSetup)

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-shutdown
		log.Println("shutting down")
		close(done) // triggers a final flush
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()

	switch {
	case cfg.TLSEnabled():
		err = httpServer.ListenAndServeTLS(cfg.CertFile, cfg.KeyFile)

	case cfg.BehindProxy():
		// Plain HTTP here, but the origins are https, so something in front is
		// terminating TLS — a Cloudflare Tunnel or a reverse proxy. The browser is on
		// a secure context, so Face ID works and session cookies are still marked
		// Secure; see config.CookiesSecure.
		log.Printf("serving plain HTTP behind a TLS terminator (origins: %v)", cfg.Origins)
		err = httpServer.ListenAndServe()

	default:
		log.Println("WARNING: no TLS anywhere — the origins are not https. Face ID " +
			"will not work, because WebAuthn needs a secure context. Use this only " +
			"to bring the TV side up.")
		err = httpServer.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}

	if err := st.Flush(); err != nil {
		log.Printf("final flush: %v", err)
	}
	log.Println("stopped")
}

func banner(cfg config.Config, setupCode string, needsSetup bool) {
	log.Printf("braviaguardd listening on %s (tls=%v)", cfg.Listen, cfg.TLSEnabled())
	log.Printf("data dir: %s", cfg.DataDir)
	log.Printf("webauthn rp id: %s  origins: %v", cfg.RPID, cfg.Origins)
	log.Printf("timezone: %s", cfg.Zone)
	if needsSetup {
		log.Printf("")
		log.Printf("  No parent passkey registered yet.")
		log.Printf("  Open %s on your iPhone and enter this setup code:", cfg.Origins[0])
		log.Printf("")
		log.Printf("      SETUP CODE: %s", setupCode)
		log.Printf("")
		log.Printf("  It is only valid for this run of the process.")
	}
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		// Device sync is the overwhelming majority of traffic and says nothing useful;
		// logging it would bury everything else.
		if r.URL.Path != "/api/device/sync" || recorder.status >= 400 {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, recorder.status,
				time.Since(started).Round(time.Millisecond))
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func randomDigits(n int) string {
	out := make([]byte, n)
	for i := range out {
		digit, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			log.Fatalf("crypto/rand: %v", err)
		}
		out[i] = byte('0' + digit.Int64())
	}
	return string(out)
}
