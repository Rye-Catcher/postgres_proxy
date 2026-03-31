package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	_ "github.com/lib/pq"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		// Use plain stderr before the logger is set up.
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(1)
	}

	logger := initLogger(cfg.LogLevel)
	logger.Info("starting postgres proxy",
		"listen", cfg.ListenAddr,
		"admin", cfg.AdminAddr,
		"primary", redactDSN(cfg.PrimaryDSN),
		"replicas", len(cfg.ReplicaDSNs),
	)

	metrics := &Metrics{}

	// Open connections to primary and replicas.
	primary, err := openNode(cfg.PrimaryDSN, cfg.MaxOpenConns, cfg.MaxIdleConns)
	if err != nil {
		logger.Error("failed to open primary connection", "error", err)
		os.Exit(1)
	}

	replicas := make([]*DBNode, 0, len(cfg.ReplicaDSNs))
	for _, dsn := range cfg.ReplicaDSNs {
		node, err := openNode(dsn, cfg.MaxOpenConns, cfg.MaxIdleConns)
		if err != nil {
			logger.Warn("failed to open replica connection", "dsn", redactDSN(dsn), "error", err)
			continue
		}
		replicas = append(replicas, node)
	}

	if len(cfg.ReplicaDSNs) > 0 && len(replicas) == 0 {
		logger.Warn("no replicas could be opened; all reads will go to primary")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Start health checker.
	hc := NewHealthChecker(primary, replicas, cfg.HealthCheckInterval, metrics, logger)
	go hc.Start(ctx)

	// Start admin server.
	router := NewRouter(primary, replicas, metrics, logger)
	admin := NewAdminServer(cfg, metrics, primary, replicas, logger)
	go func() {
		if err := admin.Start(); err != nil {
			logger.Error("admin server error", "error", err)
		}
	}()

	// Start TCP proxy (blocks until ctx is cancelled).
	proxy := NewProxy(cfg, router, metrics, logger)
	if err := proxy.Start(ctx); err != nil {
		logger.Error("proxy error", "error", err)
		os.Exit(1)
	}

	logger.Info("proxy stopped")
}

// openNode opens a *sql.DB for the given DSN and returns a DBNode.
func openNode(dsn string, maxOpen, maxIdle int) (*DBNode, error) {
	// database/sql expects a postgres DSN; convert URL-style if needed.
	connStr := normaliseDSN(dsn)
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)

	node := &DBNode{DSN: dsn, DB: db, status: NodeStatusHealthy}
	return node, nil
}

// normaliseDSN passes the DSN through unchanged; it exists as a hook for
// future normalisation (e.g. URL → keyword=value conversion).
func normaliseDSN(dsn string) string {
	return strings.TrimSpace(dsn)
}
