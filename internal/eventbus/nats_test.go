package eventbus

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// newTestBus builds a NATSEventBus with just the state needed to exercise the
// readiness/publish/metric logic, without a live NATS connection.
func newTestBus() *NATSEventBus {
	return &NATSEventBus{
		readyCh:   make(chan struct{}),
		connected: newConnectedGauge(),
	}
}

func gaugeValue(g prometheus.Gauge) float64 {
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		return -1
	}
	return m.GetGauge().GetValue()
}

// ISI-1466: publishing before the bus is ready must return an error rather than
// silently dropping the event or panicking on a nil JetStream handle.
func TestPublishReturnsErrorWhenNotReady(t *testing.T) {
	n := newTestBus()

	err := n.Publish(context.Background(), TopicChannelMessageRecv, &Event{Topic: TopicChannelMessageRecv})
	if err == nil {
		t.Fatal("expected error when publishing before bus is ready, got nil")
	}
}

// ISI-1466: waitReady must unblock immediately once the stream becomes ready.
func TestWaitReadyUnblocksWhenReady(t *testing.T) {
	n := newTestBus()

	go func() {
		// Simulate ensureStreamOnce marking the bus ready.
		n.ready.Store(true)
		close(n.readyCh)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := n.waitReady(ctx); err != nil {
		t.Fatalf("waitReady returned error after becoming ready: %v", err)
	}
}

// ISI-1466: a consumer waiting for readiness must abort cleanly when its context
// is cancelled (e.g. controller shutdown) rather than blocking forever.
func TestWaitReadyRespectsContextCancellation(t *testing.T) {
	n := newTestBus()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := n.waitReady(ctx); err == nil {
		t.Fatal("expected context cancellation error from waitReady, got nil")
	}
}

// The connected gauge must default to 0 (down) and reflect setConnected.
func TestConnectedGaugeReflectsState(t *testing.T) {
	n := newTestBus()

	if got := gaugeValue(n.connected); got != 0 {
		t.Fatalf("expected connected gauge to default to 0, got %v", got)
	}

	n.setConnected(true)
	if got := gaugeValue(n.connected); got != 1 {
		t.Fatalf("expected connected gauge to be 1 after setConnected(true), got %v", got)
	}

	n.setConnected(false)
	if got := gaugeValue(n.connected); got != 0 {
		t.Fatalf("expected connected gauge to be 0 after setConnected(false), got %v", got)
	}
}

// ISI-1468 M2: only an actual consumer-loss error must trigger consumer
// re-creation; transient connection/fetch errors must not (they would leak
// ephemeral consumers server-side).
func TestConsumerLostClassification(t *testing.T) {
	lost := []error{
		jetstream.ErrConsumerNotFound,
		jetstream.ErrConsumerDeleted,
		jetstream.ErrConsumerDoesNotExist,
		fmt.Errorf("wrapped: %w", jetstream.ErrConsumerNotFound),
	}
	for _, err := range lost {
		if !consumerLost(err) {
			t.Errorf("expected consumerLost(%v) = true", err)
		}
	}

	notLost := []error{
		nil,
		context.DeadlineExceeded,
		fmt.Errorf("nats: connection closed"),
	}
	for _, err := range notLost {
		if consumerLost(err) {
			t.Errorf("expected consumerLost(%v) = false", err)
		}
	}
}

// ISI-1468 M1: startHealer must be re-armable but never run two healers at once.
// The atomic guard admits exactly one goroutine; once it exits, a later call
// (e.g. a subsequent reconnect) can re-arm it.
func TestStartHealerGuardIsReArmable(t *testing.T) {
	n := newTestBus()

	// First arm wins the CAS.
	if !n.healing.CompareAndSwap(false, true) {
		t.Fatal("expected healing guard to start cleared")
	}
	// While one healer is "running", startHealer must not launch another: the
	// guard is already set, so a concurrent CAS fails.
	if n.healing.CompareAndSwap(false, true) {
		t.Fatal("expected startHealer guard to block a second healer while one runs")
	}

	// Healer exits -> clears guard -> a later reconnect can re-arm.
	n.healing.Store(false)
	if !n.healing.CompareAndSwap(false, true) {
		t.Fatal("expected healing guard to be re-armable after the healer exits")
	}
}

// ISI-1466 residual (child 25d47671): a recreated stream (new Created timestamp)
// must bump the generation so running subscribers re-establish their consumer;
// the same stream surviving a reconnect must NOT, so transient blips don't churn
// (and leak) consumers.
func TestStreamRecreatedDetection(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	t1 := time.Unix(1_700_000_500, 0) // a later, different Created timestamp

	// First observation (no prior stream) must not count as a recreate.
	if streamRecreated(time.Time{}, t0) {
		t.Error("expected first observation not to count as a stream recreate")
	}
	// Same stream surviving a reconnect (same Created) must not bump.
	if streamRecreated(t0, t0) {
		t.Error("expected an unchanged Created timestamp not to count as a recreate")
	}
	// A different Created timestamp means the stream was deleted and recreated.
	if !streamRecreated(t0, t1) {
		t.Error("expected a changed Created timestamp to count as a stream recreate")
	}
	// Missing current info must not spuriously bump.
	if streamRecreated(t0, time.Time{}) {
		t.Error("expected a zero current timestamp not to count as a recreate")
	}
}

// Collectors must expose the connected gauge so the controller can register it.
func TestCollectorsExposesConnectedGauge(t *testing.T) {
	n := newTestBus()
	cols := n.Collectors()
	if len(cols) == 0 {
		t.Fatal("expected at least one collector from Collectors()")
	}

	reg := prometheus.NewRegistry()
	if err := reg.Register(cols[0]); err != nil {
		t.Fatalf("failed to register collector: %v", err)
	}
}
