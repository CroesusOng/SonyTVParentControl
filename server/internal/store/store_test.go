package store

import (
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
