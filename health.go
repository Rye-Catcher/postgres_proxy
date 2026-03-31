package main

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

// NodeStatus represents the health state of a database node.
type NodeStatus int

const (
	NodeStatusHealthy NodeStatus = iota
	NodeStatusUnhealthy
)

func (s NodeStatus) String() string {
	if s == NodeStatusHealthy {
		return "healthy"
	}
	return "unhealthy"
}

// DBNode holds a database connection and its current health status.
type DBNode struct {
	DSN    string
	DB     *sql.DB
	mu     sync.RWMutex
	status NodeStatus
}

// IsHealthy returns true if the node is currently considered healthy.
func (n *DBNode) IsHealthy() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.status == NodeStatusHealthy
}

// setStatus updates the health status of the node.
func (n *DBNode) setStatus(s NodeStatus) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.status = s
}

// HealthChecker monitors all database nodes at a regular interval.
type HealthChecker struct {
	primary  *DBNode
	replicas []*DBNode
	interval time.Duration
	metrics  *Metrics
	logger   *slog.Logger
}

// NewHealthChecker creates a HealthChecker. It does not start background
// goroutines; call Start to begin checking.
func NewHealthChecker(primary *DBNode, replicas []*DBNode, interval time.Duration, metrics *Metrics, logger *slog.Logger) *HealthChecker {
	return &HealthChecker{
		primary:  primary,
		replicas: replicas,
		interval: interval,
		metrics:  metrics,
		logger:   logger,
	}
}

// Start launches the background health-check loop. It runs until ctx is
// cancelled.
func (hc *HealthChecker) Start(ctx context.Context) {
	hc.checkAll()
	ticker := time.NewTicker(hc.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hc.checkAll()
		}
	}
}

func (hc *HealthChecker) checkAll() {
	hc.check(hc.primary)
	for _, r := range hc.replicas {
		hc.check(r)
	}
}

func (hc *HealthChecker) check(node *DBNode) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := node.DB.PingContext(ctx); err != nil {
		if node.IsHealthy() {
			hc.logger.Warn("database node became unhealthy", "dsn", redactDSN(node.DSN), "error", err)
		}
		node.setStatus(NodeStatusUnhealthy)
		hc.metrics.HealthCheckFailures.Add(1)
	} else {
		if !node.IsHealthy() {
			hc.logger.Info("database node recovered", "dsn", redactDSN(node.DSN))
		}
		node.setStatus(NodeStatusHealthy)
	}
}

// redactDSN removes the password from a DSN for safe logging.
func redactDSN(dsn string) string {
	// Simple redaction: replace password=... with password=***
	result := []byte(dsn)
	start := -1
	for i := 0; i < len(result)-9; i++ {
		if string(result[i:i+9]) == "password=" {
			start = i + 9
			break
		}
	}
	if start == -1 {
		return dsn
	}
	end := start
	for end < len(result) && result[end] != ' ' && result[end] != '&' {
		end++
	}
	return string(result[:start]) + "***" + string(result[end:])
}
