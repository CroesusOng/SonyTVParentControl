package api

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/homelab/braviaguard/internal/model"
	"github.com/homelab/braviaguard/internal/store"
	"github.com/homelab/braviaguard/internal/totp"
)

var errBadPairingCode = errors.New("pairing code is not valid")

// handleEnroll trades a single-use pairing code for a long-lived device token.
// The token itself is returned once and only its SHA-256 is kept, so a stolen state.json
// does not hand anyone a working device identity.
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var request model.EnrollRequest
	if !readJSON(w, r, &request) {
		return
	}
	if request.PairingCode == "" {
		fail(w, http.StatusBadRequest, "missing_pairing_code", "")
		return
	}

	now := time.Now()
	token := randomToken(32)
	var response model.EnrollResponse

	err := s.store.Write(func(st *store.State, _ store.Usage) error {
		if !st.TakePairingCode(request.PairingCode, now.UnixMilli()) {
			return errBadPairingCode
		}
		// One secret for the whole household: the escape code has to work on any TV,
		// and a parent reading a code off their phone should not have to know which.
		if st.EscapeSecret == "" {
			secret, err := totp.NewSecret()
			if err != nil {
				return err
			}
			st.EscapeSecret = secret
		}

		deviceID := randomToken(9)
		name := request.DeviceName
		if name == "" {
			name = "Android TV"
		}
		st.Devices = append(st.Devices, store.Device{
			ID:              deviceID,
			Name:            name,
			Model:           request.Model,
			AndroidRelease:  request.AndroidRelease,
			TokenHash:       hashToken(token),
			CreatedAtMillis: now.UnixMilli(),
			LastSeenMillis:  now.UnixMilli(),
		})
		response = model.EnrollResponse{
			DeviceID:           deviceID,
			Token:              token,
			EscapeSecretBase32: st.EscapeSecret,
			ServerTimeMillis:   now.UnixMilli(),
		}
		return nil
	})

	switch {
	case errors.Is(err, errBadPairingCode):
		fail(w, http.StatusForbidden, "bad_pairing_code", "")
	case err != nil:
		fail(w, http.StatusInternalServerError, "enroll_failed", err.Error())
	default:
		writeJSON(w, http.StatusOK, response)
	}
}

// MaxSyncWait caps how long one device sync request may be held open.
//
// It has to stay comfortably inside the server's WriteTimeout, because Go starts that
// clock when the request header is read and not when the handler returns: a handler parked
// for this long has only the remainder in which to reply. cmd/braviaguardd/main.go derives
// its timeouts from this constant so the two cannot drift apart.
const MaxSyncWait = 20 * time.Second

// handleSync is the only endpoint a paired TV needs. The policy is omitted when the TV
// already has the current version, which keeps the request down to a few bytes.
//
// With `wait` set the request is a long poll: a television that is already up to date gets
// no reply until a parent changes something, the hold expires, or the connection drops.
// That turns an unlock tapped on a phone into a LAN round trip instead of a wait for the
// TV's next poll, which measured 26 seconds in practice. It stays plain request/response,
// so the app needs no reconnect state machine for the standby cycles a television goes
// through daily -- a held request that dies is just a failed request, retried.
//
// Grant *expiry* is deliberately not pushed. Nothing is written when a grant runs out, so
// there is no revision to bump; the TV knows every expiry up front and re-locks on its own
// schedule. See RuleEngine.nextBoundary.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request, _ store.Device) {
	knownVersion, _ := strconv.ParseInt(r.URL.Query().Get("policyVersion"), 10, 64)
	knownRevision, _ := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	wait := syncWait(r.URL.Query().Get("wait"))

	// One timer for the whole hold. Re-arming it inside the loop would leave a timer per
	// wake-up alive until it fired.
	timer := time.NewTimer(wait)
	defer timer.Stop()

	for {
		// Claimed before the state is read, so a write that lands in between closes this
		// channel rather than going unnoticed until the device asks again.
		changed := s.store.Changed()
		response := s.syncResponse(knownVersion)
		// Up to date means same revision *and* same policy version. Revision alone is not
		// enough: a freshly created state file sits at revision zero, which is also what a
		// device that has never synced sends, and that TV still has a policy to collect.
		upToDate := response.Revision == knownRevision && response.Policy == nil
		if wait <= 0 || !upToDate {
			writeJSON(w, http.StatusOK, response)
			return
		}
		select {
		case <-changed:
			// Round again rather than replying here, so serverTimeMillis and the grant
			// list describe the moment of the reply.
		case <-timer.C:
			// Rebuilt for the same reason: the response in hand was composed when the hold
			// began, and answering with it would hand the TV a server clock twenty seconds
			// behind the real one -- in the field it uses to judge its own clock.
			writeJSON(w, http.StatusOK, s.syncResponse(knownVersion))
			return
		case <-r.Context().Done():
			// Standby, a lost network, or a restarted app. There is nobody to write to.
			return
		}
	}
}

func (s *Server) syncResponse(knownVersion int64) model.SyncResponse {
	now := time.Now().UnixMilli()
	response := model.SyncResponse{ServerTimeMillis: now, Grants: []model.Grant{}}
	s.store.Read(func(st *store.State, _ store.Usage) {
		response.Revision = st.Revision
		if st.Policy.Version != knownVersion {
			policy := st.Policy
			response.Policy = &policy
		}
		for _, grant := range st.Grants {
			if grant.ExpiresAtMillis > now {
				response.Grants = append(response.Grants, grant)
			}
		}
		// Sent every time on purpose: it is 32 characters, and it means a TV that lost
		// its keystore (OS upgrade, app reinstall) heals itself on the next poll instead
		// of needing to be re-paired.
		response.EscapeSecretBase32 = st.EscapeSecret
	})
	return response
}

// syncWait reads the hold a device asked for, clamped to MaxSyncWait. Absent, unparsable
// or zero means reply at once, which is what an app built before long polling sends and
// what the WorkManager backstop wants even on a new one.
func syncWait(raw string) time.Duration {
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 0
	}
	return min(time.Duration(seconds)*time.Second, MaxSyncWait)
}

// handleUsageReport takes running totals for the last few days.
func (s *Server) handleUsageReport(w http.ResponseWriter, r *http.Request, device store.Device) {
	var request model.UsageReportRequest
	if !readJSON(w, r, &request) {
		return
	}
	s.store.Touch(func(st *store.State, usage store.Usage) {
		usage.MergeUsage(device.ID, request.Days)
		for pkg, label := range request.Labels {
			if label != "" {
				st.AppLabels[pkg] = label
			}
		}
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleBlockEvent records what was blocked, so the parent app can show "Netflix was
// blocked two minutes ago" with a one-tap unlock next to it.
func (s *Server) handleBlockEvent(w http.ResponseWriter, r *http.Request, device store.Device) {
	var request model.BlockEventRequest
	if !readJSON(w, r, &request) {
		return
	}
	err := s.store.Write(func(st *store.State, _ store.Usage) error {
		if request.AppLabel != "" {
			st.AppLabels[request.PackageName] = request.AppLabel
		}
		st.RecentBlocks = append(st.RecentBlocks, store.BlockEvent{
			DeviceID:    device.ID,
			PackageName: request.PackageName,
			AppLabel:    request.AppLabel,
			Reason:      request.Reason,
			AtMillis:    request.AtMillis,
		})
		return nil
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func randomToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// randomDigits is for pairing codes, which get typed on a TV remote. Rejection-free
// uniform sampling via crypto/rand — modulo bias would be harmless here but there is
// no reason to introduce it.
func randomDigits(n int) string {
	out := make([]byte, n)
	for i := range out {
		digit, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}
		out[i] = byte('0' + digit.Int64())
	}
	return string(out)
}
