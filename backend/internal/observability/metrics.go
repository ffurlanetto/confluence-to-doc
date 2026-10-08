package observability

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

// meterName identifies this application's own instrumentation.
const meterName = "github.com/ffurlanetto/confluence-to-doc"

// MetricNamespace prefixes the metrics this application defines itself, as
// opposed to those emitted by instrumentation libraries.
const MetricNamespace = "c2d."

// Attribute keys shared by the metrics and the spans of this application.
const (
	AttrFormat   = attribute.Key("c2d.export.format")
	AttrOutcome  = attribute.Key("c2d.export.outcome")
	AttrExportID = attribute.Key("c2d.export.id")
	AttrPageID   = attribute.Key("c2d.page.id")
	AttrState    = attribute.Key("c2d.queue.state")
	// AttrCause tells, for a failed attempt, whether the user can fix it
	// ("user": token, permissions, missing page, too many pages) or not
	// ("system"). Service level objectives count only the latter.
	AttrCause = attribute.Key("c2d.export.cause")
)

// Instruments are created against the global meter provider. That provider is
// a no-op until Setup runs; the OpenTelemetry global registry re-points these
// instruments at the real provider once it is installed, so package-level
// initialisation is safe.
var (
	meter = otel.Meter(meterName)

	// ExportsCreated counts exports accepted into the queue.
	ExportsCreated = mustInt64Counter("c2d.exports.created",
		metric.WithDescription("Exports accepted into the queue."),
		metric.WithUnit("{export}"))

	// ExportsFinished counts finished attempts, by outcome.
	ExportsFinished = mustInt64Counter("c2d.exports.finished",
		metric.WithDescription("Export attempts finished, by outcome (succeeded, retry, failed) and, when not succeeded, cause (user, system)."),
		metric.WithUnit("{export}"))

	// ExportDuration measures end-to-end generation time.
	ExportDuration = mustFloat64Histogram("c2d.export.duration",
		metric.WithDescription("Time taken to generate an export."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(1, 5, 15, 30, 60, 120, 300, 600, 1200))

	// ExportPages measures the size of successful exports.
	ExportPages = mustInt64Histogram("c2d.export.pages",
		metric.WithDescription("Number of Confluence pages per successful export."),
		metric.WithUnit("{page}"),
		metric.WithExplicitBucketBoundaries(1, 2, 5, 10, 25, 50, 100, 250, 500))
)

// QueueStatsFunc reports the current queue depth.
type QueueStatsFunc func(ctx context.Context) (domain.QueueStats, error)

// RegisterQueueDepth publishes the queue depth as an observable gauge, read
// from the database whenever the collector asks for it.
func RegisterQueueDepth(stats QueueStatsFunc) error {
	gauge, err := meter.Int64ObservableGauge("c2d.queue.exports",
		metric.WithDescription("Exports waiting in or being processed by the queue."),
		metric.WithUnit("{export}"))
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		st, err := stats(ctx)
		if err != nil {
			// Returning the error would mark the whole collection as failed;
			// a transient database hiccup should just skip this sample.
			slog.WarnContext(ctx, "telemetry: reading queue depth", "err", err)
			return nil
		}
		o.ObserveInt64(gauge, int64(st.Queued), metric.WithAttributes(AttrState.String("queued")))
		o.ObserveInt64(gauge, int64(st.Running), metric.WithAttributes(AttrState.String("running")))
		return nil
	}, gauge)
	return err
}

// Instrument creation only fails on a malformed name or unit, which would be a
// programming error; falling back to a no-op keeps telemetry from breaking the
// application.
func mustInt64Counter(name string, opts ...metric.Int64CounterOption) metric.Int64Counter {
	instrument, err := meter.Int64Counter(name, opts...)
	if err != nil {
		slog.Error("telemetry: creating counter", "name", name, "err", err)
		instrument, _ = noop.NewMeterProvider().Meter(meterName).Int64Counter(name)
	}
	return instrument
}

func mustFloat64Histogram(name string, opts ...metric.Float64HistogramOption) metric.Float64Histogram {
	instrument, err := meter.Float64Histogram(name, opts...)
	if err != nil {
		slog.Error("telemetry: creating histogram", "name", name, "err", err)
		instrument, _ = noop.NewMeterProvider().Meter(meterName).Float64Histogram(name)
	}
	return instrument
}

func mustInt64Histogram(name string, opts ...metric.Int64HistogramOption) metric.Int64Histogram {
	instrument, err := meter.Int64Histogram(name, opts...)
	if err != nil {
		slog.Error("telemetry: creating histogram", "name", name, "err", err)
		instrument, _ = noop.NewMeterProvider().Meter(meterName).Int64Histogram(name)
	}
	return instrument
}
