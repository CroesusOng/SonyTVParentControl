// Package model is the wire contract shared with the Android TV app.
//
// Every type here has a one-to-one counterpart in
// core-rules/src/main/kotlin/dev/homelab/braviaguard/core/Protocol.kt. The JSON tags are
// the contract: primitives only, epoch milliseconds for instants, "HH:mm" for times and
// weekday names for days, so neither side has to agree about a date library. Changing a
// tag here without changing the Kotlin DTO silently breaks a TV in someone's living room.
package model

// Curfew is a recurring blocked window. Days name the day the window opens.
type Curfew struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Days     []string `json:"days"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Packages []string `json:"packages,omitempty"`
}

// Policy is the complete rule set. Version is bumped on every change so a TV that
// already has this version can be sent an empty sync response.
type Policy struct {
	Version             int64          `json:"version"`
	EnforcementEnabled  bool           `json:"enforcementEnabled"`
	ListMode            string         `json:"listMode"`
	Allowlist           []string       `json:"allowlist"`
	Blocklist           []string       `json:"blocklist"`
	ExemptPackages      []string       `json:"exemptPackages"`
	DailyMinutes        map[string]int `json:"dailyMinutes"`
	WeeklyMinutes       *int           `json:"weeklyMinutes,omitempty"`
	PerAppDailyMinutes  map[string]int `json:"perAppDailyMinutes"`
	Curfews             []Curfew       `json:"curfews"`
	OfflineBehavior     string         `json:"offlineBehavior"`
	OfflineGraceMinutes int            `json:"offlineGraceMinutes"`
}

// DefaultPolicy is what a fresh install serves: inert. Nothing is enforced until a
// parent turns it on, so a half-finished setup cannot lock the family out of the TV.
func DefaultPolicy() Policy {
	return Policy{
		Version:             1,
		EnforcementEnabled:  false,
		ListMode:            "OFF",
		Allowlist:           []string{},
		Blocklist:           []string{},
		ExemptPackages:      []string{},
		DailyMinutes:        map[string]int{},
		PerAppDailyMinutes:  map[string]int{},
		Curfews:             []Curfew{},
		OfflineBehavior:     "KEEP_ENFORCING",
		OfflineGraceMinutes: 60,
	}
}

// Grant is a temporary permission issued by a parent. Source is PARENT_APP here;
// OFFLINE_CODE grants are minted on the TV and never reach the server.
type Grant struct {
	ID              string   `json:"id"`
	Source          string   `json:"source"`
	IssuedAtMillis  int64    `json:"issuedAtMillis"`
	ExpiresAtMillis int64    `json:"expiresAtMillis"`
	Packages        []string `json:"packages,omitempty"`
	BypassList      bool     `json:"bypassList"`
	BypassCurfew    bool     `json:"bypassCurfew"`
	BypassBudget    bool     `json:"bypassBudget"`
	Note            string   `json:"note,omitempty"`
}

type SyncResponse struct {
	ServerTimeMillis int64   `json:"serverTimeMillis"`
	Policy           *Policy `json:"policy,omitempty"`
	Grants           []Grant `json:"grants"`
	// Revision is echoed back on the next request so the backend can hold that request
	// open until something actually changes. See api.handleSync.
	Revision           int64  `json:"revision"`
	EscapeSecretBase32 string `json:"escapeSecretBase32,omitempty"`
}

type DayUsage struct {
	Day           string           `json:"day"`
	PerAppSeconds map[string]int64 `json:"perAppSeconds"`
}

type UsageReportRequest struct {
	DeviceID string     `json:"deviceId"`
	Days     []DayUsage `json:"days"`
	// Package name to human label, so the parent app can offer a pick list instead of
	// asking anyone to type "com.google.android.youtube.tv".
	Labels map[string]string `json:"labels,omitempty"`
}

type EnrollRequest struct {
	PairingCode    string `json:"pairingCode"`
	DeviceName     string `json:"deviceName"`
	Model          string `json:"model"`
	AndroidRelease string `json:"androidRelease"`
}

type EnrollResponse struct {
	DeviceID           string `json:"deviceId"`
	Token              string `json:"token"`
	EscapeSecretBase32 string `json:"escapeSecretBase32"`
	ServerTimeMillis   int64  `json:"serverTimeMillis"`
}

type BlockEventRequest struct {
	DeviceID    string `json:"deviceId"`
	PackageName string `json:"packageName"`
	AppLabel    string `json:"appLabel,omitempty"`
	Reason      string `json:"reason"`
	AtMillis    int64  `json:"atMillis"`
}

type APIError struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}
