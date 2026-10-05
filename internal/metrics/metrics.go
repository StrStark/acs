// Package metrics keeps request counters and renders them, plus gauges
// supplied at scrape time, in the Prometheus text exposition format.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

type httpKey struct {
	api    string
	method string
	class  string // 2xx, 3xx, 4xx, 5xx
}

type Registry struct {
	mu       sync.Mutex
	requests map[httpKey]uint64
	bytesOut map[string]uint64
	bytesIn  map[string]uint64
}

func New() *Registry {
	return &Registry{
		requests: make(map[httpKey]uint64),
		bytesOut: make(map[string]uint64),
		bytesIn:  make(map[string]uint64),
	}
}

// ObserveHTTP records one completed request on the given API ("panel", "s3").
func (r *Registry) ObserveHTTP(api, method string, status int, out, in int64) {
	if r == nil {
		return
	}
	k := httpKey{api: api, method: method, class: fmt.Sprintf("%dxx", status/100)}
	r.mu.Lock()
	r.requests[k]++
	if out > 0 {
		r.bytesOut[api] += uint64(out)
	}
	if in > 0 {
		r.bytesIn[api] += uint64(in)
	}
	r.mu.Unlock()
}

// Gauge is a labelled value computed at scrape time.
type Gauge struct {
	Name   string
	Help   string
	Labels map[string]string
	Value  float64
}

func labels(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(m[k])
		parts[i] = fmt.Sprintf(`%s="%s"`, k, v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// Write renders counters followed by gauges.
func (r *Registry) Write(w io.Writer, gauges []Gauge) {
	r.mu.Lock()
	fmt.Fprintln(w, "# HELP acs_http_requests_total HTTP requests by API, method and status class.")
	fmt.Fprintln(w, "# TYPE acs_http_requests_total counter")
	keys := make([]httpKey, 0, len(r.requests))
	for k := range r.requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		return a.api+a.method+a.class < b.api+b.method+b.class
	})
	for _, k := range keys {
		fmt.Fprintf(w, "acs_http_requests_total%s %d\n",
			labels(map[string]string{"api": k.api, "method": k.method, "status": k.class}), r.requests[k])
	}
	for _, c := range []struct {
		name, help string
		m          map[string]uint64
	}{
		{"acs_http_response_bytes_total", "Bytes sent in HTTP responses.", r.bytesOut},
		{"acs_http_request_bytes_total", "Bytes received in HTTP request bodies.", r.bytesIn},
	} {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.name, c.help, c.name)
		apis := make([]string, 0, len(c.m))
		for a := range c.m {
			apis = append(apis, a)
		}
		sort.Strings(apis)
		for _, a := range apis {
			fmt.Fprintf(w, "%s%s %d\n", c.name, labels(map[string]string{"api": a}), c.m[a])
		}
	}
	r.mu.Unlock()

	seen := map[string]bool{}
	for _, g := range gauges {
		if !seen[g.Name] {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", g.Name, g.Help, g.Name)
			seen[g.Name] = true
		}
		fmt.Fprintf(w, "%s%s %g\n", g.Name, labels(g.Labels), g.Value)
	}
}
