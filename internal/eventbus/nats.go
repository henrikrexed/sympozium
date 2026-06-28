package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	streamName    = "sympozium"
	consumerGroup = "sympozium-workers"

	// bootStreamAttempts bounds the synchronous stream-creation attempts made
	// inside NewNATSEventBus. If NATS is not reachable within this budget we do
	// NOT give up — we return a degraded bus that self-heals in the background.
	bootStreamAttempts = 5

	// streamHealMaxWait caps the backoff between background stream-creation
	// attempts once the boot budget is exhausted.
	streamHealMaxWait = 15 * time.Second
)

// NATSEventBus implements EventBus using NATS JetStream.
//
// Resilience contract (ISI-1466):
//   - The underlying NATS connection uses infinite reconnect, so a transient
//     broker outage never permanently disconnects the bus.
//   - JetStream stream creation is retried in the background until it succeeds,
//     so a broker that is unreachable at boot does not permanently disable the
//     bus (and, by extension, channel routing). The constructor returns a
//     working handle that becomes ready once NATS is reachable.
//   - Subscribe blocks until the stream is ready (or ctx is cancelled) and
//     re-creates its consumer if it is lost across a reconnect, so consumers
//     recover automatically without a process restart.
type NATSEventBus struct {
	url  string
	conn *nats.Conn
	js   jetstream.JetStream
	log  logr.Logger

	mu     sync.RWMutex
	stream jetstream.Stream

	ready   atomic.Bool
	readyCh chan struct{} // closed once the stream is first ready

	connected prometheus.Gauge
}

// Option configures a NATSEventBus.
type Option func(*NATSEventBus)

// WithLogger sets the logger used to surface connection/stream state changes.
// Without it, connection outages are logged through a discard logger and only
// observable via the connected metric.
func WithLogger(log logr.Logger) Option {
	return func(n *NATSEventBus) { n.log = log }
}

// NewNATSEventBus creates a new NATS JetStream event bus.
//
// It never fails for a transient broker outage: nats.Connect uses
// RetryOnFailedConnect with infinite reconnects, and JetStream stream creation
// is retried in the background until it succeeds. The returned bus is safe to
// wire into consumers immediately — Subscribe will wait for readiness. An error
// is only returned for unrecoverable configuration problems (e.g. a malformed
// URL).
func NewNATSEventBus(url string, opts ...Option) (*NATSEventBus, error) {
	n := &NATSEventBus{
		url:       url,
		log:       logr.Discard(),
		readyCh:   make(chan struct{}),
		connected: newConnectedGauge(),
	}
	for _, opt := range opts {
		opt(n)
	}

	nc, err := nats.Connect(url,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1), // never give up — survive broker restarts/blips
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, connErr error) {
			n.setConnected(false)
			n.log.Error(connErr, "NATS connection lost — channel routing degraded until reconnect")
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			n.log.Info("NATS reconnected — re-establishing JetStream stream")
			// Re-ensure the stream after a reconnect; the server may have
			// restarted. ensureStream is idempotent (CreateOrUpdate).
			if _, serr := n.ensureStreamOnce(); serr != nil {
				n.log.Error(serr, "failed to re-ensure JetStream stream after reconnect — will keep retrying")
			}
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			n.setConnected(false)
			n.log.Info("NATS connection closed")
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to NATS: %w", err)
	}
	n.conn = nc

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("creating JetStream context: %w", err)
	}
	n.js = js

	// Try to create the stream synchronously so the happy path is unchanged.
	// If NATS is not ready yet, do NOT disable routing — self-heal instead.
	var lastErr error
	for attempt := 0; attempt < bootStreamAttempts; attempt++ {
		if _, lastErr = n.ensureStreamOnce(); lastErr == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		n.log.Error(lastErr, "JetStream stream not ready at boot — channel routing will self-heal once NATS is reachable")
		go n.streamHealer()
	}

	return n, nil
}

// ensureStreamOnce attempts a single CreateOrUpdateStream and, on success,
// records the stream handle and marks the bus ready. It is idempotent.
func (n *NATSEventBus) ensureStreamOnce() (jetstream.Stream, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := n.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      streamName,
		Subjects:  []string{"sympozium.>"},
		Retention: jetstream.LimitsPolicy,
		MaxAge:    24 * time.Hour,
		Storage:   jetstream.FileStorage,
		Replicas:  1,
	})
	if err != nil {
		return nil, err
	}

	n.mu.Lock()
	n.stream = stream
	n.mu.Unlock()
	n.setConnected(true)

	if n.ready.CompareAndSwap(false, true) {
		close(n.readyCh)
		n.log.Info("JetStream stream ready — channel routing enabled")
	}
	return stream, nil
}

// streamHealer keeps retrying stream creation (with capped backoff) until it
// succeeds, so a broker that was unreachable at boot eventually enables routing
// without a process restart.
func (n *NATSEventBus) streamHealer() {
	wait := 2 * time.Second
	for {
		if n.conn.IsClosed() {
			return
		}
		if _, err := n.ensureStreamOnce(); err == nil {
			return
		}
		time.Sleep(wait)
		if wait < streamHealMaxWait {
			wait *= 2
			if wait > streamHealMaxWait {
				wait = streamHealMaxWait
			}
		}
	}
}

func (n *NATSEventBus) getStream() jetstream.Stream {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.stream
}

func (n *NATSEventBus) setConnected(up bool) {
	if up {
		n.connected.Set(1)
	} else {
		n.connected.Set(0)
	}
}

// waitReady blocks until the stream is ready or ctx is cancelled.
func (n *NATSEventBus) waitReady(ctx context.Context) error {
	if n.ready.Load() {
		return nil
	}
	select {
	case <-n.readyCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Healthy reports whether the bus is connected and its JetStream stream is ready.
func (n *NATSEventBus) Healthy() bool {
	return n.ready.Load() && !n.conn.IsClosed()
}

// Collectors returns Prometheus collectors that expose the bus health so the
// outage is visible (instead of silent). Register them with the controller's
// metrics registry.
func (n *NATSEventBus) Collectors() []prometheus.Collector {
	return []prometheus.Collector{n.connected}
}

func newConnectedGauge() prometheus.Gauge {
	return prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "sympozium",
		Subsystem: "eventbus",
		Name:      "connected",
		Help:      "1 if the NATS JetStream event bus is connected and its stream is ready, 0 otherwise.",
	})
}

// Publish sends an event to the NATS JetStream stream.
// Trace context from ctx is automatically injected into NATS message headers.
func (n *NATSEventBus) Publish(ctx context.Context, topic string, event *Event) error {
	if !n.ready.Load() {
		return fmt.Errorf("event bus not ready (NATS unreachable) — dropping publish to %s", topic)
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshalling event: %w", err)
	}

	subject := topicToSubject(topic)
	msg := &nats.Msg{
		Subject: subject,
		Data:    data,
		Header:  nats.Header{},
	}
	InjectTraceContext(ctx, msg.Header)

	_, err = n.js.PublishMsg(ctx, msg)
	if err != nil {
		return fmt.Errorf("publishing to %s: %w", subject, err)
	}

	return nil
}

// Subscribe returns a channel that receives events for the given topic.
//
// It blocks until the stream is ready (or ctx is cancelled). The internal fetch
// loop tolerates transient broker outages and re-creates its consumer if it is
// lost across a reconnect, so subscribers recover without a process restart.
func (n *NATSEventBus) Subscribe(ctx context.Context, topic string) (<-chan *Event, error) {
	if err := n.waitReady(ctx); err != nil {
		return nil, fmt.Errorf("waiting for event bus readiness for %s: %w", topic, err)
	}

	subject := topicToSubject(topic)

	consumer, err := n.createConsumer(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("creating consumer for %s: %w", subject, err)
	}

	ch := make(chan *Event, 64)

	go func() {
		defer close(ch)
		for {
			if ctx.Err() != nil {
				return
			}

			msgs, err := consumer.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
				}
				// The consumer may have been lost across a reconnect
				// (ephemeral consumers are reaped by the server). Wait for
				// readiness and re-create it rather than spinning on errors.
				if err := n.waitReady(ctx); err != nil {
					return
				}
				if newConsumer, cerr := n.createConsumer(ctx, subject); cerr == nil {
					consumer = newConsumer
				} else {
					n.log.Error(cerr, "failed to re-create consumer after fetch error — retrying", "subject", subject)
					select {
					case <-ctx.Done():
						return
					case <-time.After(2 * time.Second):
					}
				}
				continue
			}

			for msg := range msgs.Messages() {
				var event Event
				if err := json.Unmarshal(msg.Data(), &event); err != nil {
					msg.Nak()
					continue
				}

				// Extract trace context from NATS message headers so consumers
				// can continue the distributed trace started by the publisher.
				event.Ctx = ExtractTraceContext(ctx, msg.Headers())

				select {
				case ch <- &event:
					msg.Ack()
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch, nil
}

// createConsumer creates (or updates) the ephemeral consumer for a subject on
// the current stream handle.
func (n *NATSEventBus) createConsumer(ctx context.Context, subject string) (jetstream.Consumer, error) {
	stream := n.getStream()
	if stream == nil {
		return nil, fmt.Errorf("stream not ready")
	}
	return stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		FilterSubject: subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
}

// Close shuts down the NATS connection.
func (n *NATSEventBus) Close() error {
	n.conn.Close()
	n.setConnected(false)
	return nil
}

// topicToSubject converts a dotted topic (e.g. "agent.run.completed")
// to a NATS subject under the sympozium namespace (e.g. "sympozium.agent.run.completed").
func topicToSubject(topic string) string {
	return "sympozium." + topic
}
