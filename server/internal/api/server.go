// Package api exposes two very different surfaces on one port.
//
//   - /api/device/*  the televisions. Bearer token, machine to machine, no cookies.
//   - /api/parent/*  the phone. Passkey session cookie, Face ID for anything that writes.
//
// Everything else is the parent app itself, served from the embedded web/ directory.
package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/homelab/braviaguard/internal/auth"
	"github.com/homelab/braviaguard/internal/model"
	"github.com/homelab/braviaguard/internal/store"
)

const maxBody = 1 << 20 // 1 MiB; the biggest legitimate body is a few days of usage

type Server struct {
	store  *store.Store
	auth   *auth.Manager
	assets fs.FS
	zone   *time.Location
	mux    *http.ServeMux
}

func New(st *store.Store, am *auth.Manager, assets fs.FS, zone *time.Location) *Server {
	s := &Server{store: st, auth: am, assets: assets, zone: zone, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	// --- televisions ---
	s.mux.HandleFunc("POST /api/device/enroll", s.handleEnroll)
	s.mux.HandleFunc("GET /api/device/sync", s.device(s.handleSync))
	s.mux.HandleFunc("POST /api/device/usage", s.device(s.handleUsageReport))
	s.mux.HandleFunc("POST /api/device/block-event", s.device(s.handleBlockEvent))

	// --- parent app: sign in ---
	s.mux.HandleFunc("GET /api/parent/bootstrap", s.handleBootstrap)
	s.mux.HandleFunc("POST /api/parent/register/begin", s.handleRegisterBegin)
	s.mux.HandleFunc("POST /api/parent/register/finish", s.handleRegisterFinish)
	s.mux.HandleFunc("POST /api/parent/login/begin", s.handleLoginBegin)
	s.mux.HandleFunc("POST /api/parent/login/finish", s.handleLoginFinish)
	s.mux.HandleFunc("POST /api/parent/logout", s.handleLogout)

	// --- parent app: read ---
	s.mux.HandleFunc("GET /api/parent/state", s.parentRead(s.handleState))
	s.mux.HandleFunc("GET /api/parent/usage", s.parentRead(s.handleUsageQuery))
	s.mux.HandleFunc("GET /api/parent/apps", s.parentRead(s.handleApps))

	// --- parent app: write, each one behind a fresh Face ID assertion ---
	s.mux.HandleFunc("PUT /api/parent/policy", s.parentWrite(s.handlePutPolicy))
	s.mux.HandleFunc("POST /api/parent/enforcement", s.parentWrite(s.handleEnforcement))
	s.mux.HandleFunc("POST /api/parent/grants", s.parentWrite(s.handleCreateGrant))
	s.mux.HandleFunc("DELETE /api/parent/grants/{id}", s.parentWrite(s.handleDeleteGrant))
	s.mux.HandleFunc("POST /api/parent/pairing-code", s.parentWrite(s.handlePairingCode))
	s.mux.HandleFunc("DELETE /api/parent/devices/{id}", s.parentWrite(s.handleDeleteDevice))
	s.mux.HandleFunc("GET /api/parent/escape-secret", s.parentWrite(s.handleEscapeSecret))
	s.mux.HandleFunc("GET /api/parent/escape-code", s.parentWrite(s.handleEscapeCode))

	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok\n")
	})

	s.mux.Handle("GET /", s.staticHandler())
}

// staticHandler serves the parent app, with no-store on the HTML so a republished
// build is picked up instead of being pinned by a stale cache on someone's phone.
func (s *Server) staticHandler() http.Handler {
	files := http.FileServer(http.FS(s.assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html") ||
			strings.HasSuffix(r.URL.Path, "sw.js") {
			w.Header().Set("Cache-Control", "no-store")
		}
		// Go's mime table has no entry for .webmanifest, so it would go out as
		// text/plain. ServeContent keeps a Content-Type that is already set, and the
		// manifest is what makes "Add to Home Screen" behave like an app rather than
		// a bookmark, so it is worth being correct about.
		if strings.HasSuffix(r.URL.Path, ".webmanifest") {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		files.ServeHTTP(w, r)
	})
}

// ---------- middleware ----------

func (s *Server) device(
	next func(http.ResponseWriter, *http.Request, store.Device),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			fail(w, http.StatusUnauthorized, "missing_token", "")
			return
		}
		hash := hashToken(token)

		var device store.Device
		found := false
		s.store.Read(func(st *store.State, _ store.Usage) {
			// Constant-time compare so a wrong token cannot be narrowed down by timing.
			for i := range st.Devices {
				if subtle.ConstantTimeCompare([]byte(st.Devices[i].TokenHash), []byte(hash)) == 1 {
					device = st.Devices[i]
					found = true
					return
				}
			}
		})
		if !found {
			fail(w, http.StatusUnauthorized, "unknown_device", "")
			return
		}

		// Deferred write: a TV checks in every few seconds and this must not cause a
		// file write each time.
		s.store.Touch(func(st *store.State, _ store.Usage) {
			if d := st.DeviceByID(device.ID); d != nil {
				d.LastSeenMillis = time.Now().UnixMilli()
			}
		})
		next(w, r, device)
	}
}

func (s *Server) parentRead(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.auth.RequireSession(r); err != nil {
			fail(w, http.StatusUnauthorized, "signin_required", "")
			return
		}
		next(w, r)
	}
}

func (s *Server) parentWrite(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, err := s.auth.RequireFreshAssertion(r)
		switch {
		case errors.Is(err, auth.ErrStaleAssertion):
			// Not an error the user sees: the parent app reacts by running a Face ID
			// prompt and replaying the request.
			fail(w, http.StatusUnauthorized, "reauth_required", "")
			return
		case err != nil:
			fail(w, http.StatusUnauthorized, "signin_required", "")
			return
		}
		next(w, r)
	}
}

// ---------- helpers ----------

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(header, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := decoder.Decode(target); err != nil {
		fail(w, http.StatusBadRequest, "bad_json", err.Error())
		return false
	}
	return true
}

func fail(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, model.APIError{Error: code, Detail: detail})
}
