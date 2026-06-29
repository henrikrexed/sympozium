package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// withRecordingTracer installs an always-sampling SDK TracerProvider as the
// global, starts a parent "agentrun.reconcile"-style span, and returns a ctx
// carrying that span plus the span context. This mirrors what the real
// reconcile loop has in ctx when it calls the successor triggers, so any
// handoff span we anchor is a real, sampled, exported span.
func withRecordingTracer(t *testing.T) (context.Context, trace.SpanContext, func()) {
	t.Helper()
	prev := otel.GetTracerProvider()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(tp)
	ctx, span := tp.Tracer("test").Start(context.Background(), "parent.reconcile")
	cleanup := func() {
		span.End()
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	}
	return ctx, span.SpanContext(), cleanup
}

// TestTriggerDelegationSuccessors_StampsTraceparent is the ISI-1484 acceptance
// at the unit level: a delegated child run must carry a non-empty
// otel.dev/traceparent annotation, and that traceparent must share the parent
// run's trace.id (so the child joins the parent trace instead of rooting a fresh
// one). The annotated span id is the exported handoff span, so it resolves.
func TestTriggerDelegationSuccessors_StampsTraceparent(t *testing.T) {
	ctx, parentSC, cleanup := withRecordingTracer(t)
	defer cleanup()

	run, objs := delegationFixtures()
	r := newAgentRunTestReconciler(t, objs...)
	r.DelegationControllerExecutor = true

	if _, err := r.triggerDelegationSuccessors(ctx, logr.Discard(), run); err != nil {
		t.Fatalf("triggerDelegationSuccessors: %v", err)
	}

	children := listChildRuns(t, r)
	if len(children) != 1 {
		t.Fatalf("expected 1 child run, got %d", len(children))
	}
	assertJoinedTrace(t, &children[0], parentSC)
}

// TestTriggerSequentialSuccessors_StampsTraceparent is the same acceptance for
// the sequential-pipeline lane.
func TestTriggerSequentialSuccessors_StampsTraceparent(t *testing.T) {
	ctx, parentSC, cleanup := withRecordingTracer(t)
	defer cleanup()

	run, objs := sequentialTraceFixtures()
	r := newAgentRunTestReconciler(t, objs...)

	if err := r.triggerSequentialSuccessors(ctx, logr.Discard(), run); err != nil {
		t.Fatalf("triggerSequentialSuccessors: %v", err)
	}

	var children []sympoziumv1alpha1.AgentRun
	var runs sympoziumv1alpha1.AgentRunList
	if err := r.List(ctx, &runs); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	for i := range runs.Items {
		if runs.Items[i].Labels["sympozium.ai/sequential-from"] != "" {
			children = append(children, runs.Items[i])
		}
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 sequential child run, got %d", len(children))
	}
	assertJoinedTrace(t, &children[0], parentSC)
}

// TestAnchorChildTrace_AnchorsToParentPersistedTrace is the ISI-1488 regression.
// The live reconcile ctx is in one trace (the per-reconcile throwaway root), while
// the parent run carries a PERSISTED otel.dev/traceparent annotation pointing at a
// DIFFERENT trace — the trace its runner pod actually adopted. anchorChildTrace
// must anchor the handoff (and therefore the child) to the parent's persisted
// trace, not to the live ctx. Pre-fix, the child joined the live-ctx throwaway
// trace and the cross-run chain never collapsed (live evidence: traces==runs).
func TestAnchorChildTrace_AnchorsToParentPersistedTrace(t *testing.T) {
	liveCtx, liveSC, cleanup := withRecordingTracer(t)
	defer cleanup()

	// The parent run's persisted traceparent — a valid W3C value in a trace that
	// is deliberately NOT the live ctx's trace.
	const parentTrace = "11111111111111111111111111111111"
	parentTP := "00-" + parentTrace + "-2222222222222222-01"
	parent := &sympoziumv1alpha1.AgentRun{}
	parent.Annotations = map[string]string{"otel.dev/traceparent": parentTP}
	child := &sympoziumv1alpha1.AgentRun{}

	r := newAgentRunTestReconciler(t)
	r.anchorChildTrace(liveCtx, parent, child, "sympozium.test.handoff")

	tp := child.Annotations["otel.dev/traceparent"]
	if tp == "" {
		t.Fatal("child carries no otel.dev/traceparent annotation")
	}
	childSC := trace.SpanContextFromContext(extractTraceparent(context.Background(), tp))
	if !childSC.IsValid() {
		t.Fatalf("traceparent %q did not parse to a valid span context", tp)
	}
	if childSC.TraceID().String() != parentTrace {
		t.Fatalf("child trace.id = %s, want parent persisted trace %s", childSC.TraceID(), parentTrace)
	}
	if childSC.TraceID() == liveSC.TraceID() {
		t.Fatalf("child joined the live-ctx throwaway trace %s; ISI-1488 regression", liveSC.TraceID())
	}
}

// assertJoinedTrace asserts the child run carries a traceparent that resolves to
// the parent's trace.id (cross-run linkage), not an empty or fresh-root id.
func assertJoinedTrace(t *testing.T, child *sympoziumv1alpha1.AgentRun, parentSC trace.SpanContext) {
	t.Helper()
	tp := child.Annotations["otel.dev/traceparent"]
	if tp == "" {
		t.Fatalf("child run %s carries no otel.dev/traceparent annotation", child.Name)
	}
	childCtx := extractTraceparent(context.Background(), tp)
	childSC := trace.SpanContextFromContext(childCtx)
	if !childSC.IsValid() {
		t.Fatalf("traceparent %q did not parse to a valid span context", tp)
	}
	if childSC.TraceID() != parentSC.TraceID() {
		t.Fatalf("child trace.id = %s, want parent trace.id %s (child rooted a fresh trace)",
			childSC.TraceID(), parentSC.TraceID())
	}
	// The handoff span id must differ from the parent reconcile span id — it is
	// a distinct, exported anchor span, not the parent span re-used verbatim.
	if childSC.SpanID() == parentSC.SpanID() {
		t.Fatalf("child parent-span id equals the reconcile span id; expected a dedicated handoff span")
	}
}

// sequentialTraceFixtures mirrors delegationFixtures but wires a "sequential"
// relationship so triggerSequentialSuccessors spawns a child.
func sequentialTraceFixtures() (*sympoziumv1alpha1.AgentRun, []client.Object) {
	run, objs := delegationFixtures()
	for _, o := range objs {
		if ens, ok := o.(*sympoziumv1alpha1.Ensemble); ok {
			ens.Spec.Relationships[0].Type = "sequential"
		}
	}
	return run, objs
}
