package main

import "testing"

func TestShouldAttemptJoin(t *testing.T) {
	tests := []struct {
		name     string
		chatID   string
		slackErr string
		want     bool
	}{
		{"public channel not_in_channel", "C123", "not_in_channel", true},
		{"public channel channel_not_found", "C123", "channel_not_found", true},
		{"private channel not joinable via API", "G123", "not_in_channel", false},
		{"DM not joinable", "D123", "channel_not_found", false},
		{"unrelated error not retried", "C123", "msg_too_long", false},
		{"empty error not retried", "C123", "", false},
		{"empty chat id", "", "not_in_channel", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldAttemptJoin(tt.chatID, tt.slackErr); got != tt.want {
				t.Errorf("shouldAttemptJoin(%q, %q) = %v, want %v", tt.chatID, tt.slackErr, got, tt.want)
			}
		})
	}
}
