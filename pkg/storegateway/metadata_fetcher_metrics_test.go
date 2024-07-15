// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/pkg/storegateway/metadata_fetcher_metrics_test.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package storegateway

import (
	"bytes"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/grafana/mimir/pkg/util/test"
)

func TestMetadataFetcherMetrics(t *testing.T) {
	mainReg := prometheus.NewPedanticRegistry()

	metrics := NewMetadataFetcherMetrics(test.NewTestingLogger(t))
	mainReg.MustRegister(metrics)

	metrics.AddUserRegistry("user1", populateMetadataFetcherMetrics(3))
	metrics.AddUserRegistry("user2", populateMetadataFetcherMetrics(5))
	metrics.AddUserRegistry("user3", populateMetadataFetcherMetrics(7))

	//noinspection ALL
	err := testutil.GatherAndCompare(mainReg, bytes.NewBufferString(`
		# HELP cortex_blocks_meta_sync_duration_seconds Duration of the blocks metadata synchronization in seconds
		# TYPE cortex_blocks_meta_sync_duration_seconds histogram
		cortex_blocks_meta_sync_duration_seconds_bucket{le="0.01"} 0
		cortex_blocks_meta_sync_duration_seconds_bucket{le="1"} 0
		cortex_blocks_meta_sync_duration_seconds_bucket{le="10"} 3
		cortex_blocks_meta_sync_duration_seconds_bucket{le="100"} 3
		cortex_blocks_meta_sync_duration_seconds_bucket{le="1000"} 3
		cortex_blocks_meta_sync_duration_seconds_bucket{le="+Inf"} 3
		cortex_blocks_meta_sync_duration_seconds_sum 9
		cortex_blocks_meta_sync_duration_seconds_count 3

		# HELP cortex_blocks_meta_sync_failures_total Total blocks metadata synchronization failures
		# TYPE cortex_blocks_meta_sync_failures_total counter
		cortex_blocks_meta_sync_failures_total 30

		# HELP cortex_blocks_meta_syncs_total Total blocks metadata synchronization attempts
		# TYPE cortex_blocks_meta_syncs_total counter
		cortex_blocks_meta_syncs_total 15

		# HELP cortex_blocks_meta_synced Reflects current state of synced blocks (over all tenants).
		# TYPE cortex_blocks_meta_synced gauge
		cortex_blocks_meta_synced{state="corrupted-meta-json"} 75
		cortex_blocks_meta_synced{state="loaded"} 90
`))
	require.NoError(t, err)
}

func populateMetadataFetcherMetrics(base float64) *prometheus.Registry {
	reg := prometheus.NewRegistry()
	m := newMetadataFetcherMetricsMock(reg)

	m.syncs.Add(base * 1)
	m.syncFailures.Add(base * 2)
	m.syncDuration.Observe(3)

	m.synced.WithLabelValues("corrupted-meta-json").Set(base * 5)
	m.synced.WithLabelValues("loaded").Set(base * 6)

	return reg
}

type metadataFetcherMetricsMock struct {
	syncs        prometheus.Counter
	syncFailures prometheus.Counter
	syncDuration prometheus.Histogram
	synced       *prometheus.GaugeVec
}

func newMetadataFetcherMetricsMock(reg prometheus.Registerer) *metadataFetcherMetricsMock {
	var m metadataFetcherMetricsMock

	m.syncs = promauto.With(reg).NewCounter(prometheus.CounterOpts{
		Subsystem: "blocks_meta",
		Name:      "syncs_total",
		Help:      "Total blocks metadata synchronization attempts",
	})
	m.syncFailures = promauto.With(reg).NewCounter(prometheus.CounterOpts{
		Subsystem: "blocks_meta",
		Name:      "sync_failures_total",
		Help:      "Total blocks metadata synchronization failures",
	})
	m.syncDuration = promauto.With(reg).NewHistogram(prometheus.HistogramOpts{
		Subsystem: "blocks_meta",
		Name:      "sync_duration_seconds",
		Help:      "Duration of the blocks metadata synchronization in seconds",
		Buckets:   []float64{0.01, 1, 10, 100, 1000},
	})
	m.synced = promauto.With(reg).NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: "blocks_meta",
		Name:      "synced",
		Help:      "Number of block metadata synced",
	}, []string{"state"})

	return &m
}
