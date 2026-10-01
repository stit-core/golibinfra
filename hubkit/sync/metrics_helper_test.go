package sync_test

import (
	"testing"

	"git.stit.tech/stit-core/golibinfra/testkit/metrictest"
)

// counterValue — значение счётчика по resource; -1, если серии нет.
func counterValue(t *testing.T, name, resource string) int64 {
	t.Helper()
	return seriesValue(t, name, resource, metrictest.Counter)
}

// gaugeValue — значение int64-gauge по resource; -1, если серии нет.
func gaugeValue(t *testing.T, name, resource string) int64 {
	t.Helper()
	return seriesValue(t, name, resource, metrictest.Gauge)
}

func seriesValue(t *testing.T, name, resource string, kind metrictest.Kind) int64 {
	t.Helper()
	for _, s := range metrictest.Collect(t).Find(name, "resource", resource) {
		if s.Kind == kind {
			return int64(s.Value)
		}
	}
	return -1
}
