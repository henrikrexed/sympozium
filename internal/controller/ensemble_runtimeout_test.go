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

// TestReconcileAgentConfig_PropagatesRunTimeoutToExistingAgent guards ISI-1481:
// editing an existing ensemble's per-persona runTimeout must reach already-stamped
// Agents. RunTimeout was only applied at Agent create time (buildAgent), so an edit
// never propagated and runs stayed on the controller's flat 10m default — silently
// timing out scheduled/sequential/delegation runs on slow local models.
func TestReconcileAgentConfig_PropagatesRunTimeoutToExistingAgent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium to scheme: %v", err)
	}

	// An Agent stamped before the ensemble carried a runTimeout (the bmad case).
	existing := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bmad-architect",
			Namespace: "default",
			Labels:    map[string]string{},
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{RunTimeout: ""},
			},
			Memory: &sympoziumv1alpha1.MemorySpec{Enabled: true, MaxSizeKB: 256},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	r := &EnsembleReconciler{Client: cl, Scheme: scheme}

	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "bmad", Namespace: "default"},
	}
	// The ensemble now sets runTimeout on this persona (no schedule, to keep the
	// reconcile focused on the Agent update branch).
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Name:         "architect",
		SystemPrompt: "You are the architect.",
		RunTimeout:   "30m",
	}

	if _, err := r.reconcileAgentConfig(context.Background(), logr.Discard(), pack, persona, 0, ""); err != nil {
		t.Fatalf("reconcileAgentConfig: %v", err)
	}

	updated := &sympoziumv1alpha1.Agent{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "bmad-architect", Namespace: "default"}, updated); err != nil {
		t.Fatalf("get updated agent: %v", err)
	}
	if got := updated.Spec.Agents.Default.RunTimeout; got != "30m" {
		t.Errorf("RunTimeout = %q, want %q", got, "30m")
	}
	if d := updated.Spec.Agents.Default.ParseRunTimeout(); d == nil || d.Duration.Minutes() != 30 {
		t.Errorf("ParseRunTimeout() = %v, want 30m", d)
	}
}
