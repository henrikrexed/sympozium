package main

import (
	"context"
	"net"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestCheckOTelEndpoint_Unreachable(t *testing.T) {
	// Port 1 is almost certainly not listening.
	if checkOTelEndpoint("127.0.0.1:1") {
		t.Error("expected unreachable endpoint to return false")
	}
}

func TestCheckOTelEndpoint_Reachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	if !checkOTelEndpoint(ln.Addr().String()) {
		t.Error("expected reachable endpoint to return true")
	}
}

func TestCheckOTelEndpoint_StripScheme(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	if !checkOTelEndpoint("http://" + ln.Addr().String()) {
		t.Error("expected reachable endpoint with http:// prefix to return true")
	}
}

// TestEndRunSpanWithReason proves the SIGTERM/timeout robustness contract
// (ISI-1483): a root run-span that is ended via the signal/deadline path is
// closed with an error status describing the termination and is actually
// exported (flushed) — i.e. a timed-out run still yields a complete, rooted
// trace rather than a rootless fragment.
func TestEndRunSpanWithReason(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer tp.Shutdown(context.Background())

	_, span := tp.Tracer("test").Start(context.Background(), "sympozium.agent.run")

	const reason = "run terminated by signal: terminated"
	endRunSpanWithReason(span, reason)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected exactly 1 exported span, got %d", len(spans))
	}
	got := spans[0]
	if got.Name != "sympozium.agent.run" {
		t.Errorf("expected root span name sympozium.agent.run, got %q", got.Name)
	}
	if got.Status.Code != codes.Error {
		t.Errorf("expected span status Error, got %v", got.Status.Code)
	}
	if got.Status.Description != reason {
		t.Errorf("expected status description %q, got %q", reason, got.Status.Description)
	}
	if !got.EndTime.After(got.StartTime) {
		t.Errorf("expected span to be ended (EndTime after StartTime), start=%v end=%v", got.StartTime, got.EndTime)
	}
}

// TestEndRunSpanWithReason_NilSafe ensures the helper tolerates a nil span
// (e.g. observability disabled / noop path) without panicking.
func TestEndRunSpanWithReason_NilSafe(t *testing.T) {
	endRunSpanWithReason(nil, "should not panic")
}
