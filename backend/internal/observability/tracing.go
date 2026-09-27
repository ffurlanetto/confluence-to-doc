package observability

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Tracer is this application's tracer; it is a no-op until Setup runs.
func Tracer() trace.Tracer { return otel.Tracer(meterName) }

// Start opens a span on the application tracer.
func Start(ctx context.Context, name string, kv ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(kv...))
}

// End closes a span, recording err when there is one. Call it deferred:
//
//	ctx, span := observability.Start(ctx, "convert")
//	defer func() { observability.End(span, err) }()
func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// LogHandler adds the current trace and span ids to every log record emitted
// inside a span, so a log line can be followed into the trace and back.
type LogHandler struct{ slog.Handler }

func (h LogHandler) Handle(ctx context.Context, record slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		record.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, record)
}

func (h LogHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return LogHandler{Handler: h.Handler.WithAttrs(as)}
}

func (h LogHandler) WithGroup(name string) slog.Handler {
	return LogHandler{Handler: h.Handler.WithGroup(name)}
}
