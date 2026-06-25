package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// signSlack produces a valid X-Slack-Signature for the given body/timestamp,
// mirroring how Slack signs outbound requests.
func signSlack(secret, body string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(slackSignatureVersion + ":" + strconv.FormatInt(ts, 10) + ":"))
	mac.Write([]byte(body))
	return slackSignatureVersion + "=" + hex.EncodeToString(mac.Sum(nil))
}

func headerWith(ts int64, sig string) http.Header {
	h := http.Header{}
	if ts != 0 {
		h.Set("X-Slack-Request-Timestamp", strconv.FormatInt(ts, 10))
	}
	if sig != "" {
		h.Set("X-Slack-Signature", sig)
	}
	return h
}

func TestVerifySlackSignature_Valid(t *testing.T) {
	const secret = "8f742231b10e8888abcd99yyyzzz85a5"
	body := `{"type":"event_callback","event":{"type":"message"}}`
	now := time.Unix(1_700_000_000, 0)
	ts := now.Unix()

	err := verifySlackSignature(secret, headerWith(ts, signSlack(secret, body, ts)), []byte(body), now)
	if err != nil {
		t.Fatalf("expected valid signature to pass, got: %v", err)
	}
}

func TestVerifySlackSignature_WrongSecret(t *testing.T) {
	const secret = "real-secret"
	body := `{"hello":"world"}`
	now := time.Unix(1_700_000_000, 0)
	ts := now.Unix()

	// Signed with a different secret than the one used to verify.
	sig := signSlack("attacker-secret", body, ts)
	err := verifySlackSignature(secret, headerWith(ts, sig), []byte(body), now)
	if err == nil {
		t.Fatal("expected signature mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected mismatch error, got: %v", err)
	}
}

func TestVerifySlackSignature_TamperedBody(t *testing.T) {
	const secret = "s3cr3t"
	body := `{"amount":1}`
	now := time.Unix(1_700_000_000, 0)
	ts := now.Unix()

	sig := signSlack(secret, body, ts)
	// Verify against a different body than was signed.
	err := verifySlackSignature(secret, headerWith(ts, sig), []byte(`{"amount":1000000}`), now)
	if err == nil {
		t.Fatal("expected mismatch on tampered body, got nil")
	}
}

func TestVerifySlackSignature_ExpiredTimestamp(t *testing.T) {
	const secret = "s3cr3t"
	body := `{"x":1}`
	now := time.Unix(1_700_000_000, 0)
	// Timestamp 6 minutes in the past — beyond the 5-minute window.
	ts := now.Add(-6 * time.Minute).Unix()

	err := verifySlackSignature(secret, headerWith(ts, signSlack(secret, body, ts)), []byte(body), now)
	if err == nil {
		t.Fatal("expected skew error for expired timestamp, got nil")
	}
	if !strings.Contains(err.Error(), "skew") {
		t.Fatalf("expected skew error, got: %v", err)
	}
}

func TestVerifySlackSignature_FutureTimestamp(t *testing.T) {
	const secret = "s3cr3t"
	body := `{"x":1}`
	now := time.Unix(1_700_000_000, 0)
	// 6 minutes in the future — also outside the window (replay/clock attack).
	ts := now.Add(6 * time.Minute).Unix()

	err := verifySlackSignature(secret, headerWith(ts, signSlack(secret, body, ts)), []byte(body), now)
	if err == nil {
		t.Fatal("expected skew error for future timestamp, got nil")
	}
}

func TestVerifySlackSignature_WithinWindow(t *testing.T) {
	const secret = "s3cr3t"
	body := `{"x":1}`
	now := time.Unix(1_700_000_000, 0)
	// 4 minutes old — still inside the 5-minute window.
	ts := now.Add(-4 * time.Minute).Unix()

	err := verifySlackSignature(secret, headerWith(ts, signSlack(secret, body, ts)), []byte(body), now)
	if err != nil {
		t.Fatalf("expected request within window to pass, got: %v", err)
	}
}

func TestVerifySlackSignature_MissingSecret(t *testing.T) {
	body := `{"x":1}`
	now := time.Unix(1_700_000_000, 0)
	ts := now.Unix()

	// Fail closed: no signing secret configured → reject.
	err := verifySlackSignature("", headerWith(ts, signSlack("anything", body, ts)), []byte(body), now)
	if err == nil {
		t.Fatal("expected error when no signing secret configured, got nil")
	}
}

func TestVerifySlackSignature_MissingHeaders(t *testing.T) {
	const secret = "s3cr3t"
	body := `{"x":1}`
	now := time.Unix(1_700_000_000, 0)
	ts := now.Unix()

	// Missing timestamp header.
	if err := verifySlackSignature(secret, headerWith(0, signSlack(secret, body, ts)), []byte(body), now); err == nil {
		t.Error("expected error for missing timestamp header, got nil")
	}
	// Missing signature header.
	if err := verifySlackSignature(secret, headerWith(ts, ""), []byte(body), now); err == nil {
		t.Error("expected error for missing signature header, got nil")
	}
}

func TestVerifySlackSignature_NonNumericTimestamp(t *testing.T) {
	const secret = "s3cr3t"
	body := `{"x":1}`
	now := time.Unix(1_700_000_000, 0)

	h := http.Header{}
	h.Set("X-Slack-Request-Timestamp", "not-a-number")
	h.Set("X-Slack-Signature", "v0=deadbeef")
	if err := verifySlackSignature(secret, h, []byte(body), now); err == nil {
		t.Fatal("expected error for non-numeric timestamp, got nil")
	}
}

func TestHandleSlackEvents_RejectsUnsigned(t *testing.T) {
	sc := &SlackChannel{SigningSecret: "s3cr3t", log: logr.Discard()}

	req := httptest.NewRequest(http.MethodPost, "/slack/events",
		strings.NewReader(`{"type":"url_verification","challenge":"abc"}`))
	rec := httptest.NewRecorder()
	sc.handleSlackEvents(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unsigned request, got %d", rec.Code)
	}
}

func TestHandleSlackEvents_AcceptsSignedChallenge(t *testing.T) {
	const secret = "s3cr3t"
	sc := &SlackChannel{SigningSecret: secret, log: logr.Discard()}

	body := `{"type":"url_verification","challenge":"the-challenge"}`
	now := time.Now()
	ts := now.Unix()

	req := httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Slack-Signature", signSlack(secret, body, ts))

	rec := httptest.NewRecorder()
	sc.handleSlackEvents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for signed challenge, got %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "the-challenge" {
		t.Fatalf("expected challenge echoed, got %q", got)
	}
}
