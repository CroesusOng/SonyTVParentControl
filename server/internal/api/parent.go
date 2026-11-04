package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/homelab/braviaguard/internal/auth"
	"github.com/homelab/braviaguard/internal/model"
	"github.com/homelab/braviaguard/internal/store"
	"github.com/homelab/braviaguard/internal/totp"
)

const (
	pairingCodeDigits   = 6
	pairingCodeLifetime = 10 * time.Minute
	maxGrantMinutes     = 24 * 60
)

var (
	timeOfDay  = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	validDays  = []string{"MONDAY", "TUESDAY", "WEDNESDAY", "THURSDAY", "FRIDAY", "SATURDAY", "SUNDAY"}
	validModes = []string{"OFF", "ALLOWLIST", "BLOCKLIST"}
)

// ---------- sign in ----------

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	_, sessionErr := s.auth.RequireSession(r)
	_, freshErr := s.auth.RequireFreshAssertion(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"needsSetup":     !s.auth.HasParents(),
		"signedIn":       sessionErr == nil,
		"assertionFresh": freshErr == nil,
	})
}

func (s *Server) handleRegisterBegin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name      string `json:"name"`
		SetupCode string `json:"setupCode"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	if request.Name == "" {
		request.Name = "Parent"
	}
	creation, err := s.auth.BeginRegistration(w, r, request.Name, request.SetupCode)
	switch {
	case errors.Is(err, auth.ErrSetupCode):
		fail(w, http.StatusForbidden, "bad_setup_code", "")
	case errors.Is(err, auth.ErrStaleAssertion):
		fail(w, http.StatusUnauthorized, "reauth_required", "")
	case errors.Is(err, auth.ErrNoSession):
		fail(w, http.StatusUnauthorized, "signin_required", "")
	case err != nil:
		fail(w, http.StatusInternalServerError, "register_begin_failed", err.Error())
	default:
		writeJSON(w, http.StatusOK, creation)
	}
}

func (s *Server) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.FinishRegistration(w, r); err != nil {
		fail(w, http.StatusBadRequest, "register_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	assertion, err := s.auth.BeginLogin(w, r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "login_begin_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, assertion)
}

func (s *Server) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.FinishLogin(w, r); err != nil {
		fail(w, http.StatusUnauthorized, "login_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.Logout(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- read ----------

type deviceView struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Model          string `json:"model,omitempty"`
	AndroidRelease string `json:"androidRelease,omitempty"`
	LastSeenMillis int64  `json:"lastSeenMillis"`
	Online         bool   `json:"online"`
}

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	today := now.In(s.zone).Format("2006-01-02")
	weekStart := startOfWeek(now.In(s.zone)).Format("2006-01-02")

	response := map[string]any{"serverTimeMillis": now.UnixMilli()}
	s.store.Read(func(st *store.State, usage store.Usage) {
		devices := make([]deviceView, 0, len(st.Devices))
		for _, d := range st.Devices {
			devices = append(devices, deviceView{
				ID:             d.ID,
				Name:           d.Name,
				Model:          d.Model,
				AndroidRelease: d.AndroidRelease,
				LastSeenMillis: d.LastSeenMillis,
				// A paired TV polls at least every 30s while awake, so two minutes of
				// silence means it is off, asleep or has lost the network.
				Online: now.UnixMilli()-d.LastSeenMillis < 120_000,
			})
		}

		grants := make([]model.Grant, 0, len(st.Grants))
		for _, g := range st.Grants {
			if g.ExpiresAtMillis > now.UnixMilli() {
				grants = append(grants, g)
			}
		}

		todayTotals := usage.DailyTotals(today)
		var todaySeconds int64
		if len(todayTotals) > 0 {
			todaySeconds = todayTotals[0].Seconds
		}
		var weekSeconds int64
		for _, day := range usage.DailyTotals(weekStart) {
			weekSeconds += day.Seconds
		}

		response["policy"] = st.Policy
		response["grants"] = grants
		response["devices"] = devices
		response["todaySeconds"] = todaySeconds
		response["weekSeconds"] = weekSeconds
		response["topApps"] = labelled(usage.AppTotals(today), st.AppLabels, 8)
		response["recentBlocks"] = lastBlocks(st.RecentBlocks, 10)
		response["hasEscapeSecret"] = st.EscapeSecret != ""
		// Reported here rather than from its own endpoint: the parent app has to know
		// whether unlocks are gated before it offers a button that would be refused.
		response["approvers"] = approverViews(st)
	})
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleUsageQuery(w http.ResponseWriter, r *http.Request) {
	days := 14
	if raw := r.URL.Query().Get("days"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 120 {
			days = parsed
		}
	}
	from := time.Now().In(s.zone).AddDate(0, 0, -(days - 1)).Format("2006-01-02")

	response := map[string]any{"from": from, "days": days}
	s.store.Read(func(st *store.State, usage store.Usage) {
		response["daily"] = usage.DailyTotals(from)
		response["apps"] = labelled(usage.AppTotals(from), st.AppLabels, 25)
	})
	writeJSON(w, http.StatusOK, response)
}

// handleApps is what makes the rule editor usable: the parent picks from apps the TVs
// have actually reported, instead of typing "com.google.android.youtube.tv" by hand.
func (s *Server) handleApps(w http.ResponseWriter, _ *http.Request) {
	from := time.Now().In(s.zone).AddDate(0, 0, -60).Format("2006-01-02")
	var apps []store.AppTotal
	s.store.Read(func(st *store.State, usage store.Usage) {
		apps = labelled(usage.AppTotals(from), st.AppLabels, 200)
	})
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

// ---------- write ----------

func (s *Server) handlePutPolicy(w http.ResponseWriter, r *http.Request) {
	var incoming model.Policy
	if !readJSON(w, r, &incoming) {
		return
	}
	if err := validatePolicy(&incoming); err != nil {
		fail(w, http.StatusBadRequest, "invalid_policy", err.Error())
		return
	}
	err := s.store.Write(func(st *store.State, _ store.Usage) error {
		// The version is the server's to assign: a TV decides whether to re-read the
		// policy by comparing it, so a client that sent a stale number could pin every
		// television on an old rule set.
		incoming.Version = st.Policy.Version + 1
		st.Policy = incoming
		return nil
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.handleState(w, r)
}

func (s *Server) handleEnforcement(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Enabled bool `json:"enabled"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	err := s.store.Write(func(st *store.State, _ store.Usage) error {
		st.Policy.EnforcementEnabled = request.Enabled
		st.Policy.Version++
		return nil
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	s.handleState(w, r)
}

// handleCreateGrant is the "let them watch for another half hour" button.
func (s *Server) handleCreateGrant(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Minutes      int      `json:"minutes"`
		Packages     []string `json:"packages"`
		Note         string   `json:"note"`
		BypassList   *bool    `json:"bypassList"`
		BypassCurfew *bool    `json:"bypassCurfew"`
		BypassBudget *bool    `json:"bypassBudget"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	if request.Minutes <= 0 || request.Minutes > maxGrantMinutes {
		fail(w, http.StatusBadRequest, "bad_minutes",
			fmt.Sprintf("minutes must be 1..%d", maxGrantMinutes))
		return
	}

	now := time.Now()
	grant := model.Grant{
		ID:              randomToken(9),
		Source:          "PARENT_APP",
		IssuedAtMillis:  now.UnixMilli(),
		ExpiresAtMillis: now.Add(time.Duration(request.Minutes) * time.Minute).UnixMilli(),
		Packages:        request.Packages,
		BypassList:      boolOr(request.BypassList, true),
		BypassCurfew:    boolOr(request.BypassCurfew, true),
		BypassBudget:    boolOr(request.BypassBudget, true),
		Note:            request.Note,
	}
	if err := s.store.Write(func(st *store.State, _ store.Usage) error {
		st.Grants = append(st.Grants, grant)
		return nil
	}); err != nil {
		fail(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, grant)
}

func (s *Server) handleDeleteGrant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.Write(func(st *store.State, _ store.Usage) error {
		st.Grants = slices.DeleteFunc(st.Grants, func(g model.Grant) bool { return g.ID == id })
		return nil
	}); err != nil {
		fail(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePairingCode(w http.ResponseWriter, _ *http.Request) {
	code := randomDigits(pairingCodeDigits)
	expiresAt := time.Now().Add(pairingCodeLifetime)
	if err := s.store.Write(func(st *store.State, _ store.Usage) error {
		st.PairingCodes = append(st.PairingCodes, store.PairingCode{
			Code:            code,
			ExpiresAtMillis: expiresAt.UnixMilli(),
		})
		return nil
	}); err != nil {
		fail(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code":            code,
		"expiresAtMillis": expiresAt.UnixMilli(),
	})
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.Write(func(st *store.State, usage store.Usage) error {
		st.Devices = slices.DeleteFunc(st.Devices, func(d store.Device) bool { return d.ID == id })
		delete(usage, id)
		return nil
	}); err != nil {
		fail(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleEscapeSecret hands the phone the TOTP secret so it can produce unlock codes with
// no network at all. Behind a fresh assertion because this is the one value that, once
// copied, lets someone unlock the TV forever.
func (s *Server) handleEscapeSecret(w http.ResponseWriter, _ *http.Request) {
	var secret string
	s.store.Read(func(st *store.State, _ store.Usage) { secret = st.EscapeSecret })
	if secret == "" {
		fail(w, http.StatusNotFound, "no_secret_yet", "pair a TV first")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"escapeSecretBase32": secret,
		"stepSeconds":        totp.StepSeconds,
		"digits":             totp.Digits,
		"algorithm":          "SHA1",
	})
}

// handleEscapeCode is the online convenience: the server knows the real time, so while
// the phone can reach the Pi it shows the server's code rather than one computed against
// a possibly-wrong phone clock.
func (s *Server) handleEscapeCode(w http.ResponseWriter, _ *http.Request) {
	var secret string
	s.store.Read(func(st *store.State, _ store.Usage) { secret = st.EscapeSecret })
	if secret == "" {
		fail(w, http.StatusNotFound, "no_secret_yet", "pair a TV first")
		return
	}
	decoded, err := totp.DecodeSecret(secret)
	if err != nil {
		fail(w, http.StatusInternalServerError, "bad_secret", err.Error())
		return
	}
	now := time.Now().UnixMilli()
	writeJSON(w, http.StatusOK, map[string]any{
		"code":             totp.Code(decoded, now, totp.StepSeconds, totp.Digits),
		"secondsRemaining": totp.SecondsRemaining(now, totp.StepSeconds),
	})
}

// ---------- validation ----------

// validatePolicy exists because the TV trusts this document. A malformed "25:00" would
// make the Kotlin side throw while parsing a curfew, and a TV that cannot parse its
// policy is a TV that stops enforcing.
func validatePolicy(p *model.Policy) error {
	if !slices.Contains(validModes, p.ListMode) {
		if p.ListMode == "" {
			p.ListMode = "OFF"
		} else {
			return fmt.Errorf("listMode %q must be one of %v", p.ListMode, validModes)
		}
	}
	if p.OfflineBehavior == "" {
		p.OfflineBehavior = "KEEP_ENFORCING"
	}
	if p.OfflineBehavior != "KEEP_ENFORCING" && p.OfflineBehavior != "OPEN_AFTER_GRACE" {
		return fmt.Errorf("offlineBehavior %q is not recognised", p.OfflineBehavior)
	}
	if p.OfflineGraceMinutes < 0 {
		return errors.New("offlineGraceMinutes cannot be negative")
	}

	for day, minutes := range p.DailyMinutes {
		if !slices.Contains(validDays, day) {
			return fmt.Errorf("dailyMinutes has unknown day %q", day)
		}
		if minutes < 0 {
			return fmt.Errorf("dailyMinutes[%s] cannot be negative", day)
		}
	}
	if p.WeeklyMinutes != nil && *p.WeeklyMinutes < 0 {
		return errors.New("weeklyMinutes cannot be negative")
	}
	for pkg, minutes := range p.PerAppDailyMinutes {
		if minutes < 0 {
			return fmt.Errorf("perAppDailyMinutes[%s] cannot be negative", pkg)
		}
	}

	seenIDs := map[string]bool{}
	for i := range p.Curfews {
		curfew := &p.Curfews[i]
		if curfew.ID == "" {
			curfew.ID = randomToken(6)
		}
		if seenIDs[curfew.ID] {
			return fmt.Errorf("two curfews share the id %q", curfew.ID)
		}
		seenIDs[curfew.ID] = true

		if !timeOfDay.MatchString(curfew.Start) {
			return fmt.Errorf("curfew %q has an invalid start %q, want HH:mm", curfew.ID, curfew.Start)
		}
		if !timeOfDay.MatchString(curfew.End) {
			return fmt.Errorf("curfew %q has an invalid end %q, want HH:mm", curfew.ID, curfew.End)
		}
		if len(curfew.Days) == 0 {
			return fmt.Errorf("curfew %q applies to no days, so it would never run", curfew.ID)
		}
		for _, day := range curfew.Days {
			if !slices.Contains(validDays, day) {
				return fmt.Errorf("curfew %q has unknown day %q", curfew.ID, day)
			}
		}
	}

	// Keep the JSON shape stable for the TV: never send null where it expects a list.
	if p.Allowlist == nil {
		p.Allowlist = []string{}
	}
	if p.Blocklist == nil {
		p.Blocklist = []string{}
	}
	if p.ExemptPackages == nil {
		p.ExemptPackages = []string{}
	}
	if p.DailyMinutes == nil {
		p.DailyMinutes = map[string]int{}
	}
	if p.PerAppDailyMinutes == nil {
		p.PerAppDailyMinutes = map[string]int{}
	}
	if p.Curfews == nil {
		p.Curfews = []model.Curfew{}
	}
	return nil
}

// ---------- small helpers ----------

func labelled(totals []store.AppTotal, labels map[string]string, limit int) []store.AppTotal {
	if len(totals) > limit {
		totals = totals[:limit]
	}
	out := make([]store.AppTotal, 0, len(totals))
	for _, total := range totals {
		total.Label = labels[total.PackageName]
		if total.Label == "" {
			total.Label = total.PackageName
		}
		out = append(out, total)
	}
	return out
}

func lastBlocks(events []store.BlockEvent, limit int) []store.BlockEvent {
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	out := make([]store.BlockEvent, len(events))
	for i, event := range events {
		out[len(events)-1-i] = event // newest first
	}
	return out
}

func startOfWeek(t time.Time) time.Time {
	offset := (int(t.Weekday()) + 6) % 7 // Monday = 0
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).AddDate(0, 0, -offset)
}

func boolOr(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}
