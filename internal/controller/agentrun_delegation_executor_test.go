package controller

import (
	"context"
	"strconv"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// delegationFixtures returns a succeeded source run plus the Agent instances and
// Ensemble wiring needed for the controller-side delegation executor to spawn a
// child for persona "architect" -> "reviewer" over a delegation edge.
func delegationFixtures() (*sympoziumv1alpha1.AgentRun, []client.Object) {
	sourceInst := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "team-architect",
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/agent-config": "architect",
				"sympozium.ai/ensemble":     "team",
			},
		},
	}
	targetInst := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "team-reviewer",
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/agent-config": "reviewer",
				"sympozium.ai/ensemble":     "team",
			},
		},
	}
	ensemble := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "team", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Relationships: []sympoziumv1alpha1.AgentConfigRelationship{
				{Source: "architect", Target: "reviewer", Type: "delegation"},
			},
		},
	}
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "src-run", Namespace: "default"},
		Spec:       sympoziumv1alpha1.AgentRunSpec{AgentRef: "team-architect", Task: "design it"},
		Status: sympoziumv1alpha1.AgentRunStatus{
			Phase:  sympoziumv1alpha1.AgentRunPhaseSucceeded,
			Result: "design complete",
		},
	}
	return run, []client.Object{sourceInst, targetInst, ensemble, run}
}

// listChildRuns returns AgentRuns spawned for the reviewer target.
func listChildRuns(t *testing.T, r *AgentRunReconciler) []sympoziumv1alpha1.AgentRun {
	t.Helper()
	var runs sympoziumv1alpha1.AgentRunList
	if err := r.List(context.Background(), &runs, client.InNamespace("default")); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	var children []sympoziumv1alpha1.AgentRun
	for _, ar := range runs.Items {
		if ar.Labels["sympozium.ai/delegation-from"] != "" {
			children = append(children, ar)
		}
	}
	return children
}

// TestTriggerDelegationSuccessors_DefaultOff is the core default-off guarantee:
// with the flag unset, the executor is a no-op and spawns no child runs.
func TestTriggerDelegationSuccessors_DefaultOff(t *testing.T) {
	run, objs := delegationFixtures()
	r := newAgentRunTestReconciler(t, objs...)
	// DelegationControllerExecutor left at its zero value (false).

	if _, err := r.triggerDelegationSuccessors(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("triggerDelegationSuccessors: %v", err)
	}
	if got := listChildRuns(t, r); len(got) != 0 {
		t.Fatalf("expected no child runs when flag off, got %d", len(got))
	}
	// Idempotency marker must not be set when nothing was triggered.
	if run.Labels["sympozium.ai/delegation-triggered"] == "true" {
		t.Fatal("delegation-triggered marker set despite flag off")
	}
}

// TestTriggerDelegationSuccessors_EnabledSpawnsChild verifies the executor
// spawns the target persona's run when enabled, and marks the parent idempotent.
func TestTriggerDelegationSuccessors_EnabledSpawnsChild(t *testing.T) {
	run, objs := delegationFixtures()
	r := newAgentRunTestReconciler(t, objs...)
	r.DelegationControllerExecutor = true

	if _, err := r.triggerDelegationSuccessors(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("triggerDelegationSuccessors: %v", err)
	}
	children := listChildRuns(t, r)
	if len(children) != 1 {
		t.Fatalf("expected 1 child run, got %d", len(children))
	}
	child := children[0]
	if child.Spec.AgentRef != "team-reviewer" {
		t.Fatalf("child AgentRef = %q, want team-reviewer", child.Spec.AgentRef)
	}
	if child.Spec.AgentID != "delegation-from-architect" {
		t.Fatalf("child AgentID = %q, want delegation-from-architect", child.Spec.AgentID)
	}
	if run.Labels["sympozium.ai/delegation-triggered"] != "true" {
		t.Fatal("parent not marked delegation-triggered after spawn")
	}
}

// TestTriggerDelegationSuccessors_Idempotent verifies a second call (e.g. on
// re-reconcile) does not double-spawn once the marker is set.
func TestTriggerDelegationSuccessors_Idempotent(t *testing.T) {
	run, objs := delegationFixtures()
	r := newAgentRunTestReconciler(t, objs...)
	r.DelegationControllerExecutor = true

	for i := 0; i < 2; i++ {
		if _, err := r.triggerDelegationSuccessors(context.Background(), logr.Discard(), run); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := listChildRuns(t, r); len(got) != 1 {
		t.Fatalf("expected exactly 1 child after 2 calls, got %d", len(got))
	}
}

// TestTriggerDelegationSuccessors_SkipsAlreadyDelegated verifies the executor
// stays a pure fallback: if the model already delegated to the target at
// runtime (recorded in Status.Delegates), no duplicate child is spawned.
func TestTriggerDelegationSuccessors_SkipsAlreadyDelegated(t *testing.T) {
	run, objs := delegationFixtures()
	run.Status.Delegates = []sympoziumv1alpha1.DelegateStatus{
		{ChildRunName: "tool-spawned-child", TargetPersona: "reviewer"},
	}
	r := newAgentRunTestReconciler(t, objs...)
	r.DelegationControllerExecutor = true

	if _, err := r.triggerDelegationSuccessors(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("triggerDelegationSuccessors: %v", err)
	}
	if got := listChildRuns(t, r); len(got) != 0 {
		t.Fatalf("expected no controller-spawned child when model already delegated, got %d", len(got))
	}
}

// fanoutFixtures returns a source run (cto) whose ensemble has multiple
// delegation edges all sourced from the same persona — the ISI-1458 fan-out
// shape. Each edge carries a distinct free-text routing condition. Targets are
// real Agent instances so any selected edge can spawn.
func fanoutFixtures(result string) (*sympoziumv1alpha1.AgentRun, []client.Object) {
	mk := func(persona string) *sympoziumv1alpha1.Agent {
		return &sympoziumv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "team-" + persona,
				Namespace: "default",
				Labels: map[string]string{
					"sympozium.ai/agent-config": persona,
					"sympozium.ai/ensemble":     "team",
				},
			},
		}
	}
	ensemble := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "team", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Relationships: []sympoziumv1alpha1.AgentConfigRelationship{
				{Source: "cto", Target: "architect", Type: "delegation", Condition: "architecture and solutioning design"},
				{Source: "cto", Target: "pm", Type: "delegation", Condition: "requirements PRD and epics"},
				{Source: "cto", Target: "devops", Type: "delegation", Condition: "deployment infrastructure and kubernetes"},
			},
		},
	}
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cto-run",
			Namespace: "default",
			Labels:    map[string]string{"sympozium.ai/ensemble": "team"},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{AgentRef: "team-cto", Task: "ship the platform"},
		Status: sympoziumv1alpha1.AgentRunStatus{
			Phase:  sympoziumv1alpha1.AgentRunPhaseSucceeded,
			Result: result,
		},
	}
	objs := []client.Object{
		mk("cto"), mk("architect"), mk("pm"), mk("devops"), ensemble, run,
	}
	return run, objs
}

// TestTriggerDelegationSuccessors_ConditionAwareRoutesOne is guardrail 3: with a
// wide fan-out graph, the executor scores edge conditions against the run result
// and fires only the single best match — not all edges (the ISI-1458 gridlock).
func TestTriggerDelegationSuccessors_ConditionAwareRoutesOne(t *testing.T) {
	run, objs := fanoutFixtures("Architecture solutioning complete: the design is ready.")
	r := newAgentRunTestReconciler(t, objs...)
	r.DelegationControllerExecutor = true

	if _, err := r.triggerDelegationSuccessors(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("triggerDelegationSuccessors: %v", err)
	}
	children := listChildRuns(t, r)
	if len(children) != 1 {
		t.Fatalf("expected exactly 1 routed child (no fan-out), got %d", len(children))
	}
	if children[0].Spec.AgentRef != "team-architect" {
		t.Fatalf("routed to %q, want team-architect (best condition match)", children[0].Spec.AgentRef)
	}
}

// TestTriggerDelegationSuccessors_NoMatchNoFanOut verifies that when no edge
// condition matches the run result, the executor fans out to nothing rather than
// running the whole team.
func TestTriggerDelegationSuccessors_NoMatchNoFanOut(t *testing.T) {
	run, objs := fanoutFixtures("totally unrelated narrative with no routing keywords")
	r := newAgentRunTestReconciler(t, objs...)
	r.DelegationControllerExecutor = true

	if _, err := r.triggerDelegationSuccessors(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("triggerDelegationSuccessors: %v", err)
	}
	if got := listChildRuns(t, r); len(got) != 0 {
		t.Fatalf("expected no children when no condition matches, got %d", len(got))
	}
	if run.Labels["sympozium.ai/delegation-triggered"] == "true" {
		t.Fatal("delegation-triggered marker set despite no fan-out")
	}
}

// TestTriggerDelegationSuccessors_InflightCapRequeues is guardrail 1: when the
// ensemble already has maxInflight non-terminal runs, the executor requeues
// instead of spawning and does not mark the parent triggered.
func TestTriggerDelegationSuccessors_InflightCapRequeues(t *testing.T) {
	run, objs := delegationFixtures()
	// Add two extra non-terminal runs to the ensemble so inflight == cap (2).
	for _, name := range []string{"busy-1", "busy-2"} {
		objs = append(objs, &sympoziumv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Labels:    map[string]string{"sympozium.ai/ensemble": "team"},
			},
			Status: sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhaseRunning},
		})
	}
	r := newAgentRunTestReconciler(t, objs...)
	r.DelegationControllerExecutor = true
	r.DelegationMaxInflight = 2

	res, err := r.triggerDelegationSuccessors(context.Background(), logr.Discard(), run)
	if err != nil {
		t.Fatalf("triggerDelegationSuccessors: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected RequeueAfter > 0 when in-flight cap hit, got %v", res.RequeueAfter)
	}
	if got := listChildRuns(t, r); len(got) != 0 {
		t.Fatalf("expected no children when cap hit, got %d", len(got))
	}
	if run.Labels["sympozium.ai/delegation-triggered"] == "true" {
		t.Fatal("parent marked triggered despite being requeued at the cap")
	}
}

// TestTriggerDelegationSuccessors_DepthCapStopsCascade is guardrail 2: a run
// already at the depth cap fires no further delegation successors, and a spawned
// child is stamped with the incremented depth label.
func TestTriggerDelegationSuccessors_DepthCapStopsCascade(t *testing.T) {
	// Child stamping: a depth-0 source spawns a depth-1 child.
	run, objs := delegationFixtures()
	r := newAgentRunTestReconciler(t, objs...)
	r.DelegationControllerExecutor = true
	if _, err := r.triggerDelegationSuccessors(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("triggerDelegationSuccessors: %v", err)
	}
	children := listChildRuns(t, r)
	if len(children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(children))
	}
	if got := children[0].Labels["sympozium.ai/delegation-depth"]; got != "1" {
		t.Fatalf("child delegation-depth = %q, want 1", got)
	}

	// Cap enforcement: a source already at the depth cap (1) spawns nothing.
	run2, objs2 := delegationFixtures()
	run2.Name = "src-run-deep"
	run2.Labels = map[string]string{"sympozium.ai/delegation-depth": "1"}
	r2 := newAgentRunTestReconciler(t, objs2...)
	r2.DelegationControllerExecutor = true
	if _, err := r2.triggerDelegationSuccessors(context.Background(), logr.Discard(), run2); err != nil {
		t.Fatalf("triggerDelegationSuccessors (deep): %v", err)
	}
	if got := listChildRuns(t, r2); len(got) != 0 {
		t.Fatalf("expected no children at/over depth cap, got %d", len(got))
	}
}

func TestSelectDelegationEdges(t *testing.T) {
	edges := func(conds ...string) []sympoziumv1alpha1.AgentConfigRelationship {
		out := make([]sympoziumv1alpha1.AgentConfigRelationship, 0, len(conds))
		for i, c := range conds {
			out = append(out, sympoziumv1alpha1.AgentConfigRelationship{
				Source: "cto", Target: "t" + strconv.Itoa(i), Type: "delegation", Condition: c,
			})
		}
		return out
	}

	// Single candidate always fires regardless of (un)scorable condition.
	if got := selectDelegationEdges(edges(""), "anything", "", 1); len(got) != 1 {
		t.Fatalf("single candidate: got %d, want 1", len(got))
	}
	// Best lexical match wins among several.
	got := selectDelegationEdges(edges("architecture design", "requirements prd"), "the architecture is ready", "", 1)
	if len(got) != 1 || got[0].Target != "t0" {
		t.Fatalf("expected single best match t0, got %+v", got)
	}
	// No match among several -> none.
	if got := selectDelegationEdges(edges("architecture", "requirements"), "no keywords here", "", 1); len(got) != 0 {
		t.Fatalf("expected no selection on zero match, got %d", len(got))
	}
}

func TestDelegationEdgeActive(t *testing.T) {
	// Test free-text condition fallback (empty trigger)
	cases := []struct {
		condition string
		want      bool
	}{
		{"", true},
		{"on explicit request", true},
		{"when source run succeeds", true},
		{"when source fails", false},
		{"On Error", false},
		{"if review unsuccessful", false},
		{"when rejected by reviewer", false},
	}
	for _, c := range cases {
		if got := delegationEdgeActive(c.condition, ""); got != c.want {
			t.Errorf("delegationEdgeActive(%q, \"\") = %v, want %v", c.condition, got, c.want)
		}
	}

	// Test structured trigger field (ISI-1562 finding 5)
	triggerCases := []struct {
		trigger string
		want    bool
	}{
		{"Success", true},
		{"success", true},
		{"Failure", false},
		{"failure", false},
		{"Always", true},
		{"always", true},
		{"", true}, // empty falls back to condition
	}
	for _, c := range triggerCases {
		if got := delegationEdgeActive("some condition", c.trigger); got != c.want {
			t.Errorf("delegationEdgeActive(\"some condition\", %q) = %v, want %v", c.trigger, got, c.want)
		}
	}
}
