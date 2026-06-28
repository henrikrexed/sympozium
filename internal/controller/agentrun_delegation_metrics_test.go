package controller

import (
	"context"
	"sync"
	"testing"

	"github.com/go-logr/logr"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// delegationMetricReader installs an SDK MeterProvider with a manual reader as
// the global provider exactly once for the whole test binary. The package-level
// sympozium.* instruments were created from the global meter at import time;
// OTel's global delegation rebinds them to this provider the first (and only)
// time SetMeterProvider is called, so emissions become collectable.
var (
	delegationMetricReader *sdkmetric.ManualReader
	delegationMetricOnce   sync.Once
)

func installDelegationMetricReader() *sdkmetric.ManualReader {
	delegationMetricOnce.Do(func() {
		delegationMetricReader = sdkmetric.NewManualReader()
		otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(delegationMetricReader)))
	})
	return delegationMetricReader
}

// sumEdgeDecision returns the cumulative count recorded on
// sympozium.delegation.edges for the given decision attribute across all
// ensembles/sources. Counters are cumulative across the binary, so callers diff
// a baseline against a post-run reading to isolate their own contribution.
func sumEdgeDecision(t *testing.T, reader *sdkmetric.ManualReader, decision string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "sympozium.delegation.edges" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("sympozium.delegation.edges is %T, want Sum[int64]", m.Data)
			}
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value("decision"); ok && v.AsString() == decision {
					total += dp.Value
				}
			}
		}
	}
	return total
}

// TestTriggerDelegationSuccessors_EmitsEdgeDecisionMetrics is the ISI-1462
// Phase-3 instrumentation contract: the delegation executor (previously emitting
// zero telemetry) records sympozium.delegation.edges with the decision attribute
// the obs dashboard keys on. With the cto fan-out fixture matching architecture,
// exactly one edge fires (architect) and the other two are dropped on condition —
// proving the dashboard can read "fired<=K, condition-aware narrowed the rest".
func TestTriggerDelegationSuccessors_EmitsEdgeDecisionMetrics(t *testing.T) {
	reader := installDelegationMetricReader()

	firedBefore := sumEdgeDecision(t, reader, "fired")
	skipBefore := sumEdgeDecision(t, reader, "skipped_condition")

	run, objs := fanoutFixtures("Architecture solutioning complete: the design is ready.")
	r := newAgentRunTestReconciler(t, objs...)
	r.DelegationControllerExecutor = true

	if _, err := r.triggerDelegationSuccessors(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("triggerDelegationSuccessors: %v", err)
	}

	if got := sumEdgeDecision(t, reader, "fired") - firedBefore; got != 1 {
		t.Fatalf("delegation.edges{decision=fired} delta = %d, want 1", got)
	}
	// Three cto edges; one matched and fired, the other two are condition-skipped
	// (lost the K=1 routing) — the signal that proves no 9-wide fan-out.
	if got := sumEdgeDecision(t, reader, "skipped_condition") - skipBefore; got != 2 {
		t.Fatalf("delegation.edges{decision=skipped_condition} delta = %d, want 2", got)
	}
}
