package totp

import "testing"

// RFC 6238 appendix B, SHA-1 column. The Kotlin and JavaScript implementations assert
// the same numbers; that is what guarantees a code shown on the phone is accepted by
// the TV.
func TestRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	cases := []struct {
		seconds int64
		want    string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, tc := range cases {
		if got := Code(secret, tc.seconds*1000, StepSeconds, 8); got != tc.want {
			t.Errorf("Code(T=%d) = %s, want %s", tc.seconds, got, tc.want)
		}
	}
}

func TestCounterAt(t *testing.T) {
	cases := []struct {
		millis int64
		want   int64
	}{
		{59_000, 1},
		{60_000, 2},
		{89_999, 2},
		{1111111109 * 1000, 0x23523EC},
	}
	for _, tc := range cases {
		if got := CounterAt(tc.millis, StepSeconds); got != tc.want {
			t.Errorf("CounterAt(%d) = %d, want %d", tc.millis, got, tc.want)
		}
	}
}

func TestSecretRoundTrip(t *testing.T) {
	secret, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSecret(secret)
	if err != nil {
		t.Fatalf("decode %q: %v", secret, err)
	}
	if len(decoded) != SecretBytes {
		t.Errorf("secret is %d bytes, want %d", len(decoded), SecretBytes)
	}
	// Separators a person might type when reading it off a screen.
	if _, err := DecodeSecret(secret[:4] + " " + secret[4:8] + "-" + secret[8:]); err != nil {
		t.Errorf("should tolerate separators: %v", err)
	}
}

func TestSecondsRemaining(t *testing.T) {
	if got := SecondsRemaining(60_000, 30); got != 30 {
		t.Errorf("at a step boundary: got %d, want 30", got)
	}
	if got := SecondsRemaining(61_000, 30); got != 29 {
		t.Errorf("one second in: got %d, want 29", got)
	}
}

func TestDefaultDigits(t *testing.T) {
	secret := []byte("12345678901234567890")
	code := Code(secret, 1_700_000_000_000, StepSeconds, Digits)
	if len(code) != Digits {
		t.Fatalf("code %q is %d digits, want %d", code, len(code), Digits)
	}
}
