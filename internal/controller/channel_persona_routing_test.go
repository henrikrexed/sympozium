package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	channelpkg "github.com/sympozium-ai/sympozium/internal/channel"
)

// TestAddressedPersona exercises the addressing-marker parser that decides
// whether an inbound message's first token names a persona (ISI-1443).
func TestAddressedPersona(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"mention", "@cto ship the release", "cto"},
		{"mention upper", "@CTO ship it", "cto"},
		{"salutation", "cto: please review", "cto"},
		{"mention trailing comma", "@cto, please review", "cto"},
		{"leading whitespace", "   @analyst dig in", "analyst"},
		{"plain first word not addressed", "cto should look at this", ""},
		{"no marker", "deploy the thing", ""},
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"bare at sign", "@ hello", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := addressedPersona(tc.text); got != tc.want {
				t.Errorf("addressedPersona(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// ensembleAgent builds a persona Agent CR labeled for an Ensemble.
func ensembleAgent(name, ensemble, persona string) *sympoziumv1alpha1.Agent {
	return &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/ensemble":     ensemble,
				"sympozium.ai/agent-config": persona,
			},
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{Model: "gpt-4o"},
			},
		},
	}
}

// TestResolvePersona_AddressedSibling verifies an inbound addressed to a
// non-receiving persona routes to that sibling's Agent with the resolved
// AgentID (the core ISI-1443 behaviour).
func TestResolvePersona_AddressedSibling(t *testing.T) {
	scheme := newReplyTestScheme(t)
	receiver := ensembleAgent("team-pm", "team", "pm")
	cto := ensembleAgent("team-cto", "team", "cto")

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(receiver, cto).Build()
	cr := &ChannelRouter{Client: cl, Log: logr.Discard()}

	msg := channelpkg.InboundMessage{InstanceName: "team-pm", Channel: "slack", Text: "@cto ship the release"}
	target, agentID := cr.resolvePersona(context.Background(), receiver, msg)

	if target.Name != "team-cto" {
		t.Errorf("target = %q, want team-cto", target.Name)
	}
	if agentID != "cto" {
		t.Errorf("agentID = %q, want cto", agentID)
	}
}

// TestResolvePersona_SalutationAddressing verifies the "persona: ..." form also
// routes to the named sibling.
func TestResolvePersona_SalutationAddressing(t *testing.T) {
	scheme := newReplyTestScheme(t)
	receiver := ensembleAgent("team-pm", "team", "pm")
	analyst := ensembleAgent("team-analyst", "team", "analyst")

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(receiver, analyst).Build()
	cr := &ChannelRouter{Client: cl, Log: logr.Discard()}

	msg := channelpkg.InboundMessage{InstanceName: "team-pm", Channel: "slack", Text: "analyst: dig into the logs"}
	target, agentID := cr.resolvePersona(context.Background(), receiver, msg)

	if target.Name != "team-analyst" || agentID != "analyst" {
		t.Errorf("got (%q, %q), want (team-analyst, analyst)", target.Name, agentID)
	}
}

// TestResolvePersona_NoAddressFallsBackToReceiver verifies an unaddressed
// message keeps the receiving persona and stamps its agent-config name (not the
// literal "primary").
func TestResolvePersona_NoAddressFallsBackToReceiver(t *testing.T) {
	scheme := newReplyTestScheme(t)
	receiver := ensembleAgent("team-pm", "team", "pm")
	cto := ensembleAgent("team-cto", "team", "cto")

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(receiver, cto).Build()
	cr := &ChannelRouter{Client: cl, Log: logr.Discard()}

	msg := channelpkg.InboundMessage{InstanceName: "team-pm", Channel: "slack", Text: "status update please"}
	target, agentID := cr.resolvePersona(context.Background(), receiver, msg)

	if target.Name != "team-pm" {
		t.Errorf("target = %q, want team-pm", target.Name)
	}
	if agentID != "pm" {
		t.Errorf("agentID = %q, want pm (the receiving persona), got %q", agentID, agentID)
	}
}

// TestResolvePersona_UnknownPersonaFallsBack verifies addressing a name that is
// not a persona in the Ensemble keeps the receiver.
func TestResolvePersona_UnknownPersonaFallsBack(t *testing.T) {
	scheme := newReplyTestScheme(t)
	receiver := ensembleAgent("team-pm", "team", "pm")

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(receiver).Build()
	cr := &ChannelRouter{Client: cl, Log: logr.Discard()}

	msg := channelpkg.InboundMessage{InstanceName: "team-pm", Channel: "slack", Text: "@nobody hello"}
	target, agentID := cr.resolvePersona(context.Background(), receiver, msg)

	if target.Name != "team-pm" || agentID != "pm" {
		t.Errorf("got (%q, %q), want (team-pm, pm)", target.Name, agentID)
	}
}

// TestResolvePersona_StandaloneAgentKeepsPrimary verifies a non-Ensemble Agent
// (no ensemble label) preserves the historical "primary" AgentID and never
// attempts sibling routing.
func TestResolvePersona_StandaloneAgentKeepsPrimary(t *testing.T) {
	scheme := newReplyTestScheme(t)
	standalone := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "solo", Namespace: "default"},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(standalone).Build()
	cr := &ChannelRouter{Client: cl, Log: logr.Discard()}

	msg := channelpkg.InboundMessage{InstanceName: "solo", Channel: "slack", Text: "@cto do something"}
	target, agentID := cr.resolvePersona(context.Background(), standalone, msg)

	if target.Name != "solo" || agentID != "primary" {
		t.Errorf("got (%q, %q), want (solo, primary)", target.Name, agentID)
	}
}
