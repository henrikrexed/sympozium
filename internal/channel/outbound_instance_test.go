package channel

import (
	"testing"

	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

// TestOutboundIsForInstance covers the ISI-1436 fan-out guard: every persona's
// channel pod receives every channel.message.send event, so only the pod whose
// instance matches the stamped owner may post. Mismatched instances must drop
// the event; an unstamped event must fail open (older publishers).
func TestOutboundIsForInstance(t *testing.T) {
	cases := []struct {
		name     string
		stamped  string
		instance string
		want     bool
	}{
		{"own instance posts", "bmad-ensemble-product-manager", "bmad-ensemble-product-manager", true},
		{"other instance drops", "bmad-ensemble-product-manager", "bmad-ensemble-cto", false},
		{"unstamped fails open", "", "bmad-ensemble-cto", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := &eventbus.Event{Metadata: map[string]string{"instanceName": tc.stamped}}
			if got := OutboundIsForInstance(ev, tc.instance); got != tc.want {
				t.Fatalf("OutboundIsForInstance(stamped=%q, instance=%q) = %v, want %v",
					tc.stamped, tc.instance, got, tc.want)
			}
		})
	}

	t.Run("nil event fails open", func(t *testing.T) {
		if !OutboundIsForInstance(nil, "x") {
			t.Fatal("nil event should fail open (true)")
		}
	})

	t.Run("nil metadata fails open", func(t *testing.T) {
		if !OutboundIsForInstance(&eventbus.Event{}, "x") {
			t.Fatal("event with nil metadata should fail open (true)")
		}
	})
}
