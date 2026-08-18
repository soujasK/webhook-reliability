package worker

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestSign(t *testing.T) {
	payload := []byte(`{"amount":499}`)
	secret := "demo-secret"

	got := sign(payload, secret)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	want := hex.EncodeToString(mac.Sum(nil))

	if got != want {
		t.Fatalf("sign() = %q, want %q", got, want)
	}

	// Different secrets must not collide, and the result must always be usable as a header value
	// (hex, no surprise characters) since it's sent as X-Signature.
	if other := sign(payload, "different-secret"); other == got {
		t.Fatal("sign() produced the same signature for two different secrets")
	}
	if len(got) != sha256.Size*2 {
		t.Fatalf("sign() length = %d, want %d (hex-encoded SHA-256)", len(got), sha256.Size*2)
	}
}

func TestBackoffBase(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 1, want: 2 * time.Second},
		{attempts: 2, want: 4 * time.Second},
		{attempts: 3, want: 8 * time.Second},
		{attempts: 4, want: 16 * time.Second},
		{attempts: 5, want: 32 * time.Second},
	}
	for _, c := range cases {
		if got := backoffBase(c.attempts); got != c.want {
			t.Errorf("backoffBase(%d) = %v, want %v", c.attempts, got, c.want)
		}
	}
}

func TestBackoffBaseIsMonotonic(t *testing.T) {
	// Each additional attempt should back off strictly longer than the last, otherwise
	// exponential backoff isn't actually happening.
	prev := time.Duration(0)
	for attempts := 1; attempts <= 8; attempts++ {
		d := backoffBase(attempts)
		if d <= prev {
			t.Fatalf("backoffBase(%d) = %v is not greater than backoffBase(%d) = %v", attempts, d, attempts-1, prev)
		}
		prev = d
	}
}
