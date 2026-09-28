package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/observability"
)

// reader collects the metrics produced by the package-level instruments. The
// global meter provider can only be replaced once, so it is installed here for
// the whole test binary.
var reader = sdkmetric.NewManualReader()

func TestMain(m *testing.M) {
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	os.Exit(m.Run())
}

func collect(t *testing.T) map[string]metricdata.Aggregation {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	out := map[string]metricdata.Aggregation{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			out[m.Name] = m.Data
		}
	}
	return out
}

// TestInstrumentsReachTheProvider also proves that instruments created at
// package initialisation — before any provider exists — are re-pointed at the
// real provider once it is installed. The whole package relies on that.
func TestInstrumentsReachTheProvider(t *testing.T) {
	ctx := context.Background()
	observability.ExportsCreated.Add(ctx, 1, metric.WithAttributes(observability.AttrFormat.String("pdf")))
	observability.ExportsFinished.Add(ctx, 1, metric.WithAttributes(
		observability.AttrFormat.String("pdf"), observability.AttrOutcome.String("succeeded")))
	observability.ExportDuration.Record(ctx, 12.5, metric.WithAttributes(observability.AttrFormat.String("pdf")))
	observability.ExportPages.Record(ctx, 9, metric.WithAttributes(observability.AttrFormat.String("pdf")))

	metrics := collect(t)
	for _, name := range []string{
		"c2d.exports.created", "c2d.exports.finished", "c2d.export.duration", "c2d.export.pages",
	} {
		if _, ok := metrics[name]; !ok {
			t.Errorf("metric %q was not exported", name)
		}
	}

	created, ok := metrics["c2d.exports.created"].(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("c2d.exports.created is %T, want a counter", metrics["c2d.exports.created"])
	}
	if len(created.DataPoints) != 1 || created.DataPoints[0].Value != 1 {
		t.Fatalf("unexpected data points: %+v", created.DataPoints)
	}
	if format, found := created.DataPoints[0].Attributes.Value(observability.AttrFormat); !found || format.AsString() != "pdf" {
		t.Errorf("the format attribute is missing from the counter: %+v", created.DataPoints[0].Attributes)
	}

	duration, ok := metrics["c2d.export.duration"].(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("c2d.export.duration is %T, want a histogram", metrics["c2d.export.duration"])
	}
	if len(duration.DataPoints) != 1 || duration.DataPoints[0].Sum != 12.5 {
		t.Fatalf("unexpected histogram: %+v", duration.DataPoints)
	}
}

func TestRegisterQueueDepth(t *testing.T) {
	stats := func(context.Context) (domain.QueueStats, error) {
		return domain.QueueStats{Queued: 3, Running: 2}, nil
	}
	if err := observability.RegisterQueueDepth(stats); err != nil {
		t.Fatal(err)
	}

	gauge, ok := collect(t)["c2d.queue.exports"].(metricdata.Gauge[int64])
	if !ok {
		t.Fatalf("c2d.queue.exports is missing or not a gauge")
	}
	byState := map[string]int64{}
	for _, dp := range gauge.DataPoints {
		state, _ := dp.Attributes.Value(observability.AttrState)
		byState[state.AsString()] = dp.Value
	}
	if byState["queued"] != 3 || byState["running"] != 2 {
		t.Errorf("queue depth = %+v, want queued=3 running=2", byState)
	}
}

func TestRegisterQueueDepthSurvivesDatabaseErrors(t *testing.T) {
	// A failing read must not break the whole metric collection.
	if err := observability.RegisterQueueDepth(func(context.Context) (domain.QueueStats, error) {
		return domain.QueueStats{}, errors.New("database unreachable")
	}); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("a failing queue read must not fail collection: %v", err)
	}
}

func TestSetupDisabledWithoutEndpoint(t *testing.T) {
	telemetry, err := observability.Setup(context.Background(), observability.Config{
		ServiceName: "test", SampleRatio: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if telemetry.Enabled {
		t.Error("telemetry must stay disabled without an OTLP endpoint")
	}
	if err := telemetry.Shutdown(context.Background()); err != nil {
		t.Errorf("shutting down disabled telemetry: %v", err)
	}
	// Propagation is installed either way, so inbound trace headers are honoured.
	if otel.GetTextMapPropagator() == nil {
		t.Error("a propagator must be installed even when telemetry is off")
	}
}

func TestLogHandlerAddsTraceIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(observability.LogHandler{Handler: slog.NewJSONHandler(&buf, nil)})

	provider := sdktrace.NewTracerProvider()
	ctx, span := provider.Tracer("test").Start(context.Background(), "unit")
	logger.InfoContext(ctx, "inside a span")
	span.End()

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("log line is not JSON: %v (%s)", err, buf.String())
	}
	if record["trace_id"] != span.SpanContext().TraceID().String() {
		t.Errorf("trace_id = %v, want %s", record["trace_id"], span.SpanContext().TraceID())
	}
	if record["span_id"] != span.SpanContext().SpanID().String() {
		t.Errorf("span_id = %v, want %s", record["span_id"], span.SpanContext().SpanID())
	}

	buf.Reset()
	logger.Info("outside any span")
	outside := map[string]any{} // a fresh map: Unmarshal merges into an existing one
	if err := json.Unmarshal(buf.Bytes(), &outside); err != nil {
		t.Fatal(err)
	}
	if _, present := outside["trace_id"]; present {
		t.Error("a log line outside a span must not carry a trace id")
	}
}

// TestMetricPrefixView covers the rename and, just as importantly, that the
// custom histogram boundaries advised at instrument creation survive it.
func TestMetricPrefixView(t *testing.T) {
	r := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(r),
		sdkmetric.WithView(observability.MetricPrefixView("acme.")),
	)
	meter := provider.Meter("test")

	counter, err := meter.Int64Counter("c2d.exports.created", metric.WithUnit("{export}"),
		metric.WithDescription("Exports accepted into the queue."))
	if err != nil {
		t.Fatal(err)
	}
	histogram, err := meter.Float64Histogram("c2d.export.duration", metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(1, 5, 15, 30))
	if err != nil {
		t.Fatal(err)
	}
	// A semantic-convention metric from an instrumentation library.
	httpDuration, err := meter.Float64Histogram("http.server.request.duration", metric.WithUnit("s"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	counter.Add(ctx, 1)
	histogram.Record(ctx, 7)
	httpDuration.Record(ctx, 0.1)

	var rm metricdata.ResourceMetrics
	if err := r.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	byName := map[string]metricdata.Metrics{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			byName[m.Name] = m
		}
	}
	for _, want := range []string{"acme.c2d.exports.created", "acme.c2d.export.duration"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("metric %q missing; got %v", want, keysOf(byName))
		}
	}
	if _, unprefixed := byName["c2d.exports.created"]; unprefixed {
		t.Error("the unprefixed name must not be exported as well")
	}
	// Semantic-convention metrics keep their standard names: dashboards and
	// backends rely on them, and services are told apart by resource
	// attributes instead.
	if _, ok := byName["http.server.request.duration"]; !ok {
		t.Errorf("the semconv metric must keep its name; got %v", keysOf(byName))
	}
	if _, renamed := byName["acme.http.server.request.duration"]; renamed {
		t.Error("a semantic-convention metric must not be renamed by the prefix")
	}
	if m := byName["acme.c2d.exports.created"]; m.Unit != "{export}" || m.Description == "" {
		t.Errorf("unit and description must survive the rename: %+v", m)
	}
	hist, ok := byName["acme.c2d.export.duration"].Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("renamed histogram has type %T", byName["acme.c2d.export.duration"].Data)
	}
	if got := hist.DataPoints[0].Bounds; len(got) != 4 || got[0] != 1 || got[3] != 30 {
		t.Errorf("custom bucket boundaries lost through the view: %v", got)
	}
}

func keysOf(m map[string]metricdata.Metrics) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
