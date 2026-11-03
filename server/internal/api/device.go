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

// handleSync is the only endpoint a paired TV needs. The policy is omitted when the TV
// already has the current version, which keeps a 30-second poll down to a few bytes.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request, _ store.Device) {
	knownVersion, _ := strconv.ParseInt(r.URL.Query().Get("policyVersion"), 10, 64)
	now := time.Now().UnixMilli()

	response := model.SyncResponse{ServerTimeMillis: now, Grants: []model.Grant{}}
	s.store.Read(func(st *store.State, _ store.Usage) {
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
	writeJSON(w, http.StatusOK, response)
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
