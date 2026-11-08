package httpapi

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Metrics is a minimal Prometheus text-format registry (counters + one histogram).
type Metrics struct {
	mu       sync.Mutex
	counters map[string]float64 // full series name with labels -> value
	buckets  []float64
	hist     map[string]*histogram
	version  string
}

type histogram struct {
	counts []uint64
	sum    float64
	count  uint64
}

// NewMetrics creates an empty registry.
func NewMetrics(version string) *Metrics {
	return &Metrics{
		counters: map[string]float64{},
		buckets:  []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		hist:     map[string]*histogram{},
		version:  version,
	}
}

func labels(kv ...string) string {
	if len(kv) == 0 {
		return ""
	}
	parts := make([]string, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(kv[i+1])
		parts = append(parts, fmt.Sprintf(`%s="%s"`, kv[i], v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// Inc adds 1 to a counter.
func (m *Metrics) Inc(name string, kv ...string) {
	m.mu.Lock()
	m.counters[name+labels(kv...)]++
	m.mu.Unlock()
}

// Observe records a request duration.
func (m *Metrics) Observe(route string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hist[route]
	if !ok {
		h = &histogram{counts: make([]uint64, len(m.buckets))}
		m.hist[route] = h
	}
	s := d.Seconds()
	for i, b := range m.buckets {
		if s <= b {
			h.counts[i]++
		}
	}
	h.sum += s
	h.count++
}

// Write renders all series in the Prometheus exposition format.
func (m *Metrics) Write(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fmt.Fprintf(w, "# TYPE shipit_build_info gauge\nshipit_build_info%s 1\n", labels("version", m.version))
	names := make([]string, 0, len(m.counters))
	for k := range m.counters {
		names = append(names, k)
	}
	sort.Strings(names)
	typed := map[string]bool{}
	for _, k := range names {
		base := strings.SplitN(k, "{", 2)[0]
		if !typed[base] {
			fmt.Fprintf(w, "# TYPE %s counter\n", base)
			typed[base] = true
		}
		fmt.Fprintf(w, "%s %g\n", k, m.counters[k])
	}
	routes := make([]string, 0, len(m.hist))
	for r := range m.hist {
		routes = append(routes, r)
	}
	sort.Strings(routes)
	if len(routes) > 0 {
		fmt.Fprintln(w, "# TYPE shipit_http_request_duration_seconds histogram")
	}
	for _, r := range routes {
		h := m.hist[r]
		for i, b := range m.buckets {
			fmt.Fprintf(w, "shipit_http_request_duration_seconds_bucket%s %d\n", labels("route", r, "le", fmt.Sprint(b)), h.counts[i])
		}
		fmt.Fprintf(w, "shipit_http_request_duration_seconds_bucket%s %d\n", labels("route", r, "le", "+Inf"), h.count)
		fmt.Fprintf(w, "shipit_http_request_duration_seconds_sum%s %g\n", labels("route", r), h.sum)
		fmt.Fprintf(w, "shipit_http_request_duration_seconds_count%s %d\n", labels("route", r), h.count)
	}
}
