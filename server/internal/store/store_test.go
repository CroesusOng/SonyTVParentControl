package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/homelab/braviaguard/internal/model"
)

func TestOpenCreatesAnInertDefaultPolicy(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.Read(func(s *State, _ Usage) {
		if s.Policy.EnforcementEnabled {
			t.Error("a brand new install must not enforce anything")
		}
		if s.Policy.ListMode != "OFF" {
			t.Errorf("listMode = %q, want OFF", s.Policy.ListMode)
		}
	})
}

func TestStateSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = st.Write(func(s *State, usage Usage) error {
		s.Policy.EnforcementEnabled = true
		s.Policy.Version = 7
		s.EscapeSecret = "ABCDEFGHIJKLMNOP"
		s.Devices = append(s.Devices, Device{ID: "tv1", Name: "Living room", TokenHash: "hash"})
		usage.MergeUsage("tv1", []model.DayUsage{
			{Day: "2026-11-03", PerAppSeconds: map[string]int64{"com.netflix.ninja": 1800}},
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Read(func(s *State, usage Usage) {
		if !s.Policy.EnforcementEnabled || s.Policy.Version != 7 {
			t.Errorf("policy did not round trip: %+v", s.Policy)
		}
		if s.EscapeSecret != "ABCDEFGHIJKLMNOP" {
			t.Errorf("escape secret = %q", s.EscapeSecret)
		}
		if got := usage["tv1"]["2026-11-03"]["com.netflix.ninja"]; got != 1800 {
			t.Errorf("usage = %d, want 1800", got)
		}
	})
}

func TestWritesAreAtomic(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	if err := st.Write(func(s *State, _ Usage) error { s.Policy.Version = 2; return nil }); err != nil {
		t.Fatal(err)
	}
	// No leftover temp files: a crashed write must not leave debris that a later Open
	// could mistake for data.
	entries, err := filepath.Glob(filepath.Join(dir, ".write-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestPairingCodeIsSingleUse(t *testing.T) {
	st, _ := Open(t.TempDir())
	now := time.Now().UnixMilli()
	_ = st.Write(func(s *State, _ Usage) error {
		s.PairingCodes = append(s.PairingCodes, PairingCode{
			Code: "123456", ExpiresAtMillis: now + 60_000,
		})
		return nil
	})

	st.Read(func(s *State, _ Usage) {
		if !s.TakePairingCode("123456", now) {
			t.Fatal("first use should succeed")
		}
		if s.TakePairingCode("123456", now) {
			t.Error("a pairing code must not work twice")
		}
	})
}

func TestExpiredPairingCodeIsRefused(t *testing.T) {
	st, _ := Open(t.TempDir())
	now := time.Now().UnixMilli()
	_ = st.Write(func(s *State, _ Usage) error {
		s.PairingCodes = append(s.PairingCodes, PairingCode{
			Code: "999999", ExpiresAtMillis: now - 1,
		})
		return nil
	})
	st.Read(func(s *State, _ Usage) {
		if s.TakePairingCode("999999", now) {
			t.Error("an expired code must be refused")
		}
	})
}

func TestMergeUsageReplacesRatherThanAccumulates(t *testing.T) {
	st, _ := Open(t.TempDir())
	st.Read(func(_ *State, usage Usage) {
		day := []model.DayUsage{{Day: "2026-11-03", PerAppSeconds: map[string]int64{"a": 600}}}
		usage.MergeUsage("tv1", day)
		// The TV re-sends running totals every poll; adding them would multiply usage.
		usage.MergeUsage("tv1", []model.DayUsage{
			{Day: "2026-11-03", PerAppSeconds: map[string]int64{"a": 900}},
		})
		if got := usage["tv1"]["2026-11-03"]["a"]; got != 900 {
			t.Errorf("usage = %d, want 900 (replaced, not summed)", got)
		}
	})
}

func TestPruneDropsExpiredGrantsAndSessions(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	now := time.Now().UnixMilli()
	_ = st.Write(func(s *State, _ Usage) error {
		s.Grants = []model.Grant{
			{ID: "live", ExpiresAtMillis: now + 60_000},
			{ID: "dead", ExpiresAtMillis: now - 60_000},
		}
		s.Sessions = []Session{
			{Token: "live", ExpiresAtMillis: now + 60_000},
			{Token: "dead", ExpiresAtMillis: now - 60_000},
		}
		return nil
	})
	st.Read(func(s *State, _ Usage) {
		if len(s.Grants) != 1 || s.Grants[0].ID != "live" {
			t.Errorf("grants = %+v, want only the live one", s.Grants)
		}
		if len(s.Sessions) != 1 || s.Sessions[0].Token != "live" {
			t.Errorf("sessions = %+v, want only the live one", s.Sessions)
		}
	})
}

func TestDailyAndAppTotalsSumAcrossDevices(t *testing.T) {
	st, _ := Open(t.TempDir())
	st.Read(func(_ *State, usage Usage) {
		usage.MergeUsage("tv1", []model.DayUsage{
			{Day: "2026-11-02", PerAppSeconds: map[string]int64{"net": 600}},
			{Day: "2026-11-03", PerAppSeconds: map[string]int64{"net": 300, "khan": 120}},
		})
		usage.MergeUsage("tv2", []model.DayUsage{
			{Day: "2026-11-03", PerAppSeconds: map[string]int64{"net": 60}},
		})

		daily := usage.DailyTotals("2026-11-02")
		if len(daily) != 2 || daily[0].Day != "2026-11-02" {
			t.Fatalf("daily = %+v, want two days oldest first", daily)
		}
		if daily[1].Seconds != 480 {
			t.Errorf("2026-11-03 total = %d, want 480", daily[1].Seconds)
		}

		apps := usage.AppTotals("2026-11-03")
		if len(apps) != 2 || apps[0].PackageName != "net" || apps[0].Seconds != 360 {
			t.Errorf("apps = %+v, want net=360 first", apps)
		}

		// A from-day filter must exclude earlier days entirely.
		if later := usage.DailyTotals("2026-11-03"); len(later) != 1 {
			t.Errorf("filtered daily = %+v, want one day", later)
		}
	})
}

func TestDeviceLookups(t *testing.T) {
	st, _ := Open(t.TempDir())
	_ = st.Write(func(s *State, _ Usage) error {
		s.Devices = []Device{{ID: "tv1", TokenHash: "aaa"}, {ID: "tv2", TokenHash: "bbb"}}
		return nil
	})
	st.Read(func(s *State, _ Usage) {
		if d := s.DeviceByID("tv2"); d == nil || d.TokenHash != "bbb" {
			t.Error("DeviceByID failed")
		}
		if d := s.DeviceByTokenHash("aaa"); d == nil || d.ID != "tv1" {
			t.Error("DeviceByTokenHash failed")
		}
		if s.DeviceByID("nope") != nil || s.DeviceByTokenHash("nope") != nil {
			t.Error("lookups should return nil for unknown ids")
		}
	})
}

func TestWriteBumpsTheRevisionAndPersistsIt(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var before int64
	st.Read(func(state *State, _ Usage) { before = state.Revision })

	if err := st.Write(func(state *State, _ Usage) error {
		state.EscapeSecret = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var after int64
	st.Read(func(state *State, _ Usage) { after = state.Revision })
	if after != before+1 {
		t.Fatalf("revision went %d -> %d, want one step", before, after)
	}

	// Persisted: a restart that reset the counter would look to every television like a
	// change it had not seen, on every single poll.
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded int64
	reopened.Read(func(state *State, _ Usage) { reloaded = state.Revision })
	if reloaded != after {
		t.Errorf("revision after reopen = %d, want %d", reloaded, after)
	}
}

func TestFailedWriteLeavesTheRevisionAlone(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var before int64
	st.Read(func(state *State, _ Usage) { before = state.Revision })

	if err := st.Write(func(_ *State, _ Usage) error {
		return errors.New("validation said no")
	}); err == nil {
		t.Fatal("expected the error to come back")
	}

	var after int64
	st.Read(func(state *State, _ Usage) { after = state.Revision })
	if after != before {
		t.Errorf("a rejected write moved the revision to %d; every TV would resync for nothing", after)
	}
}

func TestChangedIsClosedByTheNextWrite(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Claimed before the write, which is the order a held sync request uses.
	changed := st.Changed()
	select {
	case <-changed:
		t.Fatal("closed before anything was written")
	default:
	}

	if err := st.Write(func(state *State, _ Usage) error {
		state.Grants = append(state.Grants, model.Grant{ID: "g1"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("a write did not wake the channel a held request would be waiting on")
	}

	// And the next waiter gets a fresh one rather than the closed one.
	select {
	case <-st.Changed():
		t.Fatal("the channel was not replaced, so every later request returns at once")
	default:
	}
}

// Signing in, signing out and issuing a session all go through Write, and none of them
// change anything a television is sent. /api/parent/login/begin is public, so if those
// broadcast, anyone who can load the sign-in page could make every TV resync in a loop.
func TestWritesNoTVCaresAboutDoNotBroadcast(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var startRevision int64
	st.Read(func(state *State, _ Usage) { startRevision = state.Revision })
	changed := st.Changed()

	for name, mutate := range map[string]func(*State){
		"a session issued at sign-in": func(state *State) {
			state.Sessions = append(state.Sessions, Session{Token: "t", ParentID: "p"})
		},
		"a parent's passkey": func(state *State) {
			state.Parents = append(state.Parents, Parent{ID: "p", Name: "Parent"})
		},
		"block history and app labels": func(state *State) {
			state.RecentBlocks = append(state.RecentBlocks, BlockEvent{PackageName: "com.example"})
			state.AppLabels["com.example"] = "Example"
		},
		"a pairing code": func(state *State) {
			state.PairingCodes = append(state.PairingCodes, PairingCode{Code: "123456"})
		},
	} {
		if err := st.Write(func(state *State, _ Usage) error {
			mutate(state)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-changed:
			t.Fatalf("%s woke every television", name)
		default:
		}
	}

	var endRevision int64
	st.Read(func(state *State, _ Usage) { endRevision = state.Revision })
	if endRevision != startRevision {
		t.Errorf("revision moved %d -> %d, so every TV would resync for news it cannot see",
			startRevision, endRevision)
	}

	// The same store still broadcasts the writes that do matter.
	if err := st.Write(func(state *State, _ Usage) error {
		state.Policy.Version++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Error("a policy change did not broadcast")
	}
}

// Touch is the sync chatter path. If it broadcast, every usage report a television sent
// would wake that same television's held request, and the two would spin.
func TestTouchDoesNotBroadcast(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	changed := st.Changed()
	st.Touch(func(state *State, _ Usage) { state.Devices = append(state.Devices, Device{ID: "tv"}) })
	select {
	case <-changed:
		t.Error("Touch woke the held-request channel")
	default:
	}
}

// ---------- what actually reaches the disk ----------

// fileStamp identifies a particular written file. Every write here is a fresh temp file
// renamed into place, so a rewrite always moves the modification time even when the bytes
// are identical -- which is exactly what this is watching for.
func fileStamp(t *testing.T, dir, name string) (time.Time, int64) {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("stat %s: %v", name, err)
	}
	return info.ModTime(), info.Size()
}

// A television reports usage every few seconds and stamps itself last-seen on every
// request. Before this, each flush rewrote the whole of state.json -- policy, sessions,
// and a block history that is by far the largest thing in it -- to record a usage count
// and a timestamp that do not live there at all.
func TestAUsageFlushDoesNotRewriteTheStateFile(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Something substantial in state.json, so a needless rewrite would be a real cost.
	err = st.Write(func(s *State, _ Usage) error {
		s.Devices = append(s.Devices, Device{ID: "tv1", Name: "Living room", TokenHash: "h"})
		for i := 0; i < 200; i++ {
			s.RecentBlocks = append(s.RecentBlocks, BlockEvent{
				DeviceID: "tv1", PackageName: "com.example.app", Reason: "NOT_IN_ALLOWLIST",
				AtMillis: time.Now().UnixMilli(),
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	stateAt, stateSize := fileStamp(t, dir, stateFile)
	usageAt, _ := fileStamp(t, dir, usageFile)
	seenAt, _ := fileStamp(t, dir, lastSeenFile)
	if stateSize < 10_000 {
		t.Fatalf("state.json is only %d bytes; this test is not measuring anything", stateSize)
	}
	time.Sleep(10 * time.Millisecond) // so a rewrite is distinguishable by mtime

	// Exactly what a television's traffic does: usage in, last-seen stamped.
	st.Touch(func(s *State, usage Usage) {
		usage.MergeUsage("tv1", []model.DayUsage{{
			Day:           time.Now().Format("2006-01-02"),
			PerAppSeconds: map[string]int64{"com.example.app": 120},
		}})
		if d := s.DeviceByID("tv1"); d != nil {
			d.LastSeenMillis = time.Now().UnixMilli()
		}
	})
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}

	if at, _ := fileStamp(t, dir, stateFile); !at.Equal(stateAt) {
		t.Errorf("state.json was rewritten for a change that is not in it (%v -> %v)", stateAt, at)
	}
	if at, _ := fileStamp(t, dir, usageFile); at.Equal(usageAt) {
		t.Error("usage.json was not written, so the usage report was lost")
	}
	if at, _ := fileStamp(t, dir, lastSeenFile); at.Equal(seenAt) {
		t.Error("lastseen.json was not written, so the stamp was lost")
	}
}

func TestAFlushWithNothingNewWritesNothing(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(func(s *State, _ Usage) error { s.Policy.Version = 3; return nil }); err != nil {
		t.Fatal(err)
	}
	before := map[string]time.Time{}
	for _, f := range []string{stateFile, usageFile, lastSeenFile} {
		before[f], _ = fileStamp(t, dir, f)
	}
	time.Sleep(10 * time.Millisecond)

	// Touch marks the store dirty without changing anything, which is what a repeat of an
	// identical usage report amounts to.
	st.Touch(func(*State, Usage) {})
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{stateFile, usageFile, lastSeenFile} {
		if at, _ := fileStamp(t, dir, f); !at.Equal(before[f]) {
			t.Errorf("%s was rewritten although nothing changed", f)
		}
	}
}

// An upgrade must not make every television look offline: the stamp used to live inside
// state.json, and the first start after the move has no lastseen.json to read.
func TestLastSeenIsCarriedOverFromAnOlderStateFile(t *testing.T) {
	dir := t.TempDir()
	const legacy = `{"devices":[{"id":"tv1","name":"Living room","tokenHash":"h","lastSeenMillis":1762400000000}]}`
	if err := os.WriteFile(filepath.Join(dir, stateFile), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Read(func(s *State, _ Usage) {
		d := s.DeviceByID("tv1")
		if d == nil {
			t.Fatal("the device did not survive the upgrade")
		}
		if d.LastSeenMillis != 1762400000000 {
			t.Errorf("last seen = %d, want the value the old file carried", d.LastSeenMillis)
		}
	})
	// And it must not be written back, or the old field would live on forever.
	if err := st.Write(func(*State, Usage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("lastSeenMillis")) {
		t.Error("state.json still carries lastSeenMillis after being rewritten")
	}
}
