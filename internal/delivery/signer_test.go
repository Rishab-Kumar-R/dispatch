package delivery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignMatchesSpec(t *testing.T) {
	// Documents the wire format: HMAC-SHA256 over "<timestamp>.<body>", hex-encoded.
	mac := hmac.New(sha256.New, []byte("whsec_test"))
	mac.Write([]byte(`1700000000.{"a":1}`))
	want := hex.EncodeToString(mac.Sum(nil))

	if got := Sign("whsec_test", 1700000000, []byte(`{"a":1}`)); got != want {
		t.Fatalf("Sign = %s, want %s", got, want)
	}
}

func TestVerifyRoundTrip(t *testing.T) {
	now := time.Unix(1700000000, 0)
	body := []byte(`{"hello":"world"}`)
	h, err := BuildHeaders(HeaderInput{
		EventID: "evt_1", DeliveryID: "del_1", Attempt: 1,
		Timestamp: now, Secrets: []string{"s1"}, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.Get(HeaderEventID) != "evt_1" || h.Get(HeaderAttempt) != "1" {
		t.Fatalf("unexpected headers: %v", h)
	}
	if err := Verify([]string{"s1"}, h.Get(HeaderSignature), h.Get(HeaderTimestamp), body, 5*time.Minute, now); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	now := time.Unix(1700000000, 0)
	body := []byte(`{"a":1}`)
	sig := SignatureHeader([]string{"s1"}, now.Unix(), body)
	ts := "1700000000"

	cases := []struct {
		name    string
		secrets []string
		sig, ts string
		body    []byte
		now     time.Time
		want    error
	}{
		{"tampered body", []string{"s1"}, sig, ts, []byte(`{"a":2}`), now, ErrSignatureMismatch},
		{"wrong secret", []string{"other"}, sig, ts, body, now, ErrSignatureMismatch},
		{"no secrets", nil, sig, ts, body, now, ErrSignatureMismatch},
		{"stale timestamp", []string{"s1"}, sig, ts, body, now.Add(10 * time.Minute), ErrTimestampOutOfTolerance},
		{"future timestamp", []string{"s1"}, sig, ts, body, now.Add(-10 * time.Minute), ErrTimestampOutOfTolerance},
		{"bad timestamp", []string{"s1"}, sig, "abc", body, now, ErrInvalidTimestamp},
		{"empty header", []string{"s1"}, "", ts, body, now, ErrMalformedSignature},
		{"no version", []string{"s1"}, "deadbeef", ts, body, now, ErrMalformedSignature},
		{"bad hex", []string{"s1"}, "v1=zzzz", ts, body, now, ErrMalformedSignature},
		{"only unknown version", []string{"s1"}, "v9=abcd", ts, body, now, ErrMalformedSignature},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Verify(tc.secrets, tc.sig, tc.ts, tc.body, 5*time.Minute, tc.now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestVerifyDuringRotation(t *testing.T) {
	now := time.Unix(1700000000, 0)
	body := []byte(`{"a":1}`)

	sig := SignatureHeader([]string{"new", "old"}, now.Unix(), body)
	if strings.Count(sig, "v1=") != 2 {
		t.Fatalf("expected two signatures, got %q", sig)
	}
	ts := "1700000000"
	for _, secrets := range [][]string{{"new"}, {"old"}, {"old", "new"}} {
		if err := Verify(secrets, sig, ts, body, time.Minute, now); err != nil {
			t.Fatalf("secrets %v: %v", secrets, err)
		}
	}
	if err := Verify([]string{"unrelated"}, sig, ts, body, time.Minute, now); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("got %v", err)
	}
}

func TestBuildHeadersRequiresSecret(t *testing.T) {
	if _, err := BuildHeaders(HeaderInput{Timestamp: time.Now()}); !errors.Is(err, ErrNoSecrets) {
		t.Fatalf("got %v", err)
	}
}

func TestNewSecret(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSecret()
	if !strings.HasPrefix(a, "whsec_") || a == b || len(a) < 40 {
		t.Fatalf("bad secrets: %q %q", a, b)
	}
}
