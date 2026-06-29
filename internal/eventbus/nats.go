package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

	// recoveryBackoff is the minimum delay between Subscribe fetch-loop recovery
	// iterations. It is applied unconditionally on any fetch error so a flapping
	// broker (where consumer creation briefly succeeds, then Fetch errors) cannot
	// spin the loop hot (ISI-1468 M2).
	recoveryBackoff = 2 * time.Second
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

	healing atomic.Bool // true while a background streamHealer is running

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
			// Re-ensure the stream after a reconnect; the server may have come
			// back without the stream (ephemeral storage, or the stream was
			// deleted). startHealer retries CreateOrUpdateStream until it exists,
			// so connection-up/stream-gone can't silently disable routing — a
			// milder repeat of the ISI-1466 bug (ISI-1468 M1). The healer exits
			// immediately if the stream is already healthy.
			n.startHealer()
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
		n.startHealer()
	}

	return n, nil
}

// startHealer launches the background stream healer unless one is already
// running. It is safe to call concurrently and on every reconnect: the healer
// exits as soon as the stream exists, so a healthy reconnect costs a single
// CreateOrUpdateStream and the atomic guard prevents duplicate goroutines.
func (n *NATSEventBus) startHealer() {
	if n.healing.CompareAndSwap(false, true) {
		go n.streamHealer()
	}
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
// succeeds, so a broker that was unreachable at boot — or one that returns
// without the stream after a reconnect (ISI-1468 M1) — eventually re-enables
// routing without a process restart. It clears the healing guard on exit so a
// future reconnect can re-arm it.
func (n *NATSEventBus) streamHealer() {
	defer n.healing.Store(false)
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

// Healthy reports whether the bus is currently connected and its JetStream
// stream is ready. It gates on IsConnected (not just !IsClosed) so a live
// disconnect — where the connection is reconnecting but not yet Closed — reads
// as unhealthy, matching the connected gauge (ISI-1468 L1).
func (n *NATSEventBus) Healthy() bool {
	return n.ready.Load() && n.conn.IsConnected()
}

// consumerLost reports whether a fetch error indicates the consumer no longer
// exists server-side (reaped across a reconnect), as opposed to a transient
// connection error that leaves the consumer handle valid.
func consumerLost(err error) bool {
	return errors.Is(err, jetstream.ErrConsumerNotFound) ||
		errors.Is(err, jetstream.ErrConsumerDeleted) ||
		errors.Is(err, jetstream.ErrConsumerDoesNotExist)
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

// Subscribe returns a channel that receives events for the given topic using an
// ephemeral, non-load-balanced consumer: every Subscribe call gets its own
// consumer and therefore receives every matching message (fan-out). Use this for
// distinct logical consumers that each need their own copy of an event, or for
// dynamic per-run subjects. For load-balanced single-delivery across N replicas
// use SubscribeGroup (ISI-1430).
//
// It blocks until the stream is ready (or ctx is cancelled). The internal fetch
// loop tolerates transient broker outages and re-creates its consumer if it is
// lost across a reconnect, so subscribers recover without a process restart.
//
// Note: because it blocks until NATS is reachable, callers should run Subscribe
// inside a ctx-cancellable goroutine/Runnable (as all current call sites do); a
// synchronous caller could otherwise stall boot during a broker outage
// (ISI-1468 L2).
//
// Delivery is at-most-once across an outage: the recovered consumer uses
// DeliverNew, so messages published during the disconnect→recovery window are
// not redelivered (ISI-1468 M3).
func (n *NATSEventBus) Subscribe(ctx context.Context, topic string) (<-chan *Event, error) {
	if err := n.waitReady(ctx); err != nil {
		return nil, fmt.Errorf("waiting for event bus readiness for %s: %w", topic, err)
	}

	subject := topicToSubject(topic)

	consumer, err := n.createConsumer(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("creating consumer for %s: %w", subject, err)
	}

	recreate := func(c context.Context) (jetstream.Consumer, error) {
		return n.createConsumer(c, subject)
	}
	return n.drain(ctx, consumer, subject, recreate), nil
}

// SubscribeGroup returns a channel that receives events for the given topic via a
// durable queue-group consumer shared by all subscribers using the same group.
//
// Multiple instances of one logical subscriber (e.g. ChannelRouter replicas) that
// call SubscribeGroup with the same group bind to the same durable JetStream
// consumer and load-balance: each message is delivered to exactly one instance,
// not fanned out to all of them. Distinct groups bind to distinct durables and so
// remain independent — each group still receives its own copy of every event.
//
// This is the defense-in-depth chokepoint for ISI-1430: even if a controller were
// run with replicas>1 without leader election, a shared queue group collapses
// duplicate inbound deliveries instead of every replica processing every event.
// The leader-election guard remains belt-and-suspenders on top of this.
func (n *NATSEventBus) SubscribeGroup(ctx context.Context, topic, group string) (<-chan *Event, error) {
	subject := topicToSubject(topic)
	durable := consumerName(group, subject)

	// recreate binds (or rebinds) the durable queue-group consumer. It is used
	// both for the initial bind and by drain's self-heal path so a lost durable is
	// re-established as a durable (not downgraded to an ephemeral) consumer
	// (ISI-1430 + ISI-1466).
	recreate := func(c context.Context) (jetstream.Consumer, error) {
		stream := n.getStream()
		if stream == nil {
			return nil, fmt.Errorf("stream not ready")
		}
		return stream.CreateOrUpdateConsumer(c, jetstream.ConsumerConfig{
			Durable:       durable,
			FilterSubject: subject,
			AckPolicy:     jetstream.AckExplicitPolicy,
			DeliverPolicy: jetstream.DeliverNewPolicy,
		})
	}

	consumer, err := recreate(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating durable consumer %s for %s: %w", durable, subject, err)
	}

	return n.drain(ctx, consumer, subject, recreate), nil
}

// drain pumps messages from a JetStream consumer into a freshly created channel,
// decoding each NATS message into an *Event and acking on successful hand-off.
// Shared by Subscribe (ephemeral) and SubscribeGroup (durable queue group); the
// only difference between the two is the consumer's identity, not how it drains.
//
// recreate rebuilds the caller's consumer flavor when it is lost across a
// reconnect (ephemeral for Subscribe, the durable queue-group binding for
// SubscribeGroup), so the self-heal path (ISI-1466) re-establishes the right kind
// of consumer regardless of which subscriber owns the loop. subject is used only
// for log context.
func (n *NATSEventBus) drain(ctx context.Context, consumer jetstream.Consumer, subject string, recreate func(context.Context) (jetstream.Consumer, error)) <-chan *Event {
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
				// Wait for readiness — this also covers a stream re-heal across a
				// reconnect (the healer re-creates the stream; see startHealer).
				if werr := n.waitReady(ctx); werr != nil {
					return
				}
				// Only re-create the consumer when it was actually lost (reaped
				// across a reconnect). Re-creating on *every* fetch error would
				// mint a fresh ephemeral consumer each iteration, leaking the
				// prior one server-side until InactiveThreshold reaps it
				// (ISI-1468 M2). Other fetch errors (transient connection blips)
				// leave the connection-bound consumer handle valid, so we just
				// back off and retry it.
				if consumerLost(err) {
					if newConsumer, cerr := recreate(ctx); cerr == nil {
						consumer = newConsumer
					} else {
						// The stream itself may be gone (not just the consumer);
						// re-arm the healer so it is re-created, then retry.
						n.startHealer()
						n.log.Error(cerr, "failed to re-create lost consumer — retrying", "subject", subject)
					}
				}
				// Unconditional backoff so a flapping broker can't spin this loop
				// hot (ISI-1468 M2 backoff hole).
				select {
				case <-ctx.Done():
					return
				case <-time.After(recoveryBackoff):
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

	return ch
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

// consumerName builds the durable JetStream consumer name for a queue group on a
// given subject. All durables share the consumerGroup namespace prefix; the group
// distinguishes one logical subscriber from another and the subject scopes the
// durable to a single filter (a JetStream consumer has exactly one FilterSubject).
// Two subscribers collapse onto the same durable — and thus load-balance — only
// when both their group and subject match.
func consumerName(group, subject string) string {
	return sanitizeDurable(consumerGroup + "-" + group + "-" + subject)
}

// sanitizeDurable replaces characters NATS forbids in durable consumer names
// ('.', '*', '>', '/', '\\', and whitespace) with '-'. Subjects are dotted, so
// they must be sanitized before use as part of a durable name.
func sanitizeDurable(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '.', '*', '>', '/', '\\', ' ', '\t', '\n':
			return '-'
		default:
			return r
		}
	}, s)
}
