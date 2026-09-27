// Package observability wires OpenTelemetry traces and metrics.
//
// Telemetry is off until an OTLP endpoint is configured: without one the
// global providers stay no-op, instruments cost almost nothing and the
// application never tries to reach a collector.
//
// Everything beyond the endpoint and the protocol — headers, TLS, compression,
// timeouts, extra resource attributes — is read by the OTLP exporters
// themselves from the standard `OTEL_EXPORTER_OTLP_*` environment variables.
package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Protocol selects the OTLP transport.
const (
	ProtocolGRPC = "grpc"
	ProtocolHTTP = "http/protobuf"
)

type Config struct {
	// Endpoint is the OTLP collector endpoint; empty disables telemetry.
	Endpoint string
	Protocol string
	// ServiceName and ServiceVersion identify this service to the backend.
	ServiceName    string
	ServiceVersion string
	// SampleRatio is the head sampling ratio for root spans (1 = everything).
	SampleRatio float64
	// MetricInterval is how often metrics are pushed to the collector.
	MetricInterval time.Duration
	// MetricPrefix is prepended to this application's own metric names.
	// Empty leaves the names untouched.
	MetricPrefix string
}

// Telemetry owns the providers; Shutdown flushes them.
type Telemetry struct {
	Enabled  bool
	shutdown []func(context.Context) error
}

// Setup installs the global tracer and meter providers. When no endpoint is
// configured it returns a disabled Telemetry and leaves the no-op globals in
// place, so the caller never has to branch on it.
func Setup(ctx context.Context, cfg Config) (*Telemetry, error) {
	// Propagators are always installed: honouring inbound trace headers costs
	// nothing and keeps behaviour identical whether or not we export.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	// Exporter failures must never take the application down.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Warn("opentelemetry error", "err", err)
	}))

	if cfg.Endpoint == "" {
		slog.Info("telemetry: disabled (no OTLP endpoint configured)")
		return &Telemetry{}, nil
	}

	res, err := resource.New(ctx,
		resource.WithFromEnv(), // OTEL_RESOURCE_ATTRIBUTES, OTEL_SERVICE_NAME
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
	)
	if err != nil {
		// Schema conflicts are reported as an error alongside a usable
		// resource; anything else is fatal.
		if !errors.Is(err, resource.ErrSchemaURLConflict) {
			return nil, fmt.Errorf("observability: building resource: %w", err)
		}
		slog.Warn("telemetry: resource schema conflict, continuing", "err", err)
	}

	t := &Telemetry{Enabled: true}

	spanExporter, err := newSpanExporter(ctx, cfg.Protocol)
	if err != nil {
		return nil, fmt.Errorf("observability: OTLP trace exporter: %w", err)
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(spanExporter),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	otel.SetTracerProvider(tracerProvider)
	t.shutdown = append(t.shutdown, tracerProvider.Shutdown)

	metricExporter, err := newMetricExporter(ctx, cfg.Protocol)
	if err != nil {
		return nil, fmt.Errorf("observability: OTLP metric exporter: %w", err)
	}
	metricOptions := []sdkmetric.Option{
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter,
			sdkmetric.WithInterval(cfg.MetricInterval))),
	}
	if cfg.MetricPrefix != "" {
		metricOptions = append(metricOptions, sdkmetric.WithView(MetricPrefixView(cfg.MetricPrefix)))
	}
	meterProvider := sdkmetric.NewMeterProvider(metricOptions...)
	otel.SetMeterProvider(meterProvider)
	t.shutdown = append(t.shutdown, meterProvider.Shutdown)

	slog.Info("telemetry: OpenTelemetry enabled",
		"endpoint", cfg.Endpoint, "protocol", cfg.Protocol,
		"service", cfg.ServiceName, "sample_ratio", cfg.SampleRatio,
		"metric_prefix", cfg.MetricPrefix)
	return t, nil
}

func newSpanExporter(ctx context.Context, protocol string) (sdktrace.SpanExporter, error) {
	if protocol == ProtocolGRPC {
		return otlptracegrpc.New(ctx)
	}
	return otlptracehttp.New(ctx)
}

func newMetricExporter(ctx context.Context, protocol string) (sdkmetric.Exporter, error) {
	if protocol == ProtocolGRPC {
		return otlpmetricgrpc.New(ctx)
	}
	return otlpmetrichttp.New(ctx)
}

// MetricPrefixView prepends prefix to this application's own metrics.
//
// Metrics that follow the OpenTelemetry semantic conventions — everything the
// instrumentation libraries emit, such as http.server.request.duration — are
// deliberately left alone: their names are the contract backends and dashboards
// rely on, and a service is meant to be told apart by its resource attributes
// (service.name, service.namespace, and whatever OTEL_RESOURCE_ATTRIBUTES adds)
// rather than by renaming standard instruments.
//
// Aggregation is left unset so each instrument keeps the default for its kind,
// including the bucket boundaries advised at creation.
func MetricPrefixView(prefix string) sdkmetric.View {
	return func(i sdkmetric.Instrument) (sdkmetric.Stream, bool) {
		if !strings.HasPrefix(i.Name, MetricNamespace) {
			return sdkmetric.Stream{}, false
		}
		return sdkmetric.Stream{
			Name:        prefix + i.Name,
			Description: i.Description,
			Unit:        i.Unit,
		}, true
	}
}

// Shutdown flushes pending spans and metrics. It is safe to call when
// telemetry is disabled.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for i := len(t.shutdown) - 1; i >= 0; i-- {
		if err := t.shutdown[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
