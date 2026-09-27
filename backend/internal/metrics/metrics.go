// Package metrics declares the Prometheus metrics exposed on /metrics.
package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
)

var (
	ExportsCreated = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "c2d_exports_created_total",
		Help: "Exports enqueued, by format.",
	}, []string{"format"})

	ExportsFinished = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "c2d_exports_finished_total",
		Help: "Export attempts finished, by outcome (succeeded, retry, failed).",
	}, []string{"outcome"})

	ExportDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "c2d_export_duration_seconds",
		Help:    "Duration of export processing, by format.",
		Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600, 1200},
	}, []string{"format"})

	ExportPages = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "c2d_export_pages",
		Help:    "Number of pages per successful export.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 10),
	})

	HTTPRequests = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "c2d_http_request_duration_seconds",
		Help:    "HTTP request latency, by route pattern, method and status.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route", "method", "status"})
)

// QueueStatsFunc returns the current queue depth.
type QueueStatsFunc func(ctx context.Context) (domain.QueueStats, error)

type queueCollector struct {
	stats   QueueStatsFunc
	queued  *prometheus.Desc
	running *prometheus.Desc
}

func (c *queueCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.queued
	ch <- c.running
}

func (c *queueCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := c.stats(ctx)
	if err != nil {
		return
	}
	ch <- prometheus.MustNewConstMetric(c.queued, prometheus.GaugeValue, float64(st.Queued))
	ch <- prometheus.MustNewConstMetric(c.running, prometheus.GaugeValue, float64(st.Running))
}

// NewRegistry returns a registry with the application and runtime metrics.
func NewRegistry(stats QueueStatsFunc) *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		ExportsCreated, ExportsFinished, ExportDuration, ExportPages, HTTPRequests,
	)
	if stats != nil {
		reg.MustRegister(&queueCollector{
			stats:   stats,
			queued:  prometheus.NewDesc("c2d_queue_queued", "Exports waiting in the queue.", nil, nil),
			running: prometheus.NewDesc("c2d_queue_running", "Exports being processed.", nil, nil),
		})
	}
	return reg
}
