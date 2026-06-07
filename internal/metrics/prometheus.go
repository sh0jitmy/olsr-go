// Copyright 2026 The olsrd-go Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package metrics

import (
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Define Prometheus metrics matching the requirement specs
var (
	// Packet Metrics
	HelloRxTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "olsr_hello_rx_total",
		Help: "Total number of HELLO packets received",
	})
	HelloTxTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "olsr_hello_tx_total",
		Help: "Total number of HELLO packets transmitted",
	})
	TCRxTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "olsr_tc_rx_total",
		Help: "Total number of TC packets received",
	})
	TCTxTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "olsr_tc_tx_total",
		Help: "Total number of TC packets transmitted",
	})
	PacketParseFailureTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "olsr_packet_parse_failure_total",
		Help: "Total number of packet parsing failures",
	})

	// Neighbor Metrics
	NeighborTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "olsr_neighbor_total",
		Help: "Total number of discovered neighbors",
	})
	NeighborSymmetricTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "olsr_neighbor_symmetric_total",
		Help: "Total number of symmetric neighbors",
	})
	NeighborLostTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "olsr_neighbor_lost_total",
		Help: "Total number of lost/aged-out neighbors",
	})

	// SPF Metrics
	SPFRunsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "olsr_spf_runs_total",
		Help: "Total number of SPF calculations run",
	})
	SPFDurationSeconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "olsr_spf_duration_seconds",
		Help: "Duration of the last SPF calculation in seconds",
	})
	RouteCount = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "olsr_route_count",
		Help: "Number of active routes in the routing table",
	})

	// Zebra Metrics
	ZapiConnected = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "olsr_zapi_connected",
		Help: "Is Zebra ZAPI connection currently active (1 = yes, 0 = no)",
	})
	ZapiTxTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "olsr_zapi_tx_total",
		Help: "Total ZAPI messages successfully transmitted",
	})
	ZapiFailureTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "olsr_zapi_failure_total",
		Help: "Total ZAPI transmission or connection failures",
	})

	// System Metrics
	GoroutineCount = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "olsr_goroutine_count",
		Help: "Number of running Go goroutines",
	})
	MemoryUsageBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "olsr_memory_usage_bytes",
		Help: "Allocated memory usage in bytes",
	})
	QueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "olsr_queue_depth",
		Help: "Current depth of internal event bus channels",
	})
	PacketBacklog = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "olsr_packet_backlog",
		Help: "Number of packets waiting in the processing backlog",
	})
)

func init() {
	// Register custom metrics
	prometheus.MustRegister(HelloRxTotal)
	prometheus.MustRegister(HelloTxTotal)
	prometheus.MustRegister(TCRxTotal)
	prometheus.MustRegister(TCTxTotal)
	prometheus.MustRegister(PacketParseFailureTotal)

	prometheus.MustRegister(NeighborTotal)
	prometheus.MustRegister(NeighborSymmetricTotal)
	prometheus.MustRegister(NeighborLostTotal)

	prometheus.MustRegister(SPFRunsTotal)
	prometheus.MustRegister(SPFDurationSeconds)
	prometheus.MustRegister(RouteCount)

	prometheus.MustRegister(ZapiConnected)
	prometheus.MustRegister(ZapiTxTotal)
	prometheus.MustRegister(ZapiFailureTotal)

	prometheus.MustRegister(GoroutineCount)
	prometheus.MustRegister(MemoryUsageBytes)
	prometheus.MustRegister(QueueDepth)
	prometheus.MustRegister(PacketBacklog)
}

// StartSystemMetricsCollector polls system stats periodically
func StartSystemMetricsCollector(stopChan <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-stopChan:
				return
			case <-ticker.C:
				GoroutineCount.Set(float64(runtime.NumGoroutine()))

				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				MemoryUsageBytes.Set(float64(m.Alloc))
			}
		}
	}()
}
