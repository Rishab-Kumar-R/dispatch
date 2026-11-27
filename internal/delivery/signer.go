package delivery

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderEventID    = "Dispatch-Event-Id"
	HeaderDeliveryID = "Dispatch-Delivery-Id"
	HeaderAttempt    = "Dispatch-Attempt"
	HeaderTimestamp  = "Dispatch-Timestamp"
	HeaderSignature  = "Dispatch-Signature"

	signatureVersion = "v1"
	secretPrefix     = "whsec_"
)

var (
	ErrNoSecrets               = errors.New("delivery: no signing secrets")
	ErrMalformedSignature      = errors.New("delivery: malformed signature header")
	ErrInvalidTimestamp        = errors.New("delivery: invalid timestamp")
	ErrTimestampOutOfTolerance = errors.New("delivery: timestamp outside tolerance")
	ErrSignatureMismatch       = errors.New("delivery: no matching signature")
)

// NewSecret returns a fresh signing secret: "whsec_" + 32 random bytes.
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}

	return secretPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// Sign returns hex(HMAC-SHA256(secret, "<ts>.<body>")).
func Sign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)

	return hex.EncodeToString(mac.Sum(nil))
}

// SignatureHeader signs with every live secret: "v1=<hex>,v1=<hex>".
// During rotation the receiver accepts a match against any of them.
func SignatureHeader(secrets []string, ts int64, body []byte) string {
	parts := make([]string, 0, len(secrets))
	for _, s := range secrets {
		parts = append(parts, signatureVersion+"="+Sign(s, ts, body))
	}

	return strings.Join(parts, ",")
}

type HeaderInput struct {
	EventID    string
	DeliveryID string
	Attempt    int
	Timestamp  time.Time
	Secrets    []string
	Body       []byte
}

// BuildHeaders returns the full header set for one delivery attempt.
func BuildHeaders(in HeaderInput) (http.Header, error) {
	if len(in.Secrets) == 0 {
		return nil, ErrNoSecrets
	}

	ts := in.Timestamp.Unix()
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set(HeaderEventID, in.EventID)
	h.Set(HeaderDeliveryID, in.DeliveryID)
	h.Set(HeaderAttempt, strconv.Itoa(in.Attempt))
	h.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	h.Set(HeaderSignature, SignatureHeader(in.Secrets, ts, in.Body))

	return h, nil
}

// Verify is the receiver-side check. We ship it for our docs/SDK and our own tests.
func Verify(secrets []string, signatureHeader, timestampHeader string, body []byte, tolerance time.Duration, now time.Time) error {
	ts, err := strconv.ParseInt(strings.TrimSpace(timestampHeader), 10, 64)
	if err != nil {
		return ErrInvalidTimestamp
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return ErrTimestampOutOfTolerance
	}

	var got [][]byte
	for part := range strings.SplitSeq(signatureHeader, ",") {
		ver, sig, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return ErrMalformedSignature
		}
		if ver != signatureVersion {
			continue
		}
		raw, err := hex.DecodeString(sig)
		if err != nil {
			return ErrMalformedSignature
		}
		got = append(got, raw)
	}
	if len(got) == 0 {
		return ErrMalformedSignature
	}

	matched := false
	for _, s := range secrets {
		want, _ := hex.DecodeString(Sign(s, ts, body))
		for _, g := range got {
			if hmac.Equal(want, g) {
				matched = true
			}
		}
	}
	if !matched {
		return ErrSignatureMismatch
	}

	return nil
}
