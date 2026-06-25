package eventbus

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// runEmbeddedJetStream starts an in-process NATS server with JetStream enabled
// and returns its client URL. The server is shut down when the test finishes.
func runEmbeddedJetStream(t *testing.T) string {
	t.Helper()
	opts := &natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1, // ephemeral port
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("creating embedded NATS server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS server did not become ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

// publishSeq publishes n events to topic, each carrying its sequence number, so
// tests can assert which messages were delivered to which subscribers.
func publishSeq(ctx context.Context, t *testing.T, bus *NATSEventBus, topic string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		ev, err := NewEvent(topic, nil, map[string]int{"seq": i})
		if err != nil {
			t.Fatalf("building event %d: %v", i, err)
		}
		if err := bus.Publish(ctx, topic, ev); err != nil {
			t.Fatalf("publishing event %d: %v", i, err)
		}
	}
}

func seqOf(t *testing.T, ev *Event) int {
	t.Helper()
	var p struct {
		Seq int `json:"seq"`
	}
	if err := json.Unmarshal(ev.Data, &p); err != nil {
		t.Fatalf("decoding event payload: %v", err)
	}
	return p.Seq
}

// TestSubscribeGroupSingleDelivery verifies the ISI-1430 acceptance: with more
// than one subscriber in the same queue group, each published event is delivered
// to exactly one of them (load-balanced), never fanned out to both. This is the
// chokepoint that prevents duplicate processing if a controller ever runs with
// replicas>1 without leader election.
func TestSubscribeGroupSingleDelivery(t *testing.T) {
	url := runEmbeddedJetStream(t)
	bus, err := NewNATSEventBus(url)
	if err != nil {
		t.Fatalf("creating event bus: %v", err)
	}
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const (
		topic = "agent.run.completed"
		group = "channel-router"
		n     = 20
	)

	// Two subscribers binding to the SAME group => one shared durable consumer.
	ch1, err := bus.SubscribeGroup(ctx, topic, group)
	if err != nil {
		t.Fatalf("subscriber 1: %v", err)
	}
	ch2, err := bus.SubscribeGroup(ctx, topic, group)
	if err != nil {
		t.Fatalf("subscriber 2: %v", err)
	}

	publishSeq(ctx, t, bus, topic, n)

	// Collect until we have n events, then a short grace window to catch any
	// erroneous extra/duplicate delivery (which would push the total past n).
	counts := make(map[int]int)
	total := 0
	deadline := time.After(20 * time.Second)
	grace := time.NewTimer(time.Hour) // armed once n reached
	grace.Stop()
	for {
		select {
		case ev := <-ch1:
			counts[seqOf(t, ev)]++
			total++
		case ev := <-ch2:
			counts[seqOf(t, ev)]++
			total++
		case <-grace.C:
			goto assert
		case <-deadline:
			t.Fatalf("timed out: received %d/%d events", total, n)
		}
		if total == n {
			grace.Reset(2 * time.Second)
		}
	}

assert:
	if total != n {
		t.Fatalf("expected exactly %d total deliveries, got %d (fan-out or loss)", n, total)
	}
	for seq := 0; seq < n; seq++ {
		if counts[seq] != 1 {
			t.Errorf("seq %d delivered %d times, want exactly 1", seq, counts[seq])
		}
	}
}

// TestSubscribeGroupDistinctGroupsEachReceiveAll verifies that distinct queue
// groups remain independent: each logical subscriber (different group) still
// receives its own copy of every event. This guards the fan-out that the system
// relies on — e.g. ChannelRouter, SpawnRouter and the web proxies all consume
// agent.run.completed and each must see every completion.
func TestSubscribeGroupDistinctGroupsEachReceiveAll(t *testing.T) {
	url := runEmbeddedJetStream(t)
	bus, err := NewNATSEventBus(url)
	if err != nil {
		t.Fatalf("creating event bus: %v", err)
	}
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const (
		topic = "agent.run.completed"
		n     = 10
	)

	chA, err := bus.SubscribeGroup(ctx, topic, "group-a")
	if err != nil {
		t.Fatalf("group-a: %v", err)
	}
	chB, err := bus.SubscribeGroup(ctx, topic, "group-b")
	if err != nil {
		t.Fatalf("group-b: %v", err)
	}

	publishSeq(ctx, t, bus, topic, n)

	gotA := collectN(t, chA, n)
	gotB := collectN(t, chB, n)

	for seq := 0; seq < n; seq++ {
		if gotA[seq] != 1 {
			t.Errorf("group-a got seq %d %d times, want 1", seq, gotA[seq])
		}
		if gotB[seq] != 1 {
			t.Errorf("group-b got seq %d %d times, want 1", seq, gotB[seq])
		}
	}
}

func collectN(t *testing.T, ch <-chan *Event, n int) map[int]int {
	t.Helper()
	out := make(map[int]int)
	deadline := time.After(20 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case ev := <-ch:
			out[seqOf(t, ev)]++
		case <-deadline:
			t.Fatalf("timed out after %d/%d events", i, n)
		}
	}
	return out
}

// TestConsumerNameInvariants covers the durable-name builder without a server:
// the same (group, subject) must collapse onto one durable (so replicas
// load-balance), distinct groups must not collide (so they stay independent),
// and the result must contain no characters NATS forbids in durable names.
func TestConsumerNameInvariants(t *testing.T) {
	subj := topicToSubject(TopicAgentRunCompleted)

	a := consumerName("channel-router", subj)
	b := consumerName("channel-router", subj)
	if a != b {
		t.Fatalf("same group+subject must yield identical durable: %q vs %q", a, b)
	}

	if c := consumerName("spawn-router", subj); a == c {
		t.Fatalf("distinct groups must yield distinct durables, both were %q", a)
	}

	if d := consumerName("channel-router", topicToSubject(TopicChannelMessageRecv)); a == d {
		t.Fatalf("distinct subjects must yield distinct durables, both were %q", a)
	}

	if !strings.HasPrefix(a, consumerGroup+"-") {
		t.Errorf("durable %q should carry the %q namespace prefix", a, consumerGroup)
	}

	for _, r := range a {
		switch r {
		case '.', '*', '>', '/', '\\', ' ', '\t', '\n':
			t.Fatalf("durable %q contains NATS-illegal char %q", a, r)
		}
	}
}
