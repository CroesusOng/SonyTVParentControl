// Package store persists everything the backend knows, as indented JSON files.
//
// No database. The entire working set for one family is a rule document, a handful of
// devices and passkeys, and a 90-day usage map — a few hundred kilobytes that is always
// read whole. Files mean the Pi needs no database to run or back up, and mean the owner
// of the box can read and fix their own data with jq and a text editor, which for a
// homelab is worth more than query support nobody will use.
//
// Writes are atomic (temp file plus rename), so a power cut mid-write leaves the previous
// good file rather than a truncated one.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/homelab/braviaguard/internal/model"
)

const (
	stateFile      = "state.json"
	usageFile      = "usage.json"
	maxBlockEvents = 200
	usageRetention = 120 // days
)

type Device struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Model           string `json:"model"`
	AndroidRelease  string `json:"androidRelease"`
	TokenHash       string `json:"tokenHash"`
	CreatedAtMillis int64  `json:"createdAtMillis"`
	LastSeenMillis  int64  `json:"lastSeenMillis"`
}

type PairingCode struct {
	Code            string `json:"code"`
	ExpiresAtMillis int64  `json:"expiresAtMillis"`
}

// Parent holds passkeys. Credentials stay as raw JSON so this package does not depend
// on the WebAuthn library and can be tested without it.
type Parent struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	HandleBase64    string            `json:"handle"`
	Credentials     []json.RawMessage `json:"credentials"`
	CreatedAtMillis int64             `json:"createdAtMillis"`
}

type Session struct {
	Token               string `json:"token"`
	ParentID            string `json:"parentId"`
	ExpiresAtMillis     int64  `json:"expiresAtMillis"`
	LastAssertionMillis int64  `json:"lastAssertionMillis"`
}

type BlockEvent struct {
	DeviceID    string `json:"deviceId"`
	PackageName string `json:"packageName"`
	AppLabel    string `json:"appLabel,omitempty"`
	Reason      string `json:"reason"`
	AtMillis    int64  `json:"atMillis"`
}

type State struct {
	Policy       model.Policy      `json:"policy"`
	Grants       []model.Grant     `json:"grants"`
	Devices      []Device          `json:"devices"`
	Parents      []Parent          `json:"parents"`
	Sessions     []Session         `json:"sessions"`
	PairingCodes []PairingCode     `json:"pairingCodes"`
	EscapeSecret string            `json:"escapeSecretBase32"`
	AppLabels    map[string]string `json:"appLabels"`
	RecentBlocks []BlockEvent      `json:"recentBlocks"`

	// Revision counts writes. A long-polling television sends back the last one it saw,
	// which is the only way handleSync can tell "you are up to date, hold the line" from
	// "you are behind, answer now" -- the policy has its own version, but grants do not.
	// Persisted, so a restart does not look like a change to a TV that is already current.
	// Compared for inequality rather than order, so a wiped state file (revision back to
	// zero) still reads as "something changed" instead of wedging every device.
	Revision int64 `json:"revision"`
}

// Usage is deviceID -> ISO day -> package -> seconds.
type Usage map[string]map[string]map[string]int64

type Store struct {
	dir string

	mu    sync.RWMutex
	state State
	usage Usage

	// Sync traffic touches LastSeen and usage every few seconds. Writing the files that
	// often would be pointless churn on an SD card, so those changes are flushed on a
	// timer while policy and credential changes are written through immediately.
	dirty bool

	// Closed and replaced by every Write. Waiting on a channel rather than registering a
	// listener means a device that vanishes mid-request leaves nothing behind to clean up:
	// its goroutine simply stops selecting. Guarded by mu like the rest of this struct.
	changed chan struct{}
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	s := &Store{
		dir: dir,
		state: State{
			Policy:    model.DefaultPolicy(),
			AppLabels: map[string]string{},
		},
		usage:   Usage{},
		changed: make(chan struct{}),
	}
	if err := readJSON(filepath.Join(dir, stateFile), &s.state); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(dir, usageFile), &s.usage); err != nil {
		return nil, err
	}
	if s.state.AppLabels == nil {
		s.state.AppLabels = map[string]string{}
	}
	if s.usage == nil {
		s.usage = Usage{}
	}
	if s.state.Policy.Version == 0 {
		s.state.Policy = model.DefaultPolicy()
	}
	return s, nil
}

// Read gives a consistent view to a caller that only looks.
func (s *Store) Read(fn func(*State, Usage)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(&s.state, s.usage)
}

// Write mutates under lock and persists immediately. For changes a parent made.
//
// A write that a television would act on also bumps the revision and wakes any held sync
// request, so an unlock tapped on a phone reaches the TV in one round trip instead of on
// its next poll. Which writes those are is decided by [deviceFacingLocked] rather than by
// the caller: most of what comes through here is sessions, challenges and block history
// that no TV reads, and /api/parent/login/begin is a public endpoint, so broadcasting
// every write would let anyone who can load the sign-in page make every television
// resync in a loop.
func (s *Store) Write(fn func(*State, Usage) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.deviceFacingLocked()
	if err := fn(&s.state, s.usage); err != nil {
		return err
	}
	news := s.deviceFacingLocked() != before
	if news {
		// Bumped before persisting, so the number in the file always describes the file.
		s.state.Revision++
	}
	err := s.persistLocked()
	if news {
		// Broadcast even when persisting failed: the in-memory state did change, and that
		// is the state every reader is about to be served.
		close(s.changed)
		s.changed = make(chan struct{})
	}
	return err
}

// deviceFacingLocked fingerprints exactly the state a television is sent, so comparing it
// around a write tells us whether any TV needs to hear about that write.
//
// Deriving it beats keeping a list of which handlers matter: a handler added later that
// changes the policy wakes the televisions without having to remember to, and one that
// only touches a session never will. It has to cover the same fields api.syncResponse
// reads and no others -- add a field there, add it here.
func (s *Store) deviceFacingLocked() [sha256.Size]byte {
	payload, err := json.Marshal(struct {
		Policy       model.Policy  `json:"p"`
		Grants       []model.Grant `json:"g"`
		EscapeSecret string        `json:"e"`
	}{s.state.Policy, s.state.Grants, s.state.EscapeSecret})
	if err != nil {
		// Nothing in there is unmarshalable, but a silent equal-to-itself would mean no TV
		// ever hears about a change again. Returning a distinct value broadcasts instead.
		var random [sha256.Size]byte
		_, _ = rand.Read(random[:])
		return random
	}
	return sha256.Sum256(payload)
}

// Changed returns a channel the next Write will close.
//
// Take it *before* reading the state. A write landing between the read and the wait then
// closes the channel that is already in hand, instead of slipping through the gap and
// leaving the caller parked on a reply that was stale when it was built.
func (s *Store) Changed() <-chan struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.changed
}

// Touch mutates under lock but defers the write to the next flush. For sync chatter.
func (s *Store) Touch(fn func(*State, Usage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.state, s.usage)
	s.dirty = true
}

func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	return s.persistLocked()
}

// StartFlusher writes deferred changes until done is closed.
func (s *Store) StartFlusher(interval time.Duration, done <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = s.Flush()
			case <-done:
				_ = s.Flush()
				return
			}
		}
	}()
}

func (s *Store) persistLocked() error {
	s.pruneLocked()
	if err := writeJSON(filepath.Join(s.dir, stateFile), s.state); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(s.dir, usageFile), s.usage); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// pruneLocked keeps the files from growing without bound: expired grants, sessions and
// pairing codes go, usage older than the retention window goes, block history is capped.
func (s *Store) pruneLocked() {
	now := time.Now().UnixMilli()

	s.state.Grants = filter(s.state.Grants, func(g model.Grant) bool {
		return g.ExpiresAtMillis > now
	})
	s.state.Sessions = filter(s.state.Sessions, func(x Session) bool {
		return x.ExpiresAtMillis > now
	})
	s.state.PairingCodes = filter(s.state.PairingCodes, func(c PairingCode) bool {
		return c.ExpiresAtMillis > now
	})

	if len(s.state.RecentBlocks) > maxBlockEvents {
		s.state.RecentBlocks = s.state.RecentBlocks[len(s.state.RecentBlocks)-maxBlockEvents:]
	}

	cutoff := time.Now().AddDate(0, 0, -usageRetention).Format("2006-01-02")
	for device, days := range s.usage {
		for day := range days {
			if day < cutoff {
				delete(days, day)
			}
		}
		if len(days) == 0 {
			delete(s.usage, device)
		}
	}
}

// ---------- helpers used by the API layer ----------

func (s *State) DeviceByID(id string) *Device {
	for i := range s.Devices {
		if s.Devices[i].ID == id {
			return &s.Devices[i]
		}
	}
	return nil
}

func (s *State) DeviceByTokenHash(hash string) *Device {
	for i := range s.Devices {
		if s.Devices[i].TokenHash == hash {
			return &s.Devices[i]
		}
	}
	return nil
}

func (s *State) ParentByID(id string) *Parent {
	for i := range s.Parents {
		if s.Parents[i].ID == id {
			return &s.Parents[i]
		}
	}
	return nil
}

func (s *State) ParentByHandle(handleBase64 string) *Parent {
	for i := range s.Parents {
		if s.Parents[i].HandleBase64 == handleBase64 {
			return &s.Parents[i]
		}
	}
	return nil
}

func (s *State) SessionByToken(token string) *Session {
	for i := range s.Sessions {
		if s.Sessions[i].Token == token {
			return &s.Sessions[i]
		}
	}
	return nil
}

// TakePairingCode consumes a code if it is valid. Single use, by design: a code that
// kept working would be a standing invitation to enrol a rogue device.
func (s *State) TakePairingCode(code string, nowMillis int64) bool {
	for i, c := range s.PairingCodes {
		if c.Code == code && c.ExpiresAtMillis > nowMillis {
			s.PairingCodes = append(s.PairingCodes[:i], s.PairingCodes[i+1:]...)
			return true
		}
	}
	return false
}

// MergeUsage replaces the given days outright rather than adding to them: the TV sends
// running totals for the last few days, so adding would double-count on every sync.
func (u Usage) MergeUsage(deviceID string, days []model.DayUsage) {
	deviceUsage, ok := u[deviceID]
	if !ok {
		deviceUsage = map[string]map[string]int64{}
		u[deviceID] = deviceUsage
	}
	for _, day := range days {
		perApp := map[string]int64{}
		for pkg, seconds := range day.PerAppSeconds {
			perApp[pkg] = seconds
		}
		deviceUsage[day.Day] = perApp
	}
}

// DailyTotals sums every device, newest day last.
func (u Usage) DailyTotals(fromDay string) []DayTotal {
	byDay := map[string]int64{}
	for _, days := range u {
		for day, perApp := range days {
			if day < fromDay {
				continue
			}
			for _, seconds := range perApp {
				byDay[day] += seconds
			}
		}
	}
	totals := make([]DayTotal, 0, len(byDay))
	for day, seconds := range byDay {
		totals = append(totals, DayTotal{Day: day, Seconds: seconds})
	}
	sort.Slice(totals, func(i, j int) bool { return totals[i].Day < totals[j].Day })
	return totals
}

// AppTotals sums per package across devices from fromDay onwards, biggest first.
func (u Usage) AppTotals(fromDay string) []AppTotal {
	byApp := map[string]int64{}
	for _, days := range u {
		for day, perApp := range days {
			if day < fromDay {
				continue
			}
			for pkg, seconds := range perApp {
				byApp[pkg] += seconds
			}
		}
	}
	totals := make([]AppTotal, 0, len(byApp))
	for pkg, seconds := range byApp {
		totals = append(totals, AppTotal{PackageName: pkg, Seconds: seconds})
	}
	sort.Slice(totals, func(i, j int) bool {
		if totals[i].Seconds != totals[j].Seconds {
			return totals[i].Seconds > totals[j].Seconds
		}
		return totals[i].PackageName < totals[j].PackageName
	})
	return totals
}

type DayTotal struct {
	Day     string `json:"day"`
	Seconds int64  `json:"seconds"`
}

type AppTotal struct {
	PackageName string `json:"packageName"`
	Label       string `json:"label,omitempty"`
	Seconds     int64  `json:"seconds"`
}

// ---------- file io ----------

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, value any) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return fmt.Errorf("temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ") // readable with jq, editable by hand
	if err := encoder.Encode(value); err != nil {
		tmp.Close()
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename onto %s: %w", path, err)
	}
	return nil
}

func filter[T any](items []T, keep func(T) bool) []T {
	out := items[:0]
	for _, item := range items {
		if keep(item) {
			out = append(out, item)
		}
	}
	return out
}
