package main

import (
	"testing"

	"github.com/alexsjones/sympozium/internal/channel"
)

func TestBuildPostMessagePayload_Attribution(t *testing.T) {
	tests := []struct {
		name            string
		msg             channel.OutboundMessage
		defaultUsername string
		wantUsername    interface{} // nil => key must be absent
		wantIconURL     interface{}
		wantIconEmoji   interface{}
	}{
		{
			name:            "message username overrides pod default",
			msg:             channel.OutboundMessage{ChatID: "C1", Text: "hi", Username: "Architect (Winston)"},
			defaultUsername: "Crew Manager (CTO)",
			wantUsername:    "Architect (Winston)",
		},
		{
			name:            "falls back to pod display name",
			msg:             channel.OutboundMessage{ChatID: "C1", Text: "hi"},
			defaultUsername: "Crew Manager (CTO)",
			wantUsername:    "Crew Manager (CTO)",
		},
		{
			name:            "no username when both empty",
			msg:             channel.OutboundMessage{ChatID: "C1", Text: "hi"},
			defaultUsername: "",
			wantUsername:    nil,
		},
		{
			name:            "icon_url wins over icon_emoji when both set",
			msg:             channel.OutboundMessage{ChatID: "C1", Text: "hi", IconURL: "https://x/a.png", IconEmoji: ":robot_face:"},
			defaultUsername: "Bot",
			wantUsername:    "Bot",
			wantIconURL:     "https://x/a.png",
			wantIconEmoji:   nil,
		},
		{
			name:            "icon_emoji used when no url",
			msg:             channel.OutboundMessage{ChatID: "C1", Text: "hi", IconEmoji: ":robot_face:"},
			defaultUsername: "Bot",
			wantUsername:    "Bot",
			wantIconEmoji:   ":robot_face:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildPostMessagePayload(tt.msg, tt.defaultUsername)

			// Channel and text are always present.
			if got["channel"] != tt.msg.ChatID || got["text"] != tt.msg.Text {
				t.Fatalf("base fields wrong: %#v", got)
			}
			assertField(t, got, "username", tt.wantUsername)
			assertField(t, got, "icon_url", tt.wantIconURL)
			assertField(t, got, "icon_emoji", tt.wantIconEmoji)
		})
	}
}

func assertField(t *testing.T, payload map[string]interface{}, key string, want interface{}) {
	t.Helper()
	got, present := payload[key]
	if want == nil {
		if present {
			t.Errorf("expected %q absent, got %v", key, got)
		}
		return
	}
	if !present {
		t.Errorf("expected %q=%v, but key absent", key, want)
		return
	}
	if got != want {
		t.Errorf("%q = %v, want %v", key, got, want)
	}
}
