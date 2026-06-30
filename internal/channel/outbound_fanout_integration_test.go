package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	natsd "github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"

	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

// runJetStreamServer starts an embedded NATS server with JetStream enabled on
// the given port (-1 for a random port) backed by storeDir, blocking until it
// accepts connections. Mirrors the helper in internal/eventbus so a caller can
// restart on the SAME port (same storeDir keeps the stream across a bounce).
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

// personaPod models one of the per-persona *-channel-slack pods: it subscribes
// to the shared channel.message.send fan-out subject and applies the exact
// guard the live slack adapter applies (channels/slack/main.go handleOutbound):
// drop non-slack events, then OutboundIsForInstance. Every pod that passes the
// guard records a "post" — the test asserts exactly one pod posts per publish.
type personaPod struct {
	instance string
	bus      *eventbus.NATSEventBus
	posts    int64 // atomic count of messages this pod would have chat.postMessage'd

	mu     sync.Mutex
	byText map[string]int // per-message-text post count, for duplicate detection
}

func (p *personaPod) run(t *testing.T, ctx context.Context, wg *sync.WaitGroup) {
	t.Helper()
	bc := &BaseChannel{ChannelType: "slack", InstanceName: p.instance, EventBus: p.bus}
	ch, err := bc.SubscribeOutbound(ctx)
	if err != nil {
		t.Errorf("%s SubscribeOutbound: %v", p.instance, err)
		wg.Done()
		return
	}
	wg.Done() // subscription established
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-ch:
			if event == nil {
				return
			}
			var msg OutboundMessage
			if err := json.Unmarshal(event.Data, &msg); err != nil {
				continue
			}
			if msg.Channel != "slack" {
				continue
			}
			// The fix under test: only the pod whose instance matches the
			// controller-stamped owner posts. Without it, every pod that
			// received the fan-out event would post → duplicate Slack replies.
			if !OutboundIsForInstance(event, p.instance) {
				continue
			}
			atomic.AddInt64(&p.posts, 1)
			p.mu.Lock()
			if p.byText == nil {
				p.byText = map[string]int{}
			}
			p.byText[msg.Text]++
			p.mu.Unlock()
		}
	}
}

// publishSend mirrors the controller's outbound publish (channel_router.go):
// it stamps instanceName + channel on the event metadata and carries an
// OutboundMessage payload addressed to slack.
func publishSend(t *testing.T, ctx context.Context, bus *eventbus.NATSEventBus, instance, text string) {
	t.Helper()
	out := OutboundMessage{Channel: "slack", ChatID: "D123", Text: text}
	ev, err := eventbus.NewEvent(eventbus.TopicChannelMessageSend, map[string]string{
		"instanceName": instance,
		"channel":      "slack",
	}, out)
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if err := bus.Publish(ctx, eventbus.TopicChannelMessageSend, ev); err != nil {
		t.Fatalf("Publish(%s): %v", instance, err)
	}
}

func totalPosts(pods []*personaPod) int64 {
	var n int64
	for _, p := range pods {
		n += atomic.LoadInt64(&p.posts)
	}
	return n
}

// TestOutboundFanoutPostsExactlyOnce reproduces the ISI-1493 / ISI-1436
// duplicate-Slack-reply bug end-to-end against an embedded JetStream server and
// proves the per-instance filter fix.
//
// channel.message.send is consumed via eventbus.Subscribe, a fan-out subject:
// every one of the N per-persona *-channel-slack pods receives every send
// event. The live symptom was that a single controller publish produced N
// identical Slack posts because handleOutbound filtered only on channel, never
// on instance. The fix (OutboundIsForInstance) makes only the pod whose
// instance matches the controller-stamped owner post.
//
// Before the fix this test fails with totalPosts == len(personas) (N duplicate
// posts per publish). After the fix it is exactly 1.
//
// The reconnect sub-case bounces NATS on the same port with the SAME store (the
// stream survives) and re-subscribes fresh consumers — the at-most-once /
// DeliverNew semantics plus the filter must still yield exactly one post for a
// new publish, with no cross-persona misrouting or loss.
func TestOutboundFanoutPostsExactlyOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-JetStream integration test in -short mode")
	}

	personas := []string{
		"bmad-ensemble-cto",
		"bmad-ensemble-product-manager",
		"bmad-ensemble-architect",
	}

	store := t.TempDir()
	srv, port := runJetStreamServer(t, -1, store)
	defer func() { srv.Shutdown(); srv.WaitForShutdown() }()

	url := fmt.Sprintf("nats://127.0.0.1:%d", port)
	log := funcr.New(func(prefix, args string) { t.Logf("[bus] %s %s", prefix, args) }, funcr.Options{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// One bus per persona pod — in production each *-channel-slack pod has its
	// own NATS connection. They share the subject, so the broker fans out.
	pods := make([]*personaPod, 0, len(personas))
	var wg sync.WaitGroup
	for _, name := range personas {
		bus, err := eventbus.NewNATSEventBus(url, eventbus.WithLogger(log))
		if err != nil {
			t.Fatalf("NewNATSEventBus(%s): %v", name, err)
		}
		defer bus.Close()
		p := &personaPod{instance: name, bus: bus}
		pods = append(pods, p)
		wg.Add(1)
		go p.run(t, ctx, &wg)
	}
	wg.Wait() // all subscriptions established before the first publish (DeliverNew)

	// A publisher bus standing in for the controller.
	pub, err := eventbus.NewNATSEventBus(url, eventbus.WithLogger(log))
	if err != nil {
		t.Fatalf("NewNATSEventBus(publisher): %v", err)
	}
	defer pub.Close()

	t.Run("single publish lands on exactly one pod", func(t *testing.T) {
		owner := "bmad-ensemble-product-manager"
		publishSend(t, ctx, pub, owner, "reply-1")

		// Give the fan-out time to reach all N pods; the guard then drops it on
		// every pod except the owner.
		waitForPosts(t, pods, 1, 5*time.Second)
		time.Sleep(500 * time.Millisecond) // let any erroneous duplicates surface

		if got := totalPosts(pods); got != 1 {
			t.Fatalf("total posts = %d, want exactly 1 (duplicate Slack replies)", got)
		}
		for _, p := range pods {
			want := int64(0)
			if p.instance == owner {
				want = 1
			}
			if got := atomic.LoadInt64(&p.posts); got != want {
				t.Fatalf("pod %s posts = %d, want %d", p.instance, got, want)
			}
		}
	})

	t.Run("distinct owners each land on exactly their own pod", func(t *testing.T) {
		for _, p := range pods {
			atomic.StoreInt64(&p.posts, 0)
		}
		for _, name := range personas {
			publishSend(t, ctx, pub, name, "reply-for-"+name)
		}
		waitForPosts(t, pods, int64(len(personas)), 5*time.Second)
		time.Sleep(500 * time.Millisecond)

		if got := totalPosts(pods); got != int64(len(personas)) {
			t.Fatalf("total posts = %d, want %d (one per owner, no loss/dup/misroute)", got, len(personas))
		}
		for _, p := range pods {
			if got := atomic.LoadInt64(&p.posts); got != 1 {
				t.Fatalf("pod %s posts = %d, want 1", p.instance, got)
			}
		}
	})

	t.Run("exactly-once survives a NATS reconnect", func(t *testing.T) {
		// Bounce the server on the same port with the SAME store so the stream
		// persists; the pods' connections reconnect (MaxReconnects(-1)).
		srv.Shutdown()
		srv.WaitForShutdown()
		srv, _ = runJetStreamServer(t, port, store)

		// Wait for every pod's bus (and the publisher) to report healthy again.
		waitFor(t, "all buses healthy after reconnect", 30*time.Second, func() bool {
			if !pub.Healthy() {
				return false
			}
			for _, p := range pods {
				if !p.bus.Healthy() {
					return false
				}
			}
			return true
		})

		for _, p := range pods {
			atomic.StoreInt64(&p.posts, 0)
			p.mu.Lock()
			p.byText = map[string]int{}
			p.mu.Unlock()
		}

		// Consumer re-establishment after a bounce is asynchronous (ISI-1470)
		// and DeliverNew skips anything published before a consumer is back, so
		// a single publish may be dropped. Retry with a UNIQUE marker per
		// attempt and assert the anti-duplication invariant per marker: each
		// delivered marker must be posted by exactly ONE pod (the owner) exactly
		// ONCE — no cross-persona fan-out and no same-pod redelivery. This is
		// robust to how many attempts it takes for delivery to land.
		owner := "bmad-ensemble-cto"
		deadline := time.Now().Add(30 * time.Second)
		for totalPosts(pods) == 0 && time.Now().Before(deadline) {
			publishSend(t, ctx, pub, owner, fmt.Sprintf("reply-after-reconnect-%d", time.Now().UnixNano()))
			time.Sleep(1 * time.Second)
		}
		time.Sleep(1 * time.Second) // let any duplicate redelivery surface

		if totalPosts(pods) == 0 {
			t.Fatal("post-reconnect: no post ever landed — subscriber failed to re-establish")
		}

		// Aggregate every posted marker across all pods.
		posters := map[string][]string{} // text -> instances that posted it
		counts := map[string]int{}        // text -> total posts across all pods
		for _, p := range pods {
			p.mu.Lock()
			for text, n := range p.byText {
				for i := 0; i < n; i++ {
					posters[text] = append(posters[text], p.instance)
				}
				counts[text] += n
			}
			p.mu.Unlock()
		}
		for text, n := range counts {
			if n != 1 {
				t.Fatalf("post-reconnect: marker %q posted %d times (want exactly 1) by %v — duplicate Slack reply", text, n, posters[text])
			}
			if posters[text][0] != owner {
				t.Fatalf("post-reconnect: marker %q posted by %q, want owner %q — misroute", text, posters[text][0], owner)
			}
		}
	})
}

func waitForPosts(t *testing.T, pods []*personaPod, want int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if totalPosts(pods) >= want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %d posts (got %d)", timeout, want, totalPosts(pods))
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
