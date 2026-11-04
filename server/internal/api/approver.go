package api

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/homelab/braviaguard/internal/store"
)

// Approvals exist because of one gap that cannot be closed in a browser.
//
// The parent app signs in with a passkey, and on iOS a passkey assertion is satisfied by
// Face ID *or* the device passcode. WebAuthn has no way to ask for biometrics only --
// userVerification is required/preferred/discouraged and nothing else -- and the UV flag
// that comes back is one bit that does not say which was used. In a house where the
// children know the passcode, that makes a passkey a sign-in credential and no more.
//
// So handing out television time is gated on a second key instead: a P-256 key generated
// inside an iPhone's Secure Enclave under .biometryCurrentSet, held by the native
// approver app. Only biometrics can unwrap it, there is no passcode fallback, and
// enrolling another face destroys it. The server never sees the private half; it checks a
// signature over a nonce it issued itself.
//
// What this does not prove: that the key really was created with that access control. The
// server cannot tell a biometry-gated key from an ordinary one, and a free provisioning
// profile gets no App Attest. The threat being defended against is a child picking up a
// parent's phone and typing a passcode they already know -- not one who rebuilds the app
// from source to lie about its own keychain flags.

const (
	approverCodeDigits   = 6
	approverCodeLifetime = 10 * time.Minute
	challengeLifetime    = 2 * time.Minute
	maxApproverNameRunes = 40

	// Domain separation, so a signature made for this system cannot be replayed into
	// anything else that asks the same key to sign a nonce.
	approvalDomain = "braviaguard/approval/v1"
)

var errNoApproval = errors.New("no approver credentials on the request")

// approvalPayload is the exact byte string the phone signs. Both sides build it from the
// same five pieces, and the request itself is one of them: a signature collected for
// "30 minutes" cannot be lifted onto a request for a whole day.
//
// The query string is deliberately not included -- no gated endpoint reads one. Add an
// endpoint that does, and it has to go in here and in the app together.
func approvalPayload(nonce, method, path string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(strings.Join([]string{
		approvalDomain, nonce, method, path, hex.EncodeToString(sum[:]),
	}, "\n"))
}

// parseApproverKey accepts the uncompressed ANSI X9.63 point that
// SecKeyCopyExternalRepresentation produces on the phone. crypto/ecdh does the
// validation: it rejects a wrong length, a bad prefix and a point off the curve, which is
// what the now-deprecated elliptic.Unmarshal used to be needed for.
func parseApproverKey(raw []byte) (*ecdsa.PublicKey, error) {
	if _, err := ecdh.P256().NewPublicKey(raw); err != nil {
		return nil, err
	}
	// NewPublicKey having succeeded, raw is 65 bytes beginning with 0x04.
	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(raw[1:33]),
		Y:     new(big.Int).SetBytes(raw[33:65]),
	}, nil
}

// verifyApproval checks a DER-encoded ECDSA signature, which is what
// kSecKeyAlgorithmECDSASignatureMessageX962SHA256 hands back.
func verifyApproval(publicKey, signature, payload []byte) bool {
	key, err := parseApproverKey(publicKey)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(payload)
	return ecdsa.VerifyASN1(key, digest[:], signature)
}

// takeBody reads the body so a signature can be checked over it, then puts it back for
// the handler -- which would otherwise find nothing left to read.
func takeBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, "body_too_large", "")
		return nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return raw, true
}

// ---------- the gate ----------

// needsApproval replaces parentWrite on the actions that hand out television time. Three
// outcomes:
//
//   - a valid Secure Enclave signature: allowed
//   - no approver enrolled yet: falls through to parentWrite, today's behaviour exactly
//   - an approver exists but the signature is missing or wrong: 403, and a passcode is no
//     help whatsoever
//
// That middle case is what makes this safe to ship: deploying the build changes nothing
// until a phone is enrolled. After that there is no way back to passcode-only, because
// removing the last approver is itself gated here.
func (s *Server) needsApproval(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var enrolled bool
		s.store.Read(func(st *store.State, _ store.Usage) { enrolled = len(st.Approvers) > 0 })
		if !enrolled {
			s.parentWrite(next)(w, r)
			return
		}

		body, ok := takeBody(w, r)
		if !ok {
			return
		}
		id, err := s.checkApproval(r, body)
		if err != nil {
			// Worth a line: this is what a bypass attempt looks like from the server.
			log.Printf("approval refused for %s %s: %v", r.Method, r.URL.Path, err)
			fail(w, http.StatusForbidden, "approval_required", "")
			return
		}
		log.Printf("approved by %s: %s %s", id, r.Method, r.URL.Path)
		next(w, r)
	}
}

// checkApproval verifies one request and burns the nonce it used.
//
// The nonce is consumed whatever the outcome, including a wrong signature. Leaving it
// alive on failure would turn one challenge into an unlimited number of attempts.
func (s *Server) checkApproval(r *http.Request, body []byte) (string, error) {
	approverID := r.Header.Get("X-Approver-Id")
	challengeID := r.Header.Get("X-Approver-Challenge")
	signature, sigErr := base64.StdEncoding.DecodeString(r.Header.Get("X-Approver-Signature"))
	token := bearerToken(r)
	if approverID == "" || challengeID == "" || token == "" || sigErr != nil {
		return "", errNoApproval
	}

	now := time.Now().UnixMilli()
	var reason error
	err := s.store.Write(func(st *store.State, _ store.Usage) error {
		nonce, fresh := st.TakeApproverChallenge(challengeID, now)
		approver := st.ApproverByID(approverID)
		switch {
		case approver == nil:
			reason = errors.New("unknown approver")
		case !fresh:
			reason = errors.New("challenge unknown or expired")
		case subtle.ConstantTimeCompare(
			[]byte(approver.TokenHash), []byte(hashToken(token))) != 1:
			reason = errors.New("approver token does not match")
		case !verifyApproval(approver.PublicKey, signature,
			approvalPayload(nonce, r.Method, r.URL.Path, body)):
			reason = errors.New("signature does not verify")
		default:
			approver.LastUsedMillis = now
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if reason != nil {
		return "", reason
	}
	return approverID, nil
}

// approver authenticates by bearer token alone. Enough to read state and ask for a nonce;
// nowhere near enough to approve anything, which always needs a signature on top.
func (s *Server) approver(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			fail(w, http.StatusUnauthorized, "missing_token", "")
			return
		}
		hash := hashToken(token)
		found := false
		s.store.Read(func(st *store.State, _ store.Usage) {
			for i := range st.Approvers {
				if subtle.ConstantTimeCompare(
					[]byte(st.Approvers[i].TokenHash), []byte(hash)) == 1 {
					found = true
					return
				}
			}
		})
		if !found {
			fail(w, http.StatusUnauthorized, "unknown_approver", "")
			return
		}
		next(w, r)
	}
}

// ---------- enrolment ----------

// handleApproverCode mints a one-time code for enrolling a phone. Gated by needsApproval,
// which means the first one is bootstrapped by the passkey and every one after that needs
// a real face from a phone already enrolled.
func (s *Server) handleApproverCode(w http.ResponseWriter, _ *http.Request) {
	code := randomDigits(approverCodeDigits)
	expiresAt := time.Now().Add(approverCodeLifetime)
	err := s.store.Write(func(st *store.State, _ store.Usage) error {
		st.ApproverCodes = append(st.ApproverCodes, store.PairingCode{
			Code:            code,
			ExpiresAtMillis: expiresAt.UnixMilli(),
		})
		return nil
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code":            code,
		"expiresAtMillis": expiresAt.UnixMilli(),
	})
}

// handleApproverEnroll trades a one-time code plus a self-signature for a reading token.
//
// The signature is over the code, using the key being registered, which proves the app
// holds the private half: a code read over someone's shoulder is useless without it.
func (s *Server) handleApproverEnroll(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Code      string `json:"code"`
		Name      string `json:"name"`
		PublicKey []byte `json:"publicKey"`
		Signature []byte `json:"signature"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	if request.Code == "" {
		fail(w, http.StatusBadRequest, "missing_code", "")
		return
	}
	// Checked before the code is spent, so a malformed request does not burn it.
	if !verifyApproval(request.PublicKey, request.Signature,
		approvalPayload(request.Code, "POST", "/api/approver/enroll", nil)) {
		fail(w, http.StatusForbidden, "bad_signature",
			"the key did not sign its own enrolment code")
		return
	}

	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = "iPhone"
	}
	if runes := []rune(name); len(runes) > maxApproverNameRunes {
		name = string(runes[:maxApproverNameRunes])
	}

	now := time.Now()
	token := randomToken(32)
	id := randomToken(9)
	var spent bool
	err := s.store.Write(func(st *store.State, _ store.Usage) error {
		if !st.TakeApproverCode(request.Code, now.UnixMilli()) {
			return nil
		}
		spent = true
		st.Approvers = append(st.Approvers, store.Approver{
			ID:              id,
			Name:            name,
			PublicKey:       request.PublicKey,
			TokenHash:       hashToken(token),
			CreatedAtMillis: now.UnixMilli(),
			LastUsedMillis:  now.UnixMilli(),
		})
		return nil
	})
	switch {
	case err != nil:
		fail(w, http.StatusInternalServerError, "store_failed", err.Error())
	case !spent:
		fail(w, http.StatusForbidden, "bad_code", "")
	default:
		log.Printf("approver enrolled: %s (%s)", id, name)
		writeJSON(w, http.StatusOK, map[string]any{
			"approverId":       id,
			"token":            token,
			"serverTimeMillis": now.UnixMilli(),
		})
	}
}

// ---------- approving ----------

// handleApproverChallenge issues the nonce for the next approval. Cheap and unauthorised
// beyond the bearer token: a nonce is worth nothing without the key.
func (s *Server) handleApproverChallenge(w http.ResponseWriter, _ *http.Request) {
	id := randomToken(12)
	nonce := randomToken(32)
	expiresAt := time.Now().Add(challengeLifetime)
	err := s.store.Write(func(st *store.State, _ store.Usage) error {
		st.ApproverChallenges = append(st.ApproverChallenges, store.ApproverChallenge{
			ID:              id,
			Nonce:           nonce,
			ExpiresAtMillis: expiresAt.UnixMilli(),
		})
		return nil
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"challengeId":     id,
		"nonce":           nonce,
		"domain":          approvalDomain,
		"expiresAtMillis": expiresAt.UnixMilli(),
	})
}

// ---------- housekeeping ----------

type approverView struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	CreatedAtMillis int64  `json:"createdAtMillis"`
	LastUsedMillis  int64  `json:"lastUsedMillis"`
}

// approverViews is never allowed to return nil: the parent app checks the length of this
// to decide whether unlocks are gated, and a JSON null would read as zero approvers on a
// household that has one.
func approverViews(st *store.State) []approverView {
	views := make([]approverView, 0, len(st.Approvers))
	for _, a := range st.Approvers {
		views = append(views, approverView{
			ID:              a.ID,
			Name:            a.Name,
			CreatedAtMillis: a.CreatedAtMillis,
			LastUsedMillis:  a.LastUsedMillis,
		})
	}
	return views
}

func (s *Server) handleListApprovers(w http.ResponseWriter, _ *http.Request) {
	var views []approverView
	s.store.Read(func(st *store.State, _ store.Usage) { views = approverViews(st) })
	writeJSON(w, http.StatusOK, map[string]any{"approvers": views})
}

// handleDeleteApprover is gated by needsApproval like the rest, which is what stops the
// obvious attack: removing every approver to get back to a passcode-only system.
func (s *Server) handleDeleteApprover(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.store.Write(func(st *store.State, _ store.Usage) error {
		st.Approvers = slices.DeleteFunc(st.Approvers, func(a store.Approver) bool {
			return a.ID == id
		})
		return nil
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	log.Printf("approver removed: %s", id)
	w.WriteHeader(http.StatusNoContent)
}
