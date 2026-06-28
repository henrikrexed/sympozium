package eventbus

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	natsd "github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
)

// runJetStreamServer starts an embedded NATS server with JetStream enabled on
// the given port (0/-1 for a random port) backed by storeDir. It blocks until
// the server is accepting connections and returns the server plus the port it
// actually bound, so a caller can restart on the SAME port with a fresh store.
func runJetStreamServer(t *testing.T, port int, storeDir string) (*natsd.Server, int) {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Host = "127.0.0.1"
	opts.Port = port
	opts.JetStream = true
	opts.StoreDir = storeDir
	opts.NoLog = true
	opts.NoSigs = true

	s := natsserver.RunServer(&opts)
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS server not ready for connections")
	}
	return s, s.Addr().(*net.TCPAddr).Port
}

// activeConsumers returns the number of consumers the bus's current stream
// reports, or -1 if the stream handle/info is unavailable. This is the exact
// signal the ISI-1470 drill used: a re-created stream starts at 0 consumers,
// and routing only resumes once the running subscriber re-establishes its
// consumer (count → 1).
func activeConsumers(ctx context.Context, bus *NATSEventBus) int {
	st := bus.getStream()
	if st == nil {
		return -1
	}
	ictx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	info, err := st.Info(ictx)
	if err != nil {
		return -1
	}
	return info.State.Consumers
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for: %s", timeout, what)
}

func receiveWithin(t *testing.T, ch <-chan *Event, timeout time.Duration) *Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(timeout):
		t.Fatalf("timed out after %s waiting for an event", timeout)
		return nil
	}
}

// TestSubscriberReEstablishesConsumerAfterFullStreamRecreate reproduces the
// ISI-1470 / M1 ephemeral-NATS scenario end-to-end against an embedded
// JetStream server and proves the residual fix (commit 730b003):
//
//   - A running subscriber routes a baseline message.
//   - NATS is restarted on the same port with an EMPTY store, so it comes back
//     WITHOUT the sympozium stream (the ephemeral-storage / deleted-stream case
//     M1 targets). The healer re-creates the stream; the new Created timestamp
//     bumps streamGen.
//   - The fetch error against the now-stale consumer handle surfaces as a
//     no-responders/timeout, NOT ErrConsumerNotFound — the exact condition that
//     made the old consumerLost-only recovery spin forever. The fix's
//     generation check re-establishes the consumer anyway.
//
// Before 730b003 this test would hang at the "consumer re-established" wait:
// the stream re-created and the connected gauge returned to 1, but the
// subscriber never re-created its consumer (Active Consumers stayed 0) and the
// probe sat UNDELIVERED until a process restart.
func TestSubscriberReEstablishesConsumerAfterFullStreamRecreate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-JetStream integration test in -short mode")
	}

	storeA := t.TempDir()
	srv, port := runJetStreamServer(t, -1, storeA)
	defer func() { srv.Shutdown(); srv.WaitForShutdown() }()

	url := fmt.Sprintf("nats://127.0.0.1:%d", port)
	log := funcr.New(func(prefix, args string) { t.Logf("[bus] %s %s", prefix, args) }, funcr.Options{})
	bus, err := NewNATSEventBus(url, WithLogger(log))
	if err != nil {
		t.Fatalf("NewNATSEventBus: %v", err)
	}
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const topic = "channel.message.received"
	ch, err := bus.Subscribe(ctx, topic)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	gen0 := bus.streamGen.Load()

	// Baseline: the subscriber routes a message on the original stream.
	mustPublish(t, ctx, bus, topic, "baseline")
	if ev := receiveWithin(t, ch, 5*time.Second); ev.Metadata["probe"] != "baseline" {
		t.Fatalf("baseline: got probe=%q, want %q", ev.Metadata["probe"], "baseline")
	}

	// --- M1 trigger: NATS returns with empty storage (no sympozium stream) ---
	srv.Shutdown()
	srv.WaitForShutdown()
	storeB := t.TempDir() // fresh, empty store → stream is gone on restart
	srv, _ = runJetStreamServer(t, port, storeB)

	// The healer re-creates the stream on reconnect; its new Created timestamp
	// advances the generation. This is the "gauge flips back to 1" milestone —
	// necessary but, per ISI-1470, NOT sufficient on its own.
	waitFor(t, "stream re-created (streamGen advanced) and bus healthy", 30*time.Second, func() bool {
		return bus.Healthy() && bus.streamGen.Load() != gen0
	})

	// The crux of ISI-1470: the RUNNING subscriber must re-establish its
	// consumer on the freshly re-created stream without a process restart.
	// A re-created stream starts at 0 consumers; the fix drives it back to 1.
	waitFor(t, "running subscriber re-established its consumer (Active Consumers → 1)", 30*time.Second, func() bool {
		return activeConsumers(ctx, bus) >= 1
	})

	// Functional proof: a probe published after the re-create is actually
	// routed to the subscriber (the production drill's probe sat UNDELIVERED).
	// Published only after the consumer exists, since DeliverNew would skip a
	// message published before the consumer is re-established.
	mustPublish(t, ctx, bus, topic, "after-recreate")
	if ev := receiveWithin(t, ch, 15*time.Second); ev.Metadata["probe"] != "after-recreate" {
		t.Fatalf("post-recreate: got probe=%q, want %q", ev.Metadata["probe"], "after-recreate")
	}
}

// mustPublish publishes a minimal event tagged with probe=label to the topic.
func mustPublish(t *testing.T, ctx context.Context, bus *NATSEventBus, topic, label string) {
	t.Helper()
	ev, err := NewEvent(topic, map[string]string{"probe": label}, map[string]string{"probe": label})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if err := bus.Publish(ctx, topic, ev); err != nil {
		t.Fatalf("Publish(%s): %v", label, err)
	}
}
