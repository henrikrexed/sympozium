package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/sympozium-ai/sympozium/internal/channel"
)

// captureSendPayload runs sendMessage with the given channel state and message
// and returns the decoded chat.postMessage body that was sent to Slack.
func captureSendPayload(t *testing.T, mutate func(*SlackChannel), msg channel.OutboundMessage) map[string]interface{} {
	t.Helper()
	var capturedURL string
	var capturedBody []byte
	sc := newTestSlackChannel(func(req *http.Request) (*http.Response, error) {
		capturedURL = req.URL.String()
		buf, _ := io.ReadAll(req.Body)
		capturedBody = buf
		return jsonResponse(`{"ok":true}`), nil
	})
	if mutate != nil {
		mutate(sc)
	}
	if err := sc.sendMessage(context.Background(), msg); err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if capturedURL != "https://slack.com/api/chat.postMessage" {
		t.Fatalf("URL = %s", capturedURL)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return payload
}

// The guarantee from upstream #235: with no attribution configured and none on
// the message, the payload is exactly channel+text (identical to before).
func TestSendMessage_NoAttribution_PayloadUnchanged(t *testing.T) {
	payload := captureSendPayload(t, nil, channel.OutboundMessage{
		Channel: "slack", ChatID: "C123", Text: "hello",
	})
	for _, k := range []string{"username", "icon_url", "icon_emoji"} {
		if _, ok := payload[k]; ok {
			t.Errorf("payload should not carry %q when no attribution is set", k)
		}
	}
	if payload["channel"] != "C123" || payload["text"] != "hello" {
		t.Errorf("unexpected base payload: %v", payload)
	}
}

// A per-message identity is mapped onto the chat.postMessage overrides.
func TestSendMessage_PerMessageAttribution(t *testing.T) {
	payload := captureSendPayload(t, nil, channel.OutboundMessage{
		Channel: "slack", ChatID: "C123", Text: "hi",
		Username: "Winston", IconEmoji: ":bricks:",
	})
	if payload["username"] != "Winston" {
		t.Errorf("username = %v", payload["username"])
	}
	if payload["icon_emoji"] != ":bricks:" {
		t.Errorf("icon_emoji = %v", payload["icon_emoji"])
	}
}

// The pod's per-agent identity is used as a fallback when the message carries
// no explicit attribution.
func TestSendMessage_PodIdentityFallback(t *testing.T) {
	payload := captureSendPayload(t, func(sc *SlackChannel) {
		sc.displayName = "Architect"
		sc.iconURL = "https://example.com/a.png"
	}, channel.OutboundMessage{Channel: "slack", ChatID: "C123", Text: "hi"})
	if payload["username"] != "Architect" {
		t.Errorf("username = %v (want pod displayName fallback)", payload["username"])
	}
	if payload["icon_url"] != "https://example.com/a.png" {
		t.Errorf("icon_url = %v", payload["icon_url"])
	}
}

// A per-message identity overrides the pod default.
func TestSendMessage_MessageOverridesPodIdentity(t *testing.T) {
	payload := captureSendPayload(t, func(sc *SlackChannel) {
		sc.displayName = "Architect"
	}, channel.OutboundMessage{
		Channel: "slack", ChatID: "C123", Text: "hi", Username: "John",
	})
	if payload["username"] != "John" {
		t.Errorf("username = %v (message should override pod default)", payload["username"])
	}
}

// icon_url and icon_emoji are mutually exclusive: a URL wins and no icon_emoji
// key is emitted alongside it.
func TestSendMessage_IconURLBeatsEmoji(t *testing.T) {
	payload := captureSendPayload(t, nil, channel.OutboundMessage{
		Channel: "slack", ChatID: "C123", Text: "hi",
		IconURL: "https://example.com/a.png", IconEmoji: ":bricks:",
	})
	if payload["icon_url"] != "https://example.com/a.png" {
		t.Errorf("icon_url = %v", payload["icon_url"])
	}
	if _, ok := payload["icon_emoji"]; ok {
		t.Error("icon_emoji must not be sent when icon_url is present")
	}
}
