package controller

import (
	"testing"
	"time"

	channelpkg "github.com/sympozium-ai/sympozium/internal/channel"
)

func TestInboundDedupKey(t *testing.T) {
	tests := []struct {
		name string
		msg  channelpkg.InboundMessage
		want string
	}{
		{
			name: "prefers slack event id",
			msg: channelpkg.InboundMessage{
				Channel: "slack", ChatID: "C123",
				Metadata: map[string]string{"slackEventId": "Ev08AB", "slackClientMsgId": "uuid-1", "ts": "1.2"},
			},
			want: "slack/Ev08AB",
		},
		{
			name: "falls back to client_msg_id",
			msg: channelpkg.InboundMessage{
				Channel: "slack", ChatID: "C123",
				Metadata: map[string]string{"slackClientMsgId": "uuid-1", "ts": "1.2"},
			},
			want: "slack/uuid-1",
		},
		{
			name: "falls back to channel+chat+ts",
			msg: channelpkg.InboundMessage{
				Channel: "slack", ChatID: "C123",
				Metadata: map[string]string{"ts": "1.2"},
			},
			want: "slack/C123/1.2",
		},
		{
			name: "no stable key yields empty (no dedup)",
			msg:  channelpkg.InboundMessage{Channel: "telegram", ChatID: "C123"},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inboundDedupKey(tt.msg); got != tt.want {
				t.Fatalf("inboundDedupKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAlreadyProcessed(t *testing.T) {
	cr := &ChannelRouter{}
	now := time.Now()

	// First sighting of a key is not a duplicate; the second within TTL is.
	if cr.alreadyProcessed("slack/Ev1", now) {
		t.Fatal("first delivery should not be a duplicate")
	}
	if !cr.alreadyProcessed("slack/Ev1", now) {
		t.Fatal("second delivery within TTL should be a duplicate")
	}

	// A different key is independent.
	if cr.alreadyProcessed("slack/Ev2", now) {
		t.Fatal("distinct key should not be a duplicate")
	}

	// Empty keys are never deduped (preserves behaviour for channels without ids).
	if cr.alreadyProcessed("", now) || cr.alreadyProcessed("", now) {
		t.Fatal("empty key must never be treated as a duplicate")
	}

	// After the TTL window the key is forgotten and accepted again.
	if cr.alreadyProcessed("slack/Ev1", now.Add(dedupTTL+time.Minute)) {
		t.Fatal("key past TTL should be accepted again")
	}
}
