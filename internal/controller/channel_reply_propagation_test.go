package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// TestPropagateReplyAnnotations exercises the helper that carries Slack
// reply-routing annotations onto child AgentRuns (ISI-1442).
func TestPropagateReplyAnnotations(t *testing.T) {
	src := map[string]string{
		"sympozium.ai/reply-channel":    "C123",
		"sympozium.ai/reply-chat-id":    "D456",
		"sympozium.ai/reply-thread-id":  "T789",
		"sympozium.ai/reply-message-ts": "1700000000.000100",
		// Non-routing keys must NOT be propagated.
		"sympozium.ai/sender-name": "alice",
		"sympozium.ai/dedup-key":   "abc",
		"otel.dev/traceparent":     "00-aaa-bbb-01",
		"sympozium.ai/reply-empty": "", // not a routing key anyway
	}

	t.Run("nil dst allocates and copies routing keys only", func(t *testing.T) {
		got := propagateReplyAnnotations(nil, src)
		want := map[string]string{
			"sympozium.ai/reply-channel":    "C123",
			"sympozium.ai/reply-chat-id":    "D456",
			"sympozium.ai/reply-thread-id":  "T789",
			"sympozium.ai/reply-message-ts": "1700000000.000100",
		}
		if len(got) != len(want) {
			t.Fatalf("got %d keys %v, want %d", len(got), got, len(want))
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("key %q = %q, want %q", k, got[k], v)
			}
		}
		if _, ok := got["sympozium.ai/sender-name"]; ok {
			t.Error("sender-name must not be propagated")
		}
		if _, ok := got["otel.dev/traceparent"]; ok {
			t.Error("traceparent must not be propagated by this helper")
		}
	})

	t.Run("preserves existing dst entries", func(t *testing.T) {
		dst := map[string]string{"otel.dev/traceparent": "00-aaa-bbb-01"}
		got := propagateReplyAnnotations(dst, src)
		if got["otel.dev/traceparent"] != "00-aaa-bbb-01" {
			t.Error("existing dst entry was clobbered")
		}
		if got["sympozium.ai/reply-channel"] != "C123" {
			t.Error("routing key not added")
		}
	})

	t.Run("empty and absent values are skipped", func(t *testing.T) {
		got := propagateReplyAnnotations(nil, map[string]string{
			"sympozium.ai/reply-channel": "C123",
			"sympozium.ai/reply-chat-id": "", // empty -> skip
			// thread-id / message-ts absent -> skip
		})
		if len(got) != 1 || got["sympozium.ai/reply-channel"] != "C123" {
			t.Fatalf("expected only non-empty reply-channel, got %v", got)
		}
	})

	t.Run("no routing keys yields nil", func(t *testing.T) {
		got := propagateReplyAnnotations(nil, map[string]string{"foo": "bar"})
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})
}

func newReplyTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

// TestDeliverStimulus_PropagatesReplyAnnotations verifies that when the Ensemble
// itself carries reply-routing annotations, the spawned stimulus AgentRun
// inherits them so its output can be routed back to the originating channel.
func TestDeliverStimulus_PropagatesReplyAnnotations(t *testing.T) {
	scheme := newReplyTestScheme(t)

	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pack",
			Namespace: "default",
			Annotations: map[string]string{
				"sympozium.ai/reply-channel":   "C123",
				"sympozium.ai/reply-chat-id":   "D456",
				"sympozium.ai/reply-thread-id": "T789",
			},
		},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Stimulus: &sympoziumv1alpha1.StimulusSpec{Name: "kickoff", Prompt: "go"},
			Relationships: []sympoziumv1alpha1.AgentConfigRelationship{
				{Source: "kickoff", Target: "worker", Type: "stimulus"},
			},
		},
	}
	targetAgent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "pack-worker", Namespace: "default"},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pack, targetAgent).
		Build()

	r := &EnsembleReconciler{Client: cl, Log: logr.Discard()}
	if err := r.deliverStimulus(context.Background(), logr.Discard(), pack, "readiness"); err != nil {
		t.Fatalf("deliverStimulus: %v", err)
	}

	run := getSoleAgentRun(t, cl)
	assertReplyAnnotations(t, run.Annotations, map[string]string{
		"sympozium.ai/reply-channel":   "C123",
		"sympozium.ai/reply-chat-id":   "D456",
		"sympozium.ai/reply-thread-id": "T789",
	})
}

// TestDeliverStimulus_NoReplyAnnotationsOnPlainEnsemble verifies that a plain
// readiness-triggered stimulus (Ensemble without reply annotations) produces a
// child with no reply-routing annotations rather than empty/garbage values.
func TestDeliverStimulus_NoReplyAnnotationsOnPlainEnsemble(t *testing.T) {
	scheme := newReplyTestScheme(t)

	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Stimulus: &sympoziumv1alpha1.StimulusSpec{Name: "kickoff", Prompt: "go"},
			Relationships: []sympoziumv1alpha1.AgentConfigRelationship{
				{Source: "kickoff", Target: "worker", Type: "stimulus"},
			},
		},
	}
	targetAgent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "pack-worker", Namespace: "default"},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pack, targetAgent).Build()
	r := &EnsembleReconciler{Client: cl, Log: logr.Discard()}
	if err := r.deliverStimulus(context.Background(), logr.Discard(), pack, "readiness"); err != nil {
		t.Fatalf("deliverStimulus: %v", err)
	}

	run := getSoleAgentRun(t, cl)
	for _, k := range replyRoutingAnnotationKeys {
		if _, ok := run.Annotations[k]; ok {
			t.Errorf("unexpected reply annotation %q on plain stimulus child", k)
		}
	}
}

// TestTriggerSequentialSuccessors_PropagatesReplyAnnotations verifies that a
// pipeline successor inherits the predecessor run's reply-routing annotations so
// a Slack-originated chain keeps replying to the same channel/thread.
func TestTriggerSequentialSuccessors_PropagatesReplyAnnotations(t *testing.T) {
	scheme := newReplyTestScheme(t)

	sourceAgent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pack-analyst",
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/agent-config": "analyst",
				"sympozium.ai/ensemble":     "pack",
			},
		},
	}
	targetAgent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "pack-writer", Namespace: "default"},
	}
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Relationships: []sympoziumv1alpha1.AgentConfigRelationship{
				{Source: "analyst", Target: "writer", Type: "sequential"},
			},
		},
	}
	predecessor := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pack-analyst-run",
			Namespace: "default",
			Annotations: map[string]string{
				"otel.dev/traceparent":          "00-aaa-bbb-01",
				"sympozium.ai/reply-channel":    "C123",
				"sympozium.ai/reply-chat-id":    "D456",
				"sympozium.ai/reply-thread-id":  "T789",
				"sympozium.ai/reply-message-ts": "1700000000.000100",
			},
		},
		Spec:   sympoziumv1alpha1.AgentRunSpec{AgentRef: "pack-analyst", Task: "analyze"},
		Status: sympoziumv1alpha1.AgentRunStatus{Result: "done"},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sourceAgent, targetAgent, pack, predecessor).
		Build()

	r := &AgentRunReconciler{Client: cl, Log: logr.Discard()}
	if err := r.triggerSequentialSuccessors(context.Background(), logr.Discard(), predecessor); err != nil {
		t.Fatalf("triggerSequentialSuccessors: %v", err)
	}

	// Find the successor (name differs from the predecessor).
	var runs sympoziumv1alpha1.AgentRunList
	if err := cl.List(context.Background(), &runs); err != nil {
		t.Fatal(err)
	}
	var succ *sympoziumv1alpha1.AgentRun
	for i := range runs.Items {
		if runs.Items[i].Name != predecessor.Name {
			succ = &runs.Items[i]
			break
		}
	}
	if succ == nil {
		t.Fatal("no successor AgentRun created")
	}
	assertReplyAnnotations(t, succ.Annotations, map[string]string{
		"sympozium.ai/reply-channel":    "C123",
		"sympozium.ai/reply-chat-id":    "D456",
		"sympozium.ai/reply-thread-id":  "T789",
		"sympozium.ai/reply-message-ts": "1700000000.000100",
	})
}

func getSoleAgentRun(t *testing.T, cl client.Client) *sympoziumv1alpha1.AgentRun {
	t.Helper()
	var runs sympoziumv1alpha1.AgentRunList
	if err := cl.List(context.Background(), &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("expected exactly 1 AgentRun, got %d", len(runs.Items))
	}
	return &runs.Items[0]
}

func assertReplyAnnotations(t *testing.T, got, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("annotation %q = %q, want %q", k, got[k], v)
		}
	}
}
