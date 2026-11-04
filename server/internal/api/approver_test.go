package api

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/homelab/braviaguard/internal/model"
	"github.com/homelab/braviaguard/internal/store"
)

// phone stands in for the iPhone's Secure Enclave. The key is an ordinary software key
// here, which is exactly right for these tests: the server cannot tell the difference, so
// testing against a software key tests what the server actually checks.
type phone struct {
	key   *ecdsa.PrivateKey
	id    string
	token string
}

func newPhone(t *testing.T) *phone {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &phone{key: key}
}

// publicKey mimics SecKeyCopyExternalRepresentation, which hands back the uncompressed
// ANSI X9.63 point rather than anything DER-wrapped.
func (p *phone) publicKey(t *testing.T) []byte {
	t.Helper()
	key, err := p.key.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	return key.PublicKey().Bytes()
}

// sign mimics kSecKeyAlgorithmECDSASignatureMessageX962SHA256: hash the message with
// SHA-256, sign, hand back DER.
func (p *phone) sign(t *testing.T, payload []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(payload)
	signature, err := ecdsa.SignASN1(rand.Reader, p.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signature
}

func giveApproverCode(t *testing.T, st *store.Store, code string) {
	t.Helper()
	err := st.Write(func(s *store.State, _ store.Usage) error {
		s.ApproverCodes = append(s.ApproverCodes, store.PairingCode{
			Code:            code,
			ExpiresAtMillis: time.Now().Add(time.Minute).UnixMilli(),
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type enrollResult struct {
	ApproverID string `json:"approverId"`
	Token      string `json:"token"`
}

// enrolPhone takes the phone all the way through enrolment, using a code dropped straight
// into the store: minting one through the API needs a parent session, which these tests
// have no way to produce.
func enrolPhone(t *testing.T, server *httptest.Server, st *store.Store, code string) *phone {
	t.Helper()
	p := newPhone(t)
	giveApproverCode(t, st, code)
	var result enrollResult
	do(t, "POST", server.URL+"/api/approver/enroll", "", map[string]any{
		"code":      code,
		"name":      "爸爸的 iPhone",
		"publicKey": p.publicKey(t),
		"signature": p.sign(t, approvalPayload(code, "POST", "/api/approver/enroll", nil)),
	}, &result, http.StatusOK)
	if result.ApproverID == "" || result.Token == "" {
		t.Fatalf("enrolment returned nothing useful: %+v", result)
	}
	p.id, p.token = result.ApproverID, result.Token
	return p
}

// challenge asks for a nonce the way the app does.
func (p *phone) challenge(t *testing.T, server *httptest.Server) (string, string) {
	t.Helper()
	var result struct {
		ChallengeID string `json:"challengeId"`
		Nonce       string `json:"nonce"`
	}
	do(t, "POST", server.URL+"/api/approver/challenge", p.token, nil, &result, http.StatusOK)
	return result.ChallengeID, result.Nonce
}

// send performs one signed request. The signature covers the body actually sent, so
// passing signedBody different from body is how the binding tests lie to the server.
func (p *phone) send(
	t *testing.T,
	server *httptest.Server,
	method, path string,
	body, signedBody any,
	challengeID, nonce string,
	wantStatus int,
) []byte {
	t.Helper()

	encode := func(value any) []byte {
		if value == nil {
			return nil
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	sent, signed := encode(body), encode(signedBody)

	var reader io.Reader
	if sent != nil {
		reader = bytes.NewReader(sent)
	}
	request, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if sent != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
	request.Header.Set("X-Approver-Id", p.id)
	request.Header.Set("X-Approver-Challenge", challengeID)
	request.Header.Set("X-Approver-Signature", base64.StdEncoding.EncodeToString(
		p.sign(t, approvalPayload(nonce, method, path, signed))))

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s = %d, want %d: %s",
			method, path, response.StatusCode, wantStatus, payload)
	}
	return payload
}

// approve is the ordinary path: fetch a nonce, sign the body being sent, send it.
func (p *phone) approve(
	t *testing.T, server *httptest.Server, method, path string, body any, wantStatus int,
) []byte {
	t.Helper()
	challengeID, nonce := p.challenge(t, server)
	return p.send(t, server, method, path, body, body, challengeID, nonce, wantStatus)
}

// enableEnforcement turns the switch on. A brand-new store has EnforcementEnabled false,
// so a test that wants to prove the switch cannot be turned *off* has to turn it on first
// or it proves nothing.
func enableEnforcement(t *testing.T, st *store.Store) {
	t.Helper()
	err := st.Write(func(s *store.State, _ store.Usage) error {
		s.Policy.EnforcementEnabled = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func enforcing(t *testing.T, st *store.Store) bool {
	t.Helper()
	on := false
	st.Read(func(s *store.State, _ store.Usage) { on = s.Policy.EnforcementEnabled })
	return on
}

func grantCount(t *testing.T, st *store.Store) int {
	t.Helper()
	count := 0
	st.Read(func(s *store.State, _ store.Usage) { count = len(s.Grants) })
	return count
}

// ---------- enrolment ----------

func TestEnrolmentNeedsTheKeyToSignItsOwnCode(t *testing.T) {
	server, st := newTestServer(t)
	giveApproverCode(t, st, "111111")

	enroller, impostor := newPhone(t), newPhone(t)
	// The impostor signs; the enroller's public key is the one registered.
	do(t, "POST", server.URL+"/api/approver/enroll", "", map[string]any{
		"code":      "111111",
		"publicKey": enroller.publicKey(t),
		"signature": impostor.sign(t, approvalPayload("111111", "POST", "/api/approver/enroll", nil)),
	}, nil, http.StatusForbidden)

	var approvers, codes int
	st.Read(func(s *store.State, _ store.Usage) {
		approvers, codes = len(s.Approvers), len(s.ApproverCodes)
	})
	if approvers != 0 {
		t.Fatalf("a key that did not sign its own code got enrolled anyway")
	}
	// The code must survive a request that never proved anything, or a single fat-finger
	// would burn it.
	if codes != 1 {
		t.Fatalf("a refused enrolment spent the code: %d codes left, want 1", codes)
	}
}

func TestApproverCodeIsSingleUse(t *testing.T) {
	server, st := newTestServer(t)
	enrolPhone(t, server, st, "222222")

	second := newPhone(t)
	do(t, "POST", server.URL+"/api/approver/enroll", "", map[string]any{
		"code":      "222222",
		"publicKey": second.publicKey(t),
		"signature": second.sign(t, approvalPayload("222222", "POST", "/api/approver/enroll", nil)),
	}, nil, http.StatusForbidden)
}

func TestApproverTokenIsNotStoredInClear(t *testing.T) {
	server, st := newTestServer(t)
	p := enrolPhone(t, server, st, "333333")

	st.Read(func(s *store.State, _ store.Usage) {
		for _, a := range s.Approvers {
			if a.TokenHash == p.token {
				t.Fatal("the approver token is stored in clear")
			}
		}
	})
}

// ---------- the gate ----------

func TestGrantsStayOnTheOldRulesUntilAPhoneIsEnrolled(t *testing.T) {
	server, _ := newTestServer(t)
	// No approver, no session: needsApproval must fall through to parentWrite, which is
	// what the parent app has always got. Not 403 -- that would mean the fallback broke
	// and the household just lost the ability to unlock anything.
	do(t, "POST", server.URL+"/api/parent/grants", "",
		map[string]any{"minutes": 30}, nil, http.StatusUnauthorized)
}

func TestOnceEnrolledAGrantWithoutASignatureIsRefused(t *testing.T) {
	server, st := newTestServer(t)
	p := enrolPhone(t, server, st, "444444")

	// The approver's own bearer token, which is enough to read state, must not be enough
	// to hand out television time.
	do(t, "POST", server.URL+"/api/parent/grants", p.token,
		map[string]any{"minutes": 30}, nil, http.StatusForbidden)
	if n := grantCount(t, st); n != 0 {
		t.Fatalf("an unsigned request created %d grants", n)
	}
}

func TestASignedGrantIsAccepted(t *testing.T) {
	server, st := newTestServer(t)
	p := enrolPhone(t, server, st, "555555")

	var grant model.Grant
	payload := p.approve(t, server, "POST", "/api/parent/grants",
		map[string]any{"minutes": 30, "note": "功课做完了"}, http.StatusOK)
	if err := json.Unmarshal(payload, &grant); err != nil {
		t.Fatal(err)
	}
	if grant.ID == "" {
		t.Fatal("no grant came back")
	}
	if n := grantCount(t, st); n != 1 {
		t.Fatalf("grants in store = %d, want 1", n)
	}
}

func TestASignatureCannotBeReplayed(t *testing.T) {
	server, st := newTestServer(t)
	p := enrolPhone(t, server, st, "666666")

	body := map[string]any{"minutes": 30}
	challengeID, nonce := p.challenge(t, server)
	p.send(t, server, "POST", "/api/parent/grants", body, body, challengeID, nonce, http.StatusOK)
	// The same nonce and the same signature, a second time. One Face ID prompt must buy
	// exactly one approval.
	p.send(t, server, "POST", "/api/parent/grants", body, body, challengeID, nonce,
		http.StatusForbidden)

	if n := grantCount(t, st); n != 1 {
		t.Fatalf("a replayed approval produced %d grants, want 1", n)
	}
}

func TestAFailedSignatureBurnsTheChallengeToo(t *testing.T) {
	server, st := newTestServer(t)
	p := enrolPhone(t, server, st, "676767")
	impostor := newPhone(t)
	impostor.id, impostor.token = p.id, p.token

	challengeID, nonce := p.challenge(t, server)
	// Wrong key: refused.
	impostor.send(t, server, "POST", "/api/parent/grants",
		map[string]any{"minutes": 30}, map[string]any{"minutes": 30},
		challengeID, nonce, http.StatusForbidden)
	// The real phone cannot now reuse that challenge either. Otherwise a nonce would
	// survive an unlimited number of guesses.
	body := map[string]any{"minutes": 30}
	p.send(t, server, "POST", "/api/parent/grants", body, body, challengeID, nonce,
		http.StatusForbidden)
}

func TestASignatureIsBoundToTheRequestBody(t *testing.T) {
	server, st := newTestServer(t)
	p := enrolPhone(t, server, st, "777777")

	challengeID, nonce := p.challenge(t, server)
	// Signed for half an hour, sent asking for a whole day. This is the attack the body
	// hash in approvalPayload exists to stop.
	p.send(t, server, "POST", "/api/parent/grants",
		map[string]any{"minutes": 1440},
		map[string]any{"minutes": 30},
		challengeID, nonce, http.StatusForbidden)

	if n := grantCount(t, st); n != 0 {
		t.Fatalf("a swapped body produced %d grants", n)
	}
}

func TestASignatureIsBoundToThePath(t *testing.T) {
	server, st := newTestServer(t)
	p := enrolPhone(t, server, st, "787878")
	enableEnforcement(t, st)

	challengeID, nonce := p.challenge(t, server)
	// A signature made for the grant endpoint, presented at the enforcement switch.
	request, err := http.NewRequest("POST", server.URL+"/api/parent/enforcement",
		bytes.NewReader([]byte(`{"enabled":false}`)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+p.token)
	request.Header.Set("X-Approver-Id", p.id)
	request.Header.Set("X-Approver-Challenge", challengeID)
	request.Header.Set("X-Approver-Signature", base64.StdEncoding.EncodeToString(
		p.sign(t, approvalPayload(nonce, "POST", "/api/parent/grants",
			[]byte(`{"enabled":false}`)))))

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("a signature for another endpoint was accepted: %d", response.StatusCode)
	}

	if !enforcing(t, st) {
		t.Fatal("enforcement was switched off by a signature made for another endpoint")
	}
}

func TestAnExpiredChallengeIsRefused(t *testing.T) {
	server, st := newTestServer(t)
	p := enrolPhone(t, server, st, "888888")

	// Injected straight into the store, since waiting out challengeLifetime in a test is
	// two minutes nobody wants to spend.
	err := st.Write(func(s *store.State, _ store.Usage) error {
		s.ApproverChallenges = append(s.ApproverChallenges, store.ApproverChallenge{
			ID:              "stale",
			Nonce:           "stale-nonce",
			ExpiresAtMillis: time.Now().Add(-time.Second).UnixMilli(),
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	body := map[string]any{"minutes": 30}
	p.send(t, server, "POST", "/api/parent/grants", body, body,
		"stale", "stale-nonce", http.StatusForbidden)
}

func TestEnforcementCannotBeSwitchedOffWithoutAFace(t *testing.T) {
	server, st := newTestServer(t)
	p := enrolPhone(t, server, st, "999999")
	enableEnforcement(t, st)

	// The whole point: switching enforcement off is as good as a grant, so it is gated
	// identically. A kid with the passcode reaches the session, not the key.
	do(t, "POST", server.URL+"/api/parent/enforcement", p.token,
		map[string]any{"enabled": false}, nil, http.StatusForbidden)

	if !enforcing(t, st) {
		t.Fatal("enforcement went off without an approval")
	}

	p.approve(t, server, "POST", "/api/parent/enforcement",
		map[string]any{"enabled": false}, http.StatusOK)
	if enforcing(t, st) {
		t.Fatal("an approved switch-off did not take effect")
	}
}

// ---------- no way back ----------

func TestRemovingAnApproverNeedsAnApproval(t *testing.T) {
	server, st := newTestServer(t)
	p := enrolPhone(t, server, st, "101010")

	// Without this, the bypass is trivial: delete every approver and the gate falls back
	// to passcode-reachable rules.
	do(t, "DELETE", server.URL+"/api/parent/approvers/"+p.id, p.token,
		nil, nil, http.StatusForbidden)

	var left int
	st.Read(func(s *store.State, _ store.Usage) { left = len(s.Approvers) })
	if left != 1 {
		t.Fatalf("approvers left = %d, want 1", left)
	}

	p.approve(t, server, "DELETE", "/api/parent/approvers/"+p.id, nil, http.StatusNoContent)
	st.Read(func(s *store.State, _ store.Usage) { left = len(s.Approvers) })
	if left != 0 {
		t.Fatalf("an approved removal left %d approvers", left)
	}
}

func TestEnrollingASecondPhoneNeedsTheFirstOne(t *testing.T) {
	server, st := newTestServer(t)
	p := enrolPhone(t, server, st, "121212")

	// Minting a code is itself gated once a phone exists, so the passcode route cannot
	// quietly add a second approver.
	do(t, "POST", server.URL+"/api/parent/approver-code", p.token, nil, nil, http.StatusForbidden)

	var code struct {
		Code string `json:"code"`
	}
	payload := p.approve(t, server, "POST", "/api/parent/approver-code", nil, http.StatusOK)
	if err := json.Unmarshal(payload, &code); err != nil {
		t.Fatal(err)
	}
	if len(code.Code) != approverCodeDigits {
		t.Fatalf("code = %q, want %d digits", code.Code, approverCodeDigits)
	}
}

// ---------- reading ----------

func TestApproverTokenCanReadStateButNotActOnIt(t *testing.T) {
	server, st := newTestServer(t)
	p := enrolPhone(t, server, st, "131313")

	var state map[string]any
	do(t, "GET", server.URL+"/api/approver/state", p.token, nil, &state, http.StatusOK)
	if _, ok := state["policy"]; !ok {
		t.Fatalf("state carried no policy: %v", state)
	}

	// The same token on the parent surface is nothing: those routes want a session cookie.
	do(t, "GET", server.URL+"/api/parent/state", p.token, nil, nil, http.StatusUnauthorized)
}

func TestApproverReadsRejectUnknownTokens(t *testing.T) {
	server, st := newTestServer(t)
	enrolPhone(t, server, st, "141414")

	do(t, "GET", server.URL+"/api/approver/state", "not-a-token", nil, nil, http.StatusUnauthorized)
	do(t, "POST", server.URL+"/api/approver/challenge", "", nil, nil, http.StatusUnauthorized)
}

// ---------- the key parsing itself ----------

func TestOnlyWellFormedP256KeysAreAccepted(t *testing.T) {
	good := newPhone(t).publicKey(t)
	if len(good) != 65 || good[0] != 4 {
		t.Fatalf("the test's own key is not an uncompressed point: %d bytes", len(good))
	}
	if _, err := parseApproverKey(good); err != nil {
		t.Fatalf("a real P-256 point was rejected: %v", err)
	}

	offCurve := bytes.Clone(good)
	offCurve[40] ^= 0xff
	if _, err := parseApproverKey(offCurve); err == nil {
		t.Fatal("a point off the curve was accepted")
	}
	for name, raw := range map[string][]byte{
		"empty":      {},
		"truncated":  good[:64],
		"compressed": append([]byte{2}, good[1:33]...),
	} {
		if _, err := parseApproverKey(raw); err == nil {
			t.Fatalf("%s key was accepted", name)
		}
	}
}
