package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"net/http/httptest"

	"github.com/homelab/braviaguard/internal/auth"
	"github.com/homelab/braviaguard/internal/model"
	"github.com/homelab/braviaguard/internal/store"
	"github.com/homelab/braviaguard/web"
)

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authManager, err := auth.NewManager(
		st, "example.com", "BraviaGuard", []string{"https://example.com"}, "setup-code", true,
	)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(st, authManager, web.Assets, time.UTC).Handler())
	t.Cleanup(server.Close)
	return server, st
}

func do(t *testing.T, method, url, token string, body, out any, wantStatus int) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)

	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s = %d, want %d: %s", method, url, response.StatusCode, wantStatus, payload)
	}
	if out != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			t.Fatalf("decode %s: %v (%s)", url, err, payload)
		}
	}
}

func givePairingCode(t *testing.T, st *store.Store, code string) {
	t.Helper()
	err := st.Write(func(s *store.State, _ store.Usage) error {
		s.PairingCodes = append(s.PairingCodes, store.PairingCode{
			Code:            code,
			ExpiresAtMillis: time.Now().Add(time.Minute).UnixMilli(),
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func enrol(t *testing.T, server *httptest.Server, st *store.Store) model.EnrollResponse {
	t.Helper()
	givePairingCode(t, st, "424242")
	var response model.EnrollResponse
	do(t, "POST", server.URL+"/api/device/enroll", "",
		model.EnrollRequest{PairingCode: "424242", DeviceName: "BRAVIA", Model: "XR-55A80J"},
		&response, http.StatusOK)
	return response
}

// ---------- the television's whole conversation ----------

func TestEnrollThenSync(t *testing.T) {
	server, st := newTestServer(t)
	enrolled := enrol(t, server, st)

	if enrolled.Token == "" || enrolled.DeviceID == "" {
		t.Fatalf("enroll returned nothing usable: %+v", enrolled)
	}
	if enrolled.EscapeSecretBase32 == "" {
		t.Error("the TV must receive the escape secret, or offline codes cannot work")
	}

	var first model.SyncResponse
	do(t, "GET", server.URL+"/api/device/sync?deviceId="+enrolled.DeviceID+"&policyVersion=0",
		enrolled.Token, nil, &first, http.StatusOK)
	if first.Policy == nil {
		t.Fatal("a TV that knows no version must be sent the policy")
	}

	// Having it already, the TV should get a response with no policy in it at all —
	// this is what keeps a 30-second poll cheap.
	var second model.SyncResponse
	do(t, "GET", server.URL+"/api/device/sync?deviceId="+enrolled.DeviceID+
		"&policyVersion="+strconv.FormatInt(first.Policy.Version, 10),
		enrolled.Token, nil, &second, http.StatusOK)
	if second.Policy != nil {
		t.Error("policy should be omitted when the TV is already current")
	}
	if second.ServerTimeMillis == 0 {
		t.Error("serverTimeMillis is always sent; the TV uses it to spot clock skew")
	}
}

func TestTokenIsNotStoredInClear(t *testing.T) {
	server, st := newTestServer(t)
	enrolled := enrol(t, server, st)
	st.Read(func(s *store.State, _ store.Usage) {
		device := s.DeviceByID(enrolled.DeviceID)
		if device == nil {
			t.Fatal("device was not recorded")
		}
		if device.TokenHash == enrolled.Token {
			t.Error("the bearer token is stored verbatim; only its hash should be kept")
		}
		if device.TokenHash != hashToken(enrolled.Token) {
			t.Error("stored hash does not match the issued token")
		}
	})
}

func TestPairingCodeCannotBeReused(t *testing.T) {
	server, st := newTestServer(t)
	_ = enrol(t, server, st)
	// Same code again, without re-issuing it.
	do(t, "POST", server.URL+"/api/device/enroll", "",
		model.EnrollRequest{PairingCode: "424242", DeviceName: "Second TV"},
		nil, http.StatusForbidden)
}

func TestDeviceEndpointsRejectBadTokens(t *testing.T) {
	server, st := newTestServer(t)
	enrolled := enrol(t, server, st)

	do(t, "GET", server.URL+"/api/device/sync?deviceId="+enrolled.DeviceID,
		"", nil, nil, http.StatusUnauthorized)
	do(t, "GET", server.URL+"/api/device/sync?deviceId="+enrolled.DeviceID,
		"not-a-real-token", nil, nil, http.StatusUnauthorized)
}

func TestUsageReportLandsInTheStore(t *testing.T) {
	server, st := newTestServer(t)
	enrolled := enrol(t, server, st)

	do(t, "POST", server.URL+"/api/device/usage", enrolled.Token, model.UsageReportRequest{
		DeviceID: enrolled.DeviceID,
		Days: []model.DayUsage{{
			Day:           "2026-11-03",
			PerAppSeconds: map[string]int64{"com.netflix.ninja": 1800},
		}},
		Labels: map[string]string{"com.netflix.ninja": "Netflix"},
	}, nil, http.StatusNoContent)

	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	st.Read(func(s *store.State, usage store.Usage) {
		if got := usage[enrolled.DeviceID]["2026-11-03"]["com.netflix.ninja"]; got != 1800 {
			t.Errorf("stored usage = %d, want 1800", got)
		}
		// Labels are what let the parent app show "Netflix" instead of the package name.
		if s.AppLabels["com.netflix.ninja"] != "Netflix" {
			t.Errorf("app label was not recorded: %v", s.AppLabels)
		}
	})
}

func TestBlockEventLandsInTheStore(t *testing.T) {
	server, st := newTestServer(t)
	enrolled := enrol(t, server, st)

	do(t, "POST", server.URL+"/api/device/block-event", enrolled.Token, model.BlockEventRequest{
		DeviceID:    enrolled.DeviceID,
		PackageName: "com.netflix.ninja",
		AppLabel:    "Netflix",
		Reason:      "CURFEW",
		AtMillis:    time.Now().UnixMilli(),
	}, nil, http.StatusNoContent)

	st.Read(func(s *store.State, _ store.Usage) {
		if len(s.RecentBlocks) != 1 || s.RecentBlocks[0].Reason != "CURFEW" {
			t.Errorf("block events = %+v", s.RecentBlocks)
		}
	})
}

// ---------- the parent surface is shut without a passkey ----------

func TestParentReadsNeedASession(t *testing.T) {
	server, _ := newTestServer(t)
	for _, path := range []string{"/api/parent/state", "/api/parent/usage", "/api/parent/apps"} {
		var apiError model.APIError
		do(t, "GET", server.URL+path, "", nil, &apiError, http.StatusUnauthorized)
		if apiError.Error != "signin_required" {
			t.Errorf("%s said %q, want signin_required", path, apiError.Error)
		}
	}
}

func TestParentWritesNeedASession(t *testing.T) {
	server, _ := newTestServer(t)
	do(t, "POST", server.URL+"/api/parent/grants", "",
		map[string]any{"minutes": 30}, nil, http.StatusUnauthorized)
	do(t, "POST", server.URL+"/api/parent/enforcement", "",
		map[string]any{"enabled": true}, nil, http.StatusUnauthorized)
	do(t, "PUT", server.URL+"/api/parent/policy", "",
		model.DefaultPolicy(), nil, http.StatusUnauthorized)
	do(t, "POST", server.URL+"/api/parent/pairing-code", "", nil, nil, http.StatusUnauthorized)
	do(t, "GET", server.URL+"/api/parent/escape-secret", "", nil, nil, http.StatusUnauthorized)
}

func TestBootstrapIsOpenAndReportsSetupNeeded(t *testing.T) {
	server, _ := newTestServer(t)
	var bootstrap struct {
		NeedsSetup bool `json:"needsSetup"`
		SignedIn   bool `json:"signedIn"`
	}
	do(t, "GET", server.URL+"/api/parent/bootstrap", "", nil, &bootstrap, http.StatusOK)
	if !bootstrap.NeedsSetup || bootstrap.SignedIn {
		t.Errorf("bootstrap = %+v, want needsSetup and not signedIn", bootstrap)
	}
}

// Dismissing the Face ID prompt is an ordinary thing to do. If the half-finished
// record counted as a registered parent, the setup code would stop working while no
// credential existed to sign in with, and first-time setup would be stuck for good.
func TestAbandonedRegistrationDoesNotLockOutSetup(t *testing.T) {
	server, st := newTestServer(t)

	// begin, then never finish -- the browser prompt was dismissed.
	do(t, "POST", server.URL+"/api/parent/register/begin", "",
		map[string]any{"name": "Mum", "setupCode": "setup-code"}, nil, http.StatusOK)

	var dangling int
	st.Read(func(s *store.State, _ store.Usage) { dangling = len(s.Parents) })
	if dangling != 1 {
		t.Fatalf("expected the half-finished record to exist, got %d", dangling)
	}

	var bootstrap struct {
		NeedsSetup bool `json:"needsSetup"`
	}
	do(t, "GET", server.URL+"/api/parent/bootstrap", "", nil, &bootstrap, http.StatusOK)
	if !bootstrap.NeedsSetup {
		t.Error("setup must still be available after an abandoned registration")
	}

	// And retrying with the setup code must still be accepted, not demand a session.
	do(t, "POST", server.URL+"/api/parent/register/begin", "",
		map[string]any{"name": "Mum", "setupCode": "setup-code"}, nil, http.StatusOK)

	// The retry should have swept the first stub rather than stacking another.
	st.Read(func(s *store.State, _ store.Usage) { dangling = len(s.Parents) })
	if dangling != 1 {
		t.Errorf("abandoned records accumulated: %d", dangling)
	}
}

func TestRegistrationRefusesAWrongSetupCode(t *testing.T) {
	server, _ := newTestServer(t)
	var apiError model.APIError
	do(t, "POST", server.URL+"/api/parent/register/begin", "",
		map[string]any{"name": "Mum", "setupCode": "wrong"}, &apiError, http.StatusForbidden)
	if apiError.Error != "bad_setup_code" {
		t.Errorf("error = %q, want bad_setup_code", apiError.Error)
	}
}

// ---------- serving the app itself ----------

func TestParentAppIsServed(t *testing.T) {
	server, _ := newTestServer(t)
	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d", response.StatusCode)
	}
	if !strings.Contains(string(body), "电视守护") {
		t.Error("the embedded parent app was not served")
	}
	if cache := response.Header.Get("Cache-Control"); cache != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store so a new build is picked up", cache)
	}
}

func TestManifestIsServedWithTheRightType(t *testing.T) {
	server, _ := newTestServer(t)
	response, err := http.Get(server.URL + "/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	// text/plain here means the PWA installs as a bookmark instead of an app.
	if got := response.Header.Get("Content-Type"); got != "application/manifest+json" {
		t.Errorf("Content-Type = %q, want application/manifest+json", got)
	}
}

func TestHealthz(t *testing.T) {
	server, _ := newTestServer(t)
	do(t, "GET", server.URL+"/healthz", "", nil, nil, http.StatusOK)
}

// ---------- policy validation: the TV trusts this document ----------

func TestValidatePolicyAcceptsAReasonablePolicy(t *testing.T) {
	weekly := 600
	policy := model.Policy{
		ListMode:      "BLOCKLIST",
		Blocklist:     []string{"com.netflix.ninja"},
		DailyMinutes:  map[string]int{"MONDAY": 60},
		WeeklyMinutes: &weekly,
		Curfews: []model.Curfew{
			{ID: "bedtime", Days: []string{"FRIDAY"}, Start: "21:30", End: "06:30"},
		},
	}
	if err := validatePolicy(&policy); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Nil collections are filled in, so the Kotlin side never sees null where it
	// expects a list.
	if policy.Allowlist == nil || policy.ExemptPackages == nil || policy.PerAppDailyMinutes == nil {
		t.Error("nil collections should have been normalised")
	}
	if policy.OfflineBehavior != "KEEP_ENFORCING" {
		t.Errorf("offlineBehavior default = %q", policy.OfflineBehavior)
	}
}

func TestValidatePolicyRejectsWhatWouldBreakATV(t *testing.T) {
	weekly := -1
	cases := map[string]model.Policy{
		"bad list mode":  {ListMode: "SOMETHING"},
		"bad start time": {Curfews: []model.Curfew{{ID: "a", Days: []string{"MONDAY"}, Start: "25:00", End: "06:00"}}},
		"bad end time":   {Curfews: []model.Curfew{{ID: "a", Days: []string{"MONDAY"}, Start: "21:00", End: "6:0"}}},
		"no days":        {Curfews: []model.Curfew{{ID: "a", Days: []string{}, Start: "21:00", End: "22:00"}}},
		"unknown day":    {Curfews: []model.Curfew{{ID: "a", Days: []string{"FUNDAY"}, Start: "21:00", End: "22:00"}}},
		"unpadded hour":  {Curfews: []model.Curfew{{ID: "a", Days: []string{"MONDAY"}, Start: "1:00", End: "02:00"}}},
		"duplicate id": {Curfews: []model.Curfew{
			{ID: "a", Days: []string{"MONDAY"}, Start: "01:00", End: "02:00"},
			{ID: "a", Days: []string{"TUESDAY"}, Start: "03:00", End: "04:00"},
		}},
		"negative daily":   {DailyMinutes: map[string]int{"MONDAY": -5}},
		"unknown day key":  {DailyMinutes: map[string]int{"CATURDAY": 30}},
		"negative weekly":  {WeeklyMinutes: &weekly},
		"negative per app": {PerAppDailyMinutes: map[string]int{"x": -1}},
		"bad offline mode": {OfflineBehavior: "WHATEVER"},
		"negative grace":   {OfflineGraceMinutes: -1, OfflineBehavior: "KEEP_ENFORCING"},
	}
	for name, policy := range cases {
		p := policy
		if err := validatePolicy(&p); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}
}

func TestValidatePolicyAssignsMissingCurfewIDs(t *testing.T) {
	policy := model.Policy{
		Curfews: []model.Curfew{{Days: []string{"MONDAY"}, Start: "18:00", End: "20:00"}},
	}
	if err := validatePolicy(&policy); err != nil {
		t.Fatal(err)
	}
	if policy.Curfews[0].ID == "" {
		t.Error("a curfew with no id should be given one")
	}
}

func TestStartOfWeekIsMonday(t *testing.T) {
	sunday := time.Date(2026, 11, 8, 23, 0, 0, 0, time.UTC)
	if got := startOfWeek(sunday); got.Day() != 2 || got.Month() != time.November {
		t.Errorf("startOfWeek(Sun 2026-11-08) = %v, want 2026-11-02", got)
	}
	monday := time.Date(2026, 11, 2, 0, 30, 0, 0, time.UTC)
	if got := startOfWeek(monday); got.Day() != 2 {
		t.Errorf("startOfWeek(Mon) = %v, want the same day", got)
	}
}

// ---------- proxy / secure context ----------

func newRedirectingServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot) // unmistakably "the redirect did not fire"
	})
	server := httptest.NewServer(RequireHTTPS("https://tv.example.com", inner))
	t.Cleanup(server.Close)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return server, client
}

func forwarded(t *testing.T, client *http.Client, url, proto, path string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if proto != "" {
		request.Header.Set("X-Forwarded-Proto", proto)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestPlainHTTPIsRedirectedToTheCanonicalOrigin(t *testing.T) {
	server, client := newRedirectingServer(t)
	response := forwarded(t, client, server.URL, "http", "/api/parent/bootstrap?x=1")

	if response.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want 308", response.StatusCode)
	}
	// The path and query have to survive, or a deep link lands on the home screen.
	if got := response.Header.Get("Location"); got != "https://tv.example.com/api/parent/bootstrap?x=1" {
		t.Fatalf("Location = %q", got)
	}
}

// The televisions talk to this process directly on the LAN, with no proxy and so no
// X-Forwarded-Proto. Redirecting them to the public https origin would break
// enforcement exactly when the broadband is down.
func TestDirectLANRequestsAreNotRedirected(t *testing.T) {
	server, client := newRedirectingServer(t)
	for _, proto := range []string{"", "https", "HTTPS", "https,http"} {
		response := forwarded(t, client, server.URL, proto, "/api/device/sync")
		if response.StatusCode != http.StatusTeapot {
			t.Errorf("X-Forwarded-Proto %q: status = %d, want the request to pass through",
				proto, response.StatusCode)
		}
	}
}

func TestRedirectLooksAtTheBrowsersSchemeNotTheLastProxys(t *testing.T) {
	server, client := newRedirectingServer(t)
	// Two proxies deep: the browser spoke http, the hop in between spoke https.
	response := forwarded(t, client, server.URL, "http, https", "/")
	if response.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want 308", response.StatusCode)
	}
}

// A CDN in front fills in its own Cache-Control where the origin says nothing, which
// would let a stale app.js outlive an index.html that is always fresh.
func TestShellAssetsAreNotCacheable(t *testing.T) {
	server, _ := newTestServer(t)
	for _, path := range []string{"/", "/app.js", "/styles.css", "/sw.js", "/manifest.webmanifest", "/icon.svg"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d", path, response.StatusCode)
			continue
		}
		if got := response.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", path, got)
		}
	}
}
