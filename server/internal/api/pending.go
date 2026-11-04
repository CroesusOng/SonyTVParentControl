package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/homelab/braviaguard/internal/model"
	"github.com/homelab/braviaguard/internal/store"
)

// Some writes cannot simply be gated on a signature the way an unlock is.
//
// Editing the rules is the one that forced this. It has to stay in the parent app, which
// is where the editor lives and where it belongs -- but leaving it on parentWrite would
// have made the whole mechanism theatre, because handlePutPolicy replaces the policy
// wholesale, enforcementEnabled included. Anyone who got past the passcode could simply
// turn enforcement off through the rule editor.
//
// So those writes are queued instead. The parent app submits one and gets back a 202 and
// an id; nothing has happened yet. The approver app lists what is waiting, shows what
// each would change, and approves one with a face. Only then does the request run.
//
// Three things make the queue safe rather than just indirect:
//
//   - approvableAction is a whitelist in code. Whatever ends up in state.json, only the
//     handlers named there can ever be executed by an approval.
//   - the summary is computed when the change is queued, against the state at that
//     moment, so the parent reads a diff that was true when it was written rather than
//     one reconstructed later.
//   - the approving signature covers the content hash, so approving request A cannot
//     execute request B.

const (
	pendingLifetime = time.Hour

	actionPutPolicy = "PUT /api/parent/policy"
)

// approvableAction maps a queued action to the handler that carries it out.
//
// A switch rather than a map assembled at startup: there is no initialisation order to
// get wrong, and -- more to the point -- an action string that is not written here cannot
// be executed no matter how it got into the queue. The queue stores a method, a path and
// a body, which is exactly the shape of an arbitrary request; this is what stops it being
// one.
func (s *Server) approvableAction(action string) (http.HandlerFunc, bool) {
	switch action {
	case actionPutPolicy:
		return s.handlePutPolicy, true
	default:
		return nil, false
	}
}

// summariseAction turns a queued body into lines a parent can read on a phone. Returning
// nothing is not allowed: an approval screen with no description is one nobody can judge.
func (s *Server) summariseAction(action string, body []byte, st *store.State) []string {
	switch action {
	case actionPutPolicy:
		var incoming model.Policy
		if err := json.Unmarshal(body, &incoming); err != nil {
			return []string{"改规则(内容解析失败,建议驳回)"}
		}
		return summarisePolicy(st.Policy, incoming)
	default:
		return []string{action}
	}
}

// ---------- queuing ----------

// queuedIfGated is the policy-editor counterpart to needsApproval. Same fallback: with no
// approver enrolled it runs the handler exactly as before, so enrolling a phone is what
// turns this on and nothing else.
func (s *Server) queuedIfGated(action string, inner http.HandlerFunc) http.HandlerFunc {
	return s.parentWrite(func(w http.ResponseWriter, r *http.Request) {
		var enrolled bool
		s.store.Read(func(st *store.State, _ store.Usage) { enrolled = len(st.Approvers) > 0 })
		if !enrolled {
			inner(w, r)
			return
		}
		s.queueForApproval(action, w, r)
	})
}

func (s *Server) queueForApproval(action string, w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, "body_too_large", "")
		return
	}

	now := time.Now()
	change := store.PendingChange{
		ID:              randomToken(9),
		Action:          action,
		Body:            body,
		ContentHash:     contentHash(action, body),
		CreatedAtMillis: now.UnixMilli(),
		ExpiresAtMillis: now.Add(pendingLifetime).UnixMilli(),
	}

	if err := s.store.Write(func(st *store.State, _ store.Usage) error {
		change.Summary = s.summariseAction(action, body, st)
		st.Pending = append(st.Pending, change)
		return nil
	}); err != nil {
		fail(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}

	log.Printf("queued for approval: %s (%s)", action, change.ID)
	// 202, not 200: the parent app has to say "waiting for approval" rather than "saved".
	writeJSON(w, http.StatusAccepted, map[string]any{
		"pendingId":       change.ID,
		"contentHash":     change.ContentHash,
		"summary":         change.Summary,
		"expiresAtMillis": change.ExpiresAtMillis,
	})
}

func contentHash(action string, body []byte) string {
	sum := sha256.Sum256(append([]byte(action+"\n"), body...))
	return hex.EncodeToString(sum[:])
}

// ---------- listing, approving, rejecting ----------

type pendingView struct {
	ID              string   `json:"id"`
	Action          string   `json:"action"`
	ContentHash     string   `json:"contentHash"`
	Summary         []string `json:"summary"`
	CreatedAtMillis int64    `json:"createdAtMillis"`
	ExpiresAtMillis int64    `json:"expiresAtMillis"`
}

// handleListPending is readable from both surfaces: the approver app needs it to show
// what is waiting, and the parent app needs it to show that a save is not finished.
// pendingViews never returns nil, for the same reason approverViews does not: the parent
// app decides whether to show a "waiting" banner from the length of this.
//
// Expired entries are filtered rather than waited on. Pruning happens on a timer, so
// between the expiry and the next prune the queue still holds them, and showing a parent
// something they can no longer approve is worse than showing nothing.
func pendingViews(st *store.State, nowMillis int64) []pendingView {
	views := make([]pendingView, 0, len(st.Pending))
	for _, p := range st.Pending {
		if p.ExpiresAtMillis <= nowMillis {
			continue
		}
		views = append(views, pendingView{
			ID: p.ID, Action: p.Action, ContentHash: p.ContentHash,
			Summary: p.Summary, CreatedAtMillis: p.CreatedAtMillis,
			ExpiresAtMillis: p.ExpiresAtMillis,
		})
	}
	return views
}

func (s *Server) handleListPending(w http.ResponseWriter, _ *http.Request) {
	var views []pendingView
	now := time.Now().UnixMilli()
	s.store.Read(func(st *store.State, _ store.Usage) { views = pendingViews(st, now) })
	writeJSON(w, http.StatusOK, map[string]any{"pending": views})
}

// handleApprovePending runs a queued change. Reached only through needsApproval, so a
// signature has already been checked over this exact path and body.
func (s *Server) handleApprovePending(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var request struct {
		ContentHash string `json:"contentHash"`
	}
	if !readJSON(w, r, &request) {
		return
	}

	var change store.PendingChange
	var problem string
	err := s.store.Write(func(st *store.State, _ store.Usage) error {
		found, fresh := st.TakePending(id, time.Now().UnixMilli())
		switch {
		case found.ID == "":
			problem = "unknown_pending"
		case !fresh:
			problem = "pending_expired"
		case found.ContentHash != request.ContentHash:
			// The approver app sends back the hash it displayed. A mismatch means the
			// thing approved is not the thing read, so nothing runs.
			problem = "content_changed"
		default:
			change = found
		}
		return nil
	})
	switch {
	case err != nil:
		fail(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	case problem != "":
		fail(w, http.StatusConflict, problem, "")
		return
	}

	handler, ok := s.approvableAction(change.Action)
	if !ok {
		// Unreachable through the API, since queuing goes through queuedIfGated with a
		// constant. Reachable by hand-editing state.json, which is exactly the case the
		// whitelist exists for.
		log.Printf("refusing to execute unknown queued action %q (%s)", change.Action, change.ID)
		fail(w, http.StatusConflict, "unknown_action", "")
		return
	}

	log.Printf("approved and executing: %s (%s)", change.Action, change.ID)
	handler(w, replay(r, change))
}

// replay rebuilds the request the parent app originally made, so the handler sees what it
// would have seen at submission time and needs to know nothing about approvals.
func replay(r *http.Request, change store.PendingChange) *http.Request {
	method, path, _ := strings.Cut(change.Action, " ")
	out := r.Clone(r.Context())
	out.Method = method
	if parsed, err := url.Parse(path); err == nil {
		out.URL = parsed
	}
	// A fresh header map: the approver's signature headers belong to the approval, not to
	// the request being replayed, and nothing downstream should be able to see them.
	out.Header = http.Header{"Content-Type": []string{"application/json"}}
	out.Body = io.NopCloser(bytes.NewReader(change.Body))
	out.ContentLength = int64(len(change.Body))
	return out
}

// handleRejectPending throws a queued change away. Only parentWrite: rejecting is the
// safe direction, and requiring a face to say no would mean a change nobody wants sits
// there until it expires.
func (s *Server) handleRejectPending(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.Write(func(st *store.State, _ store.Usage) error {
		st.Pending = slices.DeleteFunc(st.Pending, func(p store.PendingChange) bool {
			return p.ID == id
		})
		return nil
	}); err != nil {
		fail(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- describing a rule change ----------

// summarisePolicy describes what a submitted policy would change, in the words a parent
// would use. Every line that loosens the rules has to appear: enforcement going off, the
// list mode going off, an app being allowed, a budget going up, a curfew disappearing.
// A summary that quietly omitted one of those would be worse than no summary, because it
// would carry the parent's approval.
func summarisePolicy(current, incoming model.Policy) []string {
	var lines []string
	add := func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}

	if current.EnforcementEnabled != incoming.EnforcementEnabled {
		add("拦截总开关:%s → %s",
			onOff(current.EnforcementEnabled), onOff(incoming.EnforcementEnabled))
	}
	if current.ListMode != incoming.ListMode {
		add("名单模式:%s → %s", current.ListMode, incoming.ListMode)
	}

	describeList("白名单", current.Allowlist, incoming.Allowlist, add)
	describeList("黑名单", current.Blocklist, incoming.Blocklist, add)
	describeList("豁免应用", current.ExemptPackages, incoming.ExemptPackages, add)

	describeMinutes("每日限额", current.DailyMinutes, incoming.DailyMinutes, add)
	describeMinutes("单应用限额", current.PerAppDailyMinutes, incoming.PerAppDailyMinutes, add)

	if minutesText(current.WeeklyMinutes) != minutesText(incoming.WeeklyMinutes) {
		add("每周限额:%s → %s",
			minutesText(current.WeeklyMinutes), minutesText(incoming.WeeklyMinutes))
	}

	if len(current.Curfews) != len(incoming.Curfews) {
		add("宵禁:%d 条 → %d 条", len(current.Curfews), len(incoming.Curfews))
	}
	for _, curfew := range incoming.Curfews {
		if !slices.ContainsFunc(current.Curfews, func(c model.Curfew) bool {
			return c.ID == curfew.ID && c.Start == curfew.Start && c.End == curfew.End &&
				slices.Equal(c.Days, curfew.Days)
		}) {
			add("宵禁改动:%s %s-%s(%s)",
				orDash(curfew.Label), curfew.Start, curfew.End, strings.Join(curfew.Days, " "))
		}
	}

	if current.OfflineBehavior != incoming.OfflineBehavior {
		add("断网行为:%s → %s", current.OfflineBehavior, incoming.OfflineBehavior)
	}
	if current.OfflineGraceMinutes != incoming.OfflineGraceMinutes {
		add("断网宽限:%d → %d 分钟",
			current.OfflineGraceMinutes, incoming.OfflineGraceMinutes)
	}

	if len(lines) == 0 {
		return []string{"改规则,但没有实质改动"}
	}
	return lines
}

func describeList(name string, current, incoming []string, add func(string, ...any)) {
	added, removed := difference(incoming, current), difference(current, incoming)
	if len(added) > 0 {
		add("%s 加入:%s", name, strings.Join(added, "、"))
	}
	if len(removed) > 0 {
		add("%s 移除:%s", name, strings.Join(removed, "、"))
	}
}

func describeMinutes(name string, current, incoming map[string]int, add func(string, ...any)) {
	keys := map[string]bool{}
	for k := range current {
		keys[k] = true
	}
	for k := range incoming {
		keys[k] = true
	}
	// Sorted, so two runs over the same change read the same way. Map order would make
	// the summary shuffle between a parent reading it and reading it again.
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)

	for _, key := range ordered {
		before, hadBefore := current[key]
		after, hasAfter := incoming[key]
		switch {
		case hadBefore && hasAfter && before != after:
			add("%s %s:%d → %d 分钟", name, key, before, after)
		case hadBefore && !hasAfter:
			add("%s %s:%d 分钟 → 取消限制", name, key, before)
		case !hadBefore && hasAfter:
			add("%s %s:无限制 → %d 分钟", name, key, after)
		}
	}
}

func difference(from, without []string) []string {
	var out []string
	for _, item := range from {
		if !slices.Contains(without, item) {
			out = append(out, item)
		}
	}
	return out
}

func onOff(value bool) string {
	if value {
		return "开"
	}
	return "关"
}

func minutesText(value *int) string {
	if value == nil {
		return "无限制"
	}
	return fmt.Sprintf("%d 分钟", *value)
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(无名)"
	}
	return value
}
