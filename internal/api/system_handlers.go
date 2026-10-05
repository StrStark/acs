package api

import (
	"crypto/subtle"
	_ "embed"
	"net/http"
	"runtime"
	"strings"
	"time"

	"acs/internal/auth"
	"acs/internal/metrics"
	"acs/internal/sysinfo"
	"acs/internal/version"
)

//go:embed openapi.yaml
var openapiSpec []byte

func handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.Write(openapiSpec)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.db.PingContext(r.Context()); err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type systemInfo struct {
	Version   string            `json:"version"`
	GoVersion string            `json:"goVersion"`
	StartedAt time.Time         `json:"startedAt"`
	Uptime    int64             `json:"uptimeSeconds"`
	DataDir   string            `json:"dataDir,omitempty"`
	Disk      sysinfo.DiskUsage `json:"disk"`
	Totals    struct {
		Buckets     int   `json:"buckets"`
		Objects     int64 `json:"objects"`
		Bytes       int64 `json:"bytes"`
		StoredBytes int64 `json:"storedBytes"`
	} `json:"totals"`
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	disk, err := sysinfo.Disk(s.cfg.DataDir)
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	info := systemInfo{
		Version:   version.Version,
		GoVersion: runtime.Version(),
		StartedAt: s.startedAt,
		Uptime:    int64(time.Since(s.startedAt).Seconds()),
		Disk:      disk,
	}
	p := principal(r)
	if p.Can(auth.ActAdmin, "") {
		info.DataDir = s.cfg.DataDir
	}
	buckets, err := s.objects.ListBuckets(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	for _, b := range buckets {
		if !p.Can(auth.ActRead, b.Name) {
			continue
		}
		st, err := s.objects.BucketStats(r.Context(), b.Name)
		if err != nil {
			writeError(w, r, err)
			return
		}
		info.Totals.Buckets++
		info.Totals.Objects += st.Objects
		info.Totals.Bytes += st.Bytes
		info.Totals.StoredBytes += st.StoredBytes
	}
	writeJSON(w, http.StatusOK, info)
}

// handleMetrics serves Prometheus metrics when ACS_METRICS_TOKEN is set.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	token := s.cfg.MetricsToken
	if token == "" {
		http.NotFound(w, r)
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var gauges []metrics.Gauge
	gauges = append(gauges, metrics.Gauge{Name: "acs_uptime_seconds", Help: "Seconds since the server started.",
		Value: time.Since(s.startedAt).Seconds()})
	if disk, err := sysinfo.Disk(s.cfg.DataDir); err == nil {
		gauges = append(gauges,
			metrics.Gauge{Name: "acs_disk_total_bytes", Help: "Size of the data filesystem.", Value: float64(disk.Total)},
			metrics.Gauge{Name: "acs_disk_free_bytes", Help: "Free space on the data filesystem.", Value: float64(disk.Free)},
		)
	}
	if buckets, err := s.objects.ListBuckets(r.Context()); err == nil {
		for _, b := range buckets {
			st, err := s.objects.BucketStats(r.Context(), b.Name)
			if err != nil {
				continue
			}
			l := map[string]string{"bucket": b.Name}
			gauges = append(gauges,
				metrics.Gauge{Name: "acs_bucket_objects", Help: "Current objects per bucket.", Labels: l, Value: float64(st.Objects)},
				metrics.Gauge{Name: "acs_bucket_bytes", Help: "Bytes of current objects per bucket.", Labels: l, Value: float64(st.Bytes)},
				metrics.Gauge{Name: "acs_bucket_stored_bytes", Help: "Bytes stored per bucket including old versions.", Labels: l, Value: float64(st.StoredBytes)},
			)
		}
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.metrics.Write(w, gauges)
}
