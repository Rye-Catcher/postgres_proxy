package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
)

// AdminServer exposes an HTTP endpoint with proxy statistics and diagnostics.
type AdminServer struct {
	cfg     *Config
	metrics *Metrics
	primary *DBNode
	replicas []*DBNode
	logger  *slog.Logger
}

// NewAdminServer creates an AdminServer.
func NewAdminServer(cfg *Config, metrics *Metrics, primary *DBNode, replicas []*DBNode, logger *slog.Logger) *AdminServer {
	return &AdminServer{
		cfg:      cfg,
		metrics:  metrics,
		primary:  primary,
		replicas: replicas,
		logger:   logger,
	}
}

// Start registers HTTP handlers and starts the admin HTTP server. It blocks
// until the server encounters an error.
func (a *AdminServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.handleHealth)
	mux.HandleFunc("/stats", a.handleStats)
	mux.HandleFunc("/metrics", a.handleMetrics)
	mux.HandleFunc("/nodes", a.handleNodes)

	a.logger.Info("admin server listening", "addr", a.cfg.AdminAddr)
	return http.ListenAndServe(a.cfg.AdminAddr, mux)
}

// handleHealth returns 200 OK when the primary is healthy, 503 otherwise.
func (a *AdminServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if a.primary.IsHealthy() {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status":"ok"}`)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, `{"status":"unhealthy"}`)
	}
}

// handleStats returns all metrics as a JSON object.
func (a *AdminServer) handleStats(w http.ResponseWriter, r *http.Request) {
	snap := a.metrics.Snapshot()
	snap["goroutines"] = int64(runtime.NumGoroutine())

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(snap); err != nil {
		a.logger.Error("failed to encode stats", "error", err)
	}
}

// handleMetrics exposes a minimal Prometheus-compatible text endpoint.
func (a *AdminServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprint(w, a.metrics.PrometheusText())
}

// nodeInfo is a JSON-serialisable view of a database node.
type nodeInfo struct {
	DSN    string `json:"dsn"`
	Status string `json:"status"`
	Role   string `json:"role"`
}

// nodeStatusStr converts an IsHealthy bool to a status string.
func nodeStatusStr(healthy bool) string {
	if healthy {
		return "healthy"
	}
	return "unhealthy"
}

// handleNodes returns the status of all known database nodes.
func (a *AdminServer) handleNodes(w http.ResponseWriter, r *http.Request) {
	nodes := []nodeInfo{
		{DSN: redactDSN(a.primary.DSN), Status: nodeStatusStr(a.primary.IsHealthy()), Role: "primary"},
	}
	for _, rep := range a.replicas {
		nodes = append(nodes, nodeInfo{
			DSN:    redactDSN(rep.DSN),
			Status: nodeStatusStr(rep.IsHealthy()),
			Role:   "replica",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(nodes); err != nil {
		a.logger.Error("failed to encode nodes", "error", err)
	}
}
