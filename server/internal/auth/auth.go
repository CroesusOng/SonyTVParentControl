// Package auth handles parent sign-in with passkeys.
//
// The family asked for Face ID and no passwords, so there is no password path at all —
// not a fallback, not a recovery code. A passkey with user verification required means
// the browser will not produce an assertion without Face ID (or the device passcode),
// and because the credential is discoverable the parent never types a username either.
//
// Session policy: a session is good for a week for *reading* the dashboard, but any
// action that changes something — unlocking the TV, editing rules, pairing a device —
// needs an assertion from the last few minutes. That is the difference between "mum can
// glance at the TV's status" and "whoever picks up the unlocked phone can switch the
// rules off".
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/homelab/braviaguard/internal/store"
)

const (
	SessionCookie   = "bg_session"
	ceremonyCookie  = "bg_ceremony"
	SessionLifetime = 7 * 24 * time.Hour
	// How recently the parent must have proved presence before a write is accepted.
	AssertionFreshness = 5 * time.Minute
	ceremonyLifetime   = 5 * time.Minute
)

var (
	ErrNoSession      = errors.New("not signed in")
	ErrStaleAssertion = errors.New("reauth required")
	ErrSetupCode      = errors.New("setup code is wrong")
	ErrNoCeremony     = errors.New("no ceremony in progress")
)

type Manager struct {
	wa        *webauthn.WebAuthn
	store     *store.Store
	setupCode string
	secure    bool

	mu         sync.Mutex
	ceremonies map[string]*ceremony
}

type ceremony struct {
	session   *webauthn.SessionData
	parentID  string
	expiresAt time.Time
}

func NewManager(st *store.Store, rpID, rpDisplayName string, origins []string, setupCode string, secure bool) (*Manager, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: rpDisplayName,
		RPOrigins:     origins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			// Platform + resident key + required UV is exactly "a passkey on this phone,
			// released only after Face ID".
			AuthenticatorAttachment: protocol.Platform,
			ResidentKey:             protocol.ResidentKeyRequirementRequired,
			UserVerification:        protocol.VerificationRequired,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("configure webauthn: %w", err)
	}
	return &Manager{
		wa:         wa,
		store:      st,
		setupCode:  setupCode,
		secure:     secure,
		ceremonies: map[string]*ceremony{},
	}, nil
}

// ---------- the webauthn.User adapter ----------

type parentUser struct {
	handle      []byte
	name        string
	credentials []webauthn.Credential
}

func (u *parentUser) WebAuthnID() []byte                         { return u.handle }
func (u *parentUser) WebAuthnName() string                       { return u.name }
func (u *parentUser) WebAuthnDisplayName() string                { return u.name }
func (u *parentUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func toUser(p *store.Parent) (*parentUser, error) {
	handle, err := base64.RawURLEncoding.DecodeString(p.HandleBase64)
	if err != nil {
		return nil, fmt.Errorf("bad user handle for %s: %w", p.Name, err)
	}
	credentials := make([]webauthn.Credential, 0, len(p.Credentials))
	for _, raw := range p.Credentials {
		var credential webauthn.Credential
		if err := json.Unmarshal(raw, &credential); err != nil {
			return nil, fmt.Errorf("stored credential for %s is unreadable: %w", p.Name, err)
		}
		credentials = append(credentials, credential)
	}
	return &parentUser{handle: handle, name: p.Name, credentials: credentials}, nil
}

// ---------- registration ----------

// HasParents reports whether anyone has enrolled a passkey yet.
func (m *Manager) HasParents() bool {
	count := 0
	m.store.Read(func(s *store.State, _ store.Usage) { count = len(s.Parents) })
	return count > 0
}

// BeginRegistration starts enrolling a passkey. The first one needs the setup code the
// server printed at startup; after that, an existing signed-in parent adds the next one.
func (m *Manager) BeginRegistration(w http.ResponseWriter, r *http.Request, name, setupCode string) (*protocol.CredentialCreation, error) {
	if m.HasParents() {
		if _, err := m.RequireFreshAssertion(r); err != nil {
			return nil, err
		}
	} else if setupCode == "" || setupCode != m.setupCode {
		return nil, ErrSetupCode
	}

	parentID := newToken(12)
	handle := make([]byte, 32)
	if _, err := rand.Read(handle); err != nil {
		return nil, err
	}

	user := &parentUser{handle: handle, name: name}
	creation, session, err := m.wa.BeginRegistration(user)
	if err != nil {
		return nil, fmt.Errorf("begin registration: %w", err)
	}

	err = m.store.Write(func(s *store.State, _ store.Usage) error {
		s.Parents = append(s.Parents, store.Parent{
			ID:              parentID,
			Name:            name,
			HandleBase64:    base64.RawURLEncoding.EncodeToString(handle),
			Credentials:     nil,
			CreatedAtMillis: time.Now().UnixMilli(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	m.startCeremony(w, session, parentID)
	return creation, nil
}

func (m *Manager) FinishRegistration(w http.ResponseWriter, r *http.Request) error {
	cer, err := m.takeCeremony(r)
	if err != nil {
		return err
	}

	var user *parentUser
	m.store.Read(func(s *store.State, _ store.Usage) {
		if p := s.ParentByID(cer.parentID); p != nil {
			user, _ = toUser(p)
		}
	})
	if user == nil {
		return errors.New("registration target no longer exists")
	}

	credential, err := m.wa.FinishRegistration(user, *cer.session, r)
	if err != nil {
		// A half-registered parent with no credentials can never sign in; drop it so the
		// setup code keeps working for a retry.
		_ = m.store.Write(func(s *store.State, _ store.Usage) error {
			s.Parents = dropParentWithoutCredentials(s.Parents, cer.parentID)
			return nil
		})
		return fmt.Errorf("finish registration: %w", err)
	}

	raw, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	if err := m.store.Write(func(s *store.State, _ store.Usage) error {
		p := s.ParentByID(cer.parentID)
		if p == nil {
			return errors.New("registration target no longer exists")
		}
		p.Credentials = append(p.Credentials, raw)
		return nil
	}); err != nil {
		return err
	}

	return m.issueSession(w, cer.parentID)
}

// ---------- login ----------

func (m *Manager) BeginLogin(w http.ResponseWriter, _ *http.Request) (*protocol.CredentialAssertion, error) {
	assertion, session, err := m.wa.BeginDiscoverableLogin()
	if err != nil {
		return nil, fmt.Errorf("begin login: %w", err)
	}
	m.startCeremony(w, session, "")
	return assertion, nil
}

func (m *Manager) FinishLogin(w http.ResponseWriter, r *http.Request) error {
	cer, err := m.takeCeremony(r)
	if err != nil {
		return err
	}

	var matchedParentID string
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		encoded := base64.RawURLEncoding.EncodeToString(userHandle)
		var user *parentUser
		var lookupErr error
		m.store.Read(func(s *store.State, _ store.Usage) {
			p := s.ParentByHandle(encoded)
			if p == nil {
				lookupErr = errors.New("unknown passkey")
				return
			}
			matchedParentID = p.ID
			user, lookupErr = toUser(p)
		})
		if lookupErr != nil {
			return nil, lookupErr
		}
		return user, nil
	}

	if _, err := m.wa.FinishDiscoverableLogin(handler, *cer.session, r); err != nil {
		return fmt.Errorf("finish login: %w", err)
	}
	return m.issueSession(w, matchedParentID)
}

func (m *Manager) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookie); err == nil {
		_ = m.store.Write(func(s *store.State, _ store.Usage) error {
			for i := range s.Sessions {
				if s.Sessions[i].Token == cookie.Value {
					s.Sessions = append(s.Sessions[:i], s.Sessions[i+1:]...)
					break
				}
			}
			return nil
		})
	}
	m.clearCookie(w, SessionCookie)
}

// ---------- session checks ----------

// RequireSession allows reads: the dashboard, usage history, device list.
func (m *Manager) RequireSession(r *http.Request) (*store.Session, error) {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil, ErrNoSession
	}
	now := time.Now().UnixMilli()
	var found *store.Session
	m.store.Read(func(s *store.State, _ store.Usage) {
		if sess := s.SessionByToken(cookie.Value); sess != nil && sess.ExpiresAtMillis > now {
			copied := *sess
			found = &copied
		}
	})
	if found == nil {
		return nil, ErrNoSession
	}
	return found, nil
}

// RequireFreshAssertion gates every write. Returning ErrStaleAssertion is not a failure:
// it is the signal that makes the parent app run a Face ID prompt and retry.
func (m *Manager) RequireFreshAssertion(r *http.Request) (*store.Session, error) {
	session, err := m.RequireSession(r)
	if err != nil {
		return nil, err
	}
	age := time.Since(time.UnixMilli(session.LastAssertionMillis))
	if age > AssertionFreshness {
		return nil, ErrStaleAssertion
	}
	return session, nil
}

func (m *Manager) issueSession(w http.ResponseWriter, parentID string) error {
	token := newToken(32)
	now := time.Now()
	if err := m.store.Write(func(s *store.State, _ store.Usage) error {
		s.Sessions = append(s.Sessions, store.Session{
			Token:               token,
			ParentID:            parentID,
			ExpiresAtMillis:     now.Add(SessionLifetime).UnixMilli(),
			LastAssertionMillis: now.UnixMilli(),
		})
		return nil
	}); err != nil {
		return err
	}
	m.setCookie(w, SessionCookie, token, SessionLifetime)
	return nil
}

// ---------- ceremony bookkeeping ----------

func (m *Manager) startCeremony(w http.ResponseWriter, session *webauthn.SessionData, parentID string) {
	id := newToken(16)
	m.mu.Lock()
	m.pruneCeremoniesLocked()
	m.ceremonies[id] = &ceremony{
		session:   session,
		parentID:  parentID,
		expiresAt: time.Now().Add(ceremonyLifetime),
	}
	m.mu.Unlock()
	m.setCookie(w, ceremonyCookie, id, ceremonyLifetime)
}

func (m *Manager) takeCeremony(r *http.Request) (*ceremony, error) {
	cookie, err := r.Cookie(ceremonyCookie)
	if err != nil {
		return nil, ErrNoCeremony
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cer, ok := m.ceremonies[cookie.Value]
	delete(m.ceremonies, cookie.Value) // single use
	if !ok || time.Now().After(cer.expiresAt) {
		return nil, ErrNoCeremony
	}
	return cer, nil
}

func (m *Manager) pruneCeremoniesLocked() {
	now := time.Now()
	for id, cer := range m.ceremonies {
		if now.After(cer.expiresAt) {
			delete(m.ceremonies, id)
		}
	}
}

func (m *Manager) setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		// Only over TLS in the real deployment; a plain-HTTP dev run would never see
		// the cookie come back if this were hardcoded true.
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(maxAge.Seconds()),
	})
}

func (m *Manager) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", HttpOnly: true,
		Secure: m.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

func dropParentWithoutCredentials(parents []store.Parent, id string) []store.Parent {
	out := parents[:0]
	for _, p := range parents {
		if p.ID == id && len(p.Credentials) == 0 {
			continue
		}
		out = append(out, p)
	}
	return out
}

func newToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
