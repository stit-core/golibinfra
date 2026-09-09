package otelkit

import "go.opentelemetry.io/otel/metric"

// LongDurationBuckets — границы гистограмм длительности (секунды) для операций,
// которые штатно идут минутами (генерация LLM): продолжает DurationBuckets вверх,
// иначе весь хвост ложится в +Inf и p95 неотличим от p99. Применяется вместе с
// metric.WithUnit("s"); для всего остального — DurationBuckets.
func LongDurationBuckets() metric.HistogramOption {
	return metric.WithExplicitBucketBoundaries(longDurationBounds...)
}

var longDurationBounds = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600}
