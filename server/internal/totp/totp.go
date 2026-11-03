// Package totp generates the offline escape codes.
//
// Third implementation of the same thing, after Kotlin (core-rules/Totp.kt) and
// JavaScript (web/app.js). They are kept honest by all three testing the RFC 6238
// vectors: if the TV and the phone ever disagree, a parent is locked out of their own
// television, so the vectors are the contract.
//
// HMAC-SHA1 / 6 digits / 30s, which is also what every authenticator app defaults to —
// so the secret can be imported into one as a backup if the parent's phone is lost.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
)

const (
	StepSeconds = 30
	Digits      = 6
	SecretBytes = 20
)

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a fresh base32 secret.
func NewSecret() (string, error) {
	buf := make([]byte, SecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return encoding.EncodeToString(buf), nil
}

func DecodeSecret(secretBase32 string) ([]byte, error) {
	clean := strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").Replace(secretBase32))
	return encoding.DecodeString(clean)
}

// CounterAt is the TOTP step number covering unixMillis.
func CounterAt(unixMillis int64, stepSeconds int) int64 {
	seconds := unixMillis / 1000
	if unixMillis < 0 && unixMillis%1000 != 0 {
		seconds--
	}
	return floorDiv(seconds, int64(stepSeconds))
}

// Code returns the code shown at unixMillis.
func Code(secret []byte, unixMillis int64, stepSeconds, digits int) string {
	return HOTP(secret, CounterAt(unixMillis, stepSeconds), digits)
}

func HOTP(secret []byte, counter int64, digits int) string {
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], uint64(counter))

	mac := hmac.New(sha1.New, secret)
	mac.Write(message[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	value := (int64(sum[offset]&0x7f) << 24) |
		(int64(sum[offset+1]) << 16) |
		(int64(sum[offset+2]) << 8) |
		int64(sum[offset+3])

	mod := int64(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, value%mod)
}

// SecondsRemaining says how long the current code stays valid, for the countdown ring
// in the parent app.
func SecondsRemaining(unixMillis int64, stepSeconds int) int {
	return stepSeconds - int((unixMillis/1000)%int64(stepSeconds))
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}
