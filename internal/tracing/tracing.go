// Package tracing owns the OpenTelemetry / OTLP wiring for weft-network.
//
// The daemon emits a span per NetworkControlPlane RPC via otelgrpc's
// StatsHandler ; the SDK wiring here turns those spans into batched
// OTLP/gRPC pushes against an external collector (Tempo, Jaeger with
// the OTLP receiver, or any vendor OTLP endpoint).
//
// Tracing is best-effort : an empty endpoint installs a no-op tracer
// provider and returns a no-op shutdown, so the daemon's hot path never
// blocks on collector availability. Misconfiguration at boot is logged
// and the daemon keeps serving — operators don't lose the control plane
// because the trace collector is down.
package tracing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	noop "go.opentelemetry.io/otel/trace/noop"
)

// Options configures the OTLP exporter + tracer provider.
//
// Empty OTLPEndpoint disables tracing entirely : Init installs a no-op
// tracer provider, returns a no-op shutdown, and the boot path is
// unchanged from a build without otel wiring at all.
type Options struct {
	// OTLPEndpoint is the OTLP/gRPC collector address ("host:port").
	// Empty disables tracing.
	OTLPEndpoint string
	// Insecure skips TLS on the OTLP push connection. Fine inside the
	// WireGuard mesh ; production cross-DC should wrap the collector in
	// TLS and set this to false.
	Insecure bool
	// ServiceName populates the resource's service.name attribute.
	ServiceName string
	// Version populates the resource's service.version attribute.
	Version string
}

// ShutdownFunc flushes pending spans + tears down the exporter. Always
// safe to call ; the no-op flavour returns nil immediately.
type ShutdownFunc func(context.Context) error

// noopShutdown is returned when tracing is disabled (empty endpoint).
func noopShutdown(context.Context) error { return nil }

// Init builds the OTLP exporter + SDK tracer provider and installs it
// as the global provider. The returned shutdown function flushes the
// BatchSpanProcessor and closes the exporter ; defer it on a deadline
// from the SIGTERM handler so traces aren't lost on graceful stop.
//
// Empty OTLPEndpoint installs otel's no-op provider and returns a
// no-op shutdown. Callers can safely re-invoke Init in tests : the
// no-op path has no global side effect that needs cleanup.
func Init(ctx context.Context, o Options) (ShutdownFunc, error) {
	if o.OTLPEndpoint == "" {
		// Tracing disabled. Install the noop provider explicitly so
		// any caller that already grabbed otel.Tracer(...) at package
		// init time gets a deterministic no-op instead of the SDK
		// default (which still allocates spans).
		otel.SetTracerProvider(noop.NewTracerProvider())
		return noopShutdown, nil
	}

	dialOpts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(o.OTLPEndpoint),
	}
	if o.Insecure {
		dialOpts = append(dialOpts, otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptrace.New(ctx, otlptracegrpc.NewClient(dialOpts...))
	if err != nil {
		return nil, fmt.Errorf("otlp exporter : %w", err)
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(o.ServiceName),
		semconv.ServiceVersion(o.Version),
	))
	if err != nil {
		// Resource merge failure is non-fatal — fall back to the
		// attribute-only resource so we still emit traces with the
		// service.name label that the collector groups on.
		res = resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(o.ServiceName),
			semconv.ServiceVersion(o.Version),
		)
	}

	bsp := sdktrace.NewBatchSpanProcessor(exporter)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(bsp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	return func(shutCtx context.Context) error {
		// Flush + close in order : tracer provider drives the BSP to
		// drain pending spans, then we shut the exporter. Errors are
		// joined so partial failures don't get swallowed.
		var errs []error
		if err := tp.Shutdown(shutCtx); err != nil {
			errs = append(errs, fmt.Errorf("tracer provider shutdown : %w", err))
		}
		if err := exporter.Shutdown(shutCtx); err != nil {
			errs = append(errs, fmt.Errorf("exporter shutdown : %w", err))
		}
		return errors.Join(errs...)
	}, nil
}

// ShutdownTimeout is a sensible default deadline for the shutdown
// function — long enough for the BSP batch interval (5s default) +
// one round-trip to the collector, short enough that a stuck
// collector doesn't pin the SIGTERM path.
const ShutdownTimeout = 10 * time.Second
