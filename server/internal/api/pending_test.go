package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/homelab/braviaguard/internal/auth"
	"github.com/homelab/braviaguard/internal/model"
	"github.com/homelab/braviaguard/internal/store"
)

// signedInParent plants a session straight into the store.
//
// Going through the real ceremony would mean standing up a fake WebAuthn authenticator,
// and that is not what these tests are about: they need a parent app that is signed in
// and freshly verified, so that what they actually exercise is what happens *after* the
// passkey -- which is the entire point of the queue.
func signedInParent(t *testing.T, st *store.Store) string {
	t.Helper()
	const token = "test-session"
	now := time.Now()
	err := st.Write(func(s *store.State, _ store.Usage) error {
		s.Sessions = append(s.Sessions, store.Session{
			Token:               token,
			ParentID:            "test-parent",
			ExpiresAtMillis:     now.Add(time.Hour).UnixMilli(),
			LastAssertionMillis: now.UnixMilli(),
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// doAsParent is do() with the session cookie the parent app would carry.
func doAsParent(
	t *testing.T, method, url, session string, body, out any, wantStatus int,
) {
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
	request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session})

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s = %d, want %d: %s",
			method, url, response.StatusCode, wantStatus, payload)
	}
	if out != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			t.Fatalf("decode %s: %v (%s)", url, err, payload)
		}
	}
}

func setPolicy(t *testing.T, st *store.Store, policy model.Policy) {
	t.Helper()
	err := st.Write(func(s *store.State, _ store.Usage) error {
		s.Policy = policy
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func currentPolicy(t *testing.T, st *store.Store) model.Policy {
	t.Helper()
	var policy model.Policy
	st.Read(func(s *store.State, _ store.Usage) { policy = s.Policy })
	return policy
}

// locked is a policy with something to loosen, so a test can submit a change that
// genuinely weakens it rather than one that only looks different.
func locked() model.Policy {
	policy := model.DefaultPolicy()
	policy.EnforcementEnabled = true
	policy.ListMode = "ALLOWLIST"
	policy.Allowlist = []string{"com.netflix.ninja"}
	policy.DailyMinutes = map[string]int{"MONDAY": 30}
	return policy
}

// submit sends a rule edit the way the parent app does: an ordinary parentWrite, no
// signature anywhere.
func submit(
	t *testing.T, server *httptest.Server, session string,
	policy model.Policy, wantStatus int,
) map[string]any {
	t.Helper()
	var out map[string]any
	doAsParent(t, "PUT", server.URL+"/api/parent/policy", session, policy, &out, wantStatus)
	return out
}

func pendingCount(t *testing.T, st *store.Store) int {
	t.Helper()
	count := 0
	st.Read(func(s *store.State, _ store.Usage) { count = len(s.Pending) })
	return count
}

// ---------- the fallback ----------

func TestRuleEditsStayOnTheOldRulesUntilAPhoneIsEnrolled(t *testing.T) {
	server, st := newTestServer(t)
	// No approver and no session: the old 401, not a queue and not a 403.
	do(t, "PUT", server.URL+"/api/parent/policy", "", model.DefaultPolicy(),
		nil, http.StatusUnauthorized)

	// And with a session, it applies on the spot as it always has -- 200, not 202. This
	// is the half that matters: deploying the queue must not change the parent app for a
	// household that has not enrolled a phone.
	session := signedInParent(t, st)
	setPolicy(t, st, locked())
	loosened := locked()
	loosened.EnforcementEnabled = false
	doAsParent(t, "PUT", server.URL+"/api/parent/policy", session, loosened,
		nil, http.StatusOK)

	if currentPolicy(t, st).EnforcementEnabled {
		t.Fatal("without an approver the edit should have applied immediately")
	}
	if n := pendingCount(t, st); n != 0 {
		t.Fatalf("nothing should have been queued, found %d", n)
	}
}

// ---------- queuing ----------

func TestOnceEnrolledARuleEditIsQueuedRatherThanApplied(t *testing.T) {
	server, st := newTestServer(t)
	session := signedInParent(t, st)
	enrolPhone(t, server, st, "201201")
	setPolicy(t, st, locked())
	before := currentPolicy(t, st)

	loosened := locked()
	loosened.EnforcementEnabled = false
	response := submit(t, server, session, loosened, http.StatusAccepted)

	if response["pendingId"] == nil || response["contentHash"] == nil {
		t.Fatalf("202 carried nothing to approve with: %v", response)
	}
	// The whole point: 202 means nothing happened yet.
	if after := currentPolicy(t, st); after.EnforcementEnabled != before.EnforcementEnabled {
		t.Fatal("a queued rule edit was applied immediately")
	}
	if n := pendingCount(t, st); n != 1 {
		t.Fatalf("pending = %d, want 1", n)
	}
}

func TestTheSummaryNamesEverythingThatLoosensTheRules(t *testing.T) {
	server, st := newTestServer(t)
	session := signedInParent(t, st)
	enrolPhone(t, server, st, "202202")

	strict := locked()
	strict.Curfews = []model.Curfew{{
		ID: "c1", Label: "就寝", Days: []string{"MONDAY"}, Start: "21:00", End: "07:00",
	}}
	setPolicy(t, st, strict)

	// Five separate loosenings in one submission. A parent approving this has to be able
	// to see all five, because the signature covers the lot.
	loose := strict
	loose.EnforcementEnabled = false
	loose.ListMode = "OFF"
	loose.Allowlist = []string{"com.netflix.ninja", "com.google.android.youtube.tv"}
	loose.DailyMinutes = map[string]int{"MONDAY": 600}
	loose.Curfews = []model.Curfew{}

	response := submit(t, server, session, loose, http.StatusAccepted)
	summary, _ := response["summary"].([]any)
	joined := ""
	for _, line := range summary {
		joined += line.(string) + "\n"
	}

	for _, want := range []string{
		"拦截总开关",                         // enforcement going off
		"OFF",                           // list mode going off
		"com.google.android.youtube.tv", // an app being allowed
		"600",                           // a budget going up
		"宵禁",                            // a curfew disappearing
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the summary never mentions %q:\n%s", want, joined)
		}
	}
}

// ---------- approving ----------

func TestAnApprovedRuleEditActuallyRuns(t *testing.T) {
	server, st := newTestServer(t)
	session := signedInParent(t, st)
	p := enrolPhone(t, server, st, "203203")
	setPolicy(t, st, locked())

	loosened := locked()
	loosened.EnforcementEnabled = false
	queued := submit(t, server, session, loosened, http.StatusAccepted)

	p.approve(t, server, "POST",
		"/api/parent/pending/"+queued["pendingId"].(string)+"/approve",
		map[string]any{"contentHash": queued["contentHash"]}, http.StatusOK)

	after := currentPolicy(t, st)
	if after.EnforcementEnabled {
		t.Fatal("the approved edit did not take effect")
	}
	// The version is the server's to assign; an approved edit is still an edit.
	if after.Version <= locked().Version {
		t.Fatalf("policy version did not move: %d", after.Version)
	}
	if n := pendingCount(t, st); n != 0 {
		t.Fatalf("the queue still holds %d after approval", n)
	}
}

func TestApprovingNeedsASignature(t *testing.T) {
	server, st := newTestServer(t)
	session := signedInParent(t, st)
	p := enrolPhone(t, server, st, "204204")
	setPolicy(t, st, locked())

	loosened := locked()
	loosened.EnforcementEnabled = false
	queued := submit(t, server, session, loosened, http.StatusAccepted)

	// The approver's bearer token on its own. Reading the queue is fine; approving from
	// it is the thing a passcode must not be able to reach.
	do(t, "POST",
		server.URL+"/api/parent/pending/"+queued["pendingId"].(string)+"/approve",
		p.token, map[string]any{"contentHash": queued["contentHash"]},
		nil, http.StatusForbidden)

	if !currentPolicy(t, st).EnforcementEnabled {
		t.Fatal("an unsigned approval ran the edit")
	}
}

func TestApprovingOneChangeDoesNotRunAnother(t *testing.T) {
	server, st := newTestServer(t)
	session := signedInParent(t, st)
	p := enrolPhone(t, server, st, "205205")
	setPolicy(t, st, locked())

	harmless := locked()
	harmless.Allowlist = []string{"com.netflix.ninja", "tv.ifvod.classic"}
	first := submit(t, server, session, harmless, http.StatusAccepted)

	dangerous := locked()
	dangerous.EnforcementEnabled = false
	second := submit(t, server, session, dangerous, http.StatusAccepted)

	// Approve the harmless one. The dangerous one must be untouched and unexecuted --
	// this is the attack of queuing something nasty and hoping it rides along.
	p.approve(t, server, "POST",
		"/api/parent/pending/"+first["pendingId"].(string)+"/approve",
		map[string]any{"contentHash": first["contentHash"]}, http.StatusOK)

	after := currentPolicy(t, st)
	if !after.EnforcementEnabled {
		t.Fatal("approving one change executed the other")
	}
	if len(after.Allowlist) != 2 {
		t.Fatalf("the approved change did not run: allowlist = %v", after.Allowlist)
	}
	if _, stillThere := pendingByID(t, st, second["pendingId"].(string)); !stillThere {
		t.Fatal("the unapproved change vanished from the queue")
	}
}

func TestApprovingWithTheWrongContentHashIsRefused(t *testing.T) {
	server, st := newTestServer(t)
	session := signedInParent(t, st)
	p := enrolPhone(t, server, st, "206206")
	setPolicy(t, st, locked())

	loosened := locked()
	loosened.EnforcementEnabled = false
	queued := submit(t, server, session, loosened, http.StatusAccepted)

	// The hash is what ties the thing approved to the thing read. A mismatch has to stop
	// the execution, not merely be logged.
	p.approve(t, server, "POST",
		"/api/parent/pending/"+queued["pendingId"].(string)+"/approve",
		map[string]any{"contentHash": "0000"}, http.StatusConflict)

	if !currentPolicy(t, st).EnforcementEnabled {
		t.Fatal("a mismatched hash still ran the edit")
	}
}

func TestAnApprovalCannotBeUsedTwice(t *testing.T) {
	server, st := newTestServer(t)
	session := signedInParent(t, st)
	p := enrolPhone(t, server, st, "207207")
	setPolicy(t, st, locked())

	first := locked()
	first.Allowlist = []string{"com.netflix.ninja", "tv.ifvod.classic"}
	queued := submit(t, server, session, first, http.StatusAccepted)
	path := "/api/parent/pending/" + queued["pendingId"].(string) + "/approve"
	body := map[string]any{"contentHash": queued["contentHash"]}

	p.approve(t, server, "POST", path, body, http.StatusOK)
	// A second, freshly signed approval of a change that is no longer queued.
	p.approve(t, server, "POST", path, body, http.StatusConflict)
}

func TestAnExpiredChangeCannotBeApproved(t *testing.T) {
	server, st := newTestServer(t)
	p := enrolPhone(t, server, st, "208208")
	setPolicy(t, st, locked())

	loosened := locked()
	loosened.EnforcementEnabled = false
	body, err := json.Marshal(loosened)
	if err != nil {
		t.Fatal(err)
	}
	// Planted already expired, because waiting out pendingLifetime is an hour.
	hash := contentHash(actionPutPolicy, body)
	err = st.Write(func(s *store.State, _ store.Usage) error {
		s.Pending = append(s.Pending, store.PendingChange{
			ID: "stale", Action: actionPutPolicy, Body: body, ContentHash: hash,
			ExpiresAtMillis: time.Now().Add(-time.Second).UnixMilli(),
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	p.approve(t, server, "POST", "/api/parent/pending/stale/approve",
		map[string]any{"contentHash": hash}, http.StatusConflict)

	if !currentPolicy(t, st).EnforcementEnabled {
		t.Fatal("an expired change ran")
	}
}

// ---------- the whitelist ----------

func TestOnlyWhitelistedActionsCanBeExecuted(t *testing.T) {
	server, st := newTestServer(t)
	p := enrolPhone(t, server, st, "209209")
	setPolicy(t, st, locked())

	// The queue stores a method, a path and a body, which is the shape of an arbitrary
	// request. This is the test that it is not one: planted by hand, as someone with
	// write access to state.json could, and naming a real and destructive endpoint.
	body := []byte(`{"enabled":false}`)
	hash := contentHash("POST /api/parent/enforcement", body)
	err := st.Write(func(s *store.State, _ store.Usage) error {
		s.Pending = append(s.Pending, store.PendingChange{
			ID: "planted", Action: "POST /api/parent/enforcement",
			Body: body, ContentHash: hash,
			ExpiresAtMillis: time.Now().Add(time.Hour).UnixMilli(),
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	p.approve(t, server, "POST", "/api/parent/pending/planted/approve",
		map[string]any{"contentHash": hash}, http.StatusConflict)

	if !currentPolicy(t, st).EnforcementEnabled {
		t.Fatal("an action outside approvableAction was executed")
	}
}

// ---------- rejecting ----------

func TestRejectingNeedsNoFace(t *testing.T) {
	server, st := newTestServer(t)
	session := signedInParent(t, st)
	enrolPhone(t, server, st, "210210")
	setPolicy(t, st, locked())

	loosened := locked()
	loosened.EnforcementEnabled = false
	queued := submit(t, server, session, loosened, http.StatusAccepted)

	// Throwing a change away changes nothing, so it does not need an approval. Reached
	// here without a session it is still a 401 -- parentWrite, same as before.
	do(t, "POST",
		server.URL+"/api/parent/pending/"+queued["pendingId"].(string)+"/reject",
		"", nil, nil, http.StatusUnauthorized)
	if n := pendingCount(t, st); n != 1 {
		t.Fatalf("an unauthenticated reject removed it anyway")
	}
}

// ---------- reading the queue ----------

func TestTheApproverCanReadTheQueue(t *testing.T) {
	server, st := newTestServer(t)
	session := signedInParent(t, st)
	p := enrolPhone(t, server, st, "211211")
	setPolicy(t, st, locked())

	loosened := locked()
	loosened.EnforcementEnabled = false
	submit(t, server, session, loosened, http.StatusAccepted)

	var listing struct {
		Pending []pendingView `json:"pending"`
	}
	do(t, "GET", server.URL+"/api/approver/pending", p.token, nil, &listing, http.StatusOK)
	if len(listing.Pending) != 1 {
		t.Fatalf("queue = %d entries, want 1", len(listing.Pending))
	}
	if len(listing.Pending[0].Summary) == 0 {
		t.Fatal("a queued change arrived with no summary -- nothing to judge it by")
	}
	// The body is deliberately absent from the view: the app shows the summary, and the
	// hash is what binds the approval to the content.
	if listing.Pending[0].ContentHash == "" {
		t.Fatal("the view carried no content hash")
	}
}

func TestTheApproverCanRejectWithoutAFace(t *testing.T) {
	server, st := newTestServer(t)
	session := signedInParent(t, st)
	p := enrolPhone(t, server, st, "212212")
	setPolicy(t, st, locked())

	loosened := locked()
	loosened.EnforcementEnabled = false
	queued := submit(t, server, session, loosened, http.StatusAccepted)

	// No signature, just the token. Saying no has to be cheap, or a queue nobody can
	// clear becomes a queue people approve without reading.
	do(t, "POST",
		server.URL+"/api/approver/pending/"+queued["pendingId"].(string)+"/reject",
		p.token, nil, nil, http.StatusNoContent)

	if n := pendingCount(t, st); n != 0 {
		t.Fatalf("the queue still holds %d after a reject", n)
	}
	if !currentPolicy(t, st).EnforcementEnabled {
		t.Fatal("rejecting ran the change")
	}
}

func pendingByID(t *testing.T, st *store.Store, id string) (store.PendingChange, bool) {
	t.Helper()
	var found store.PendingChange
	var ok bool
	st.Read(func(s *store.State, _ store.Usage) { found, ok = s.PendingByID(id) })
	return found, ok
}
