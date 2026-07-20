// Package metrics is a minimal Prometheus text-format counter registry —
// enough for scraping without pulling in client_golang.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Counters exposed on /metrics.
var (
	HTTPRequests   = NewCounterVec("funnel_manager_http_requests_total", "HTTP requests served, by route pattern and status code.", "route", "code")
	ReconcileRuns  = NewCounterVec("funnel_manager_reconcile_runs_total", "Reconcile passes, by result.", "result")
	OrphansDeleted = NewCounterVec("funnel_manager_reconcile_orphans_deleted_total", "Orphan mirror Ingresses deleted by the reconciler.")
)

var (
	registryMu sync.Mutex
	registry   []*CounterVec
)

// CounterVec is a monotonically increasing counter partitioned by label values.
type CounterVec struct {
	name   string
	help   string
	labels []string
	mu     sync.Mutex
	vals   map[string]uint64
}

// NewCounterVec creates and registers a counter.
func NewCounterVec(name, help string, labels ...string) *CounterVec {
	c := &CounterVec{name: name, help: help, labels: labels, vals: map[string]uint64{}}
	registryMu.Lock()
	registry = append(registry, c)
	registryMu.Unlock()
	return c
}

// Inc increments the counter for the given label values. A mismatched value
// count is silently dropped — metrics must never take the app down.
func (c *CounterVec) Inc(values ...string) {
	if len(values) != len(c.labels) {
		return
	}
	key := labelKey(c.labels, values)
	c.mu.Lock()
	c.vals[key]++
	c.mu.Unlock()
}

// Handler serves all registered counters in Prometheus text format.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		registryMu.Lock()
		counters := append([]*CounterVec(nil), registry...)
		registryMu.Unlock()

		var b strings.Builder
		for _, c := range counters {
			c.write(&b)
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	})
}

func (c *CounterVec) write(b *strings.Builder) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n", c.name, c.help, c.name)
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.vals))
	for k := range c.vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%s%s %d\n", c.name, k, c.vals[k])
	}
}

func labelKey(names, values []string) string {
	if len(names) == 0 {
		return ""
	}
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + `="` + escapeLabelValue(values[i]) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabelValue(s string) string {
	return labelEscaper.Replace(s)
}
