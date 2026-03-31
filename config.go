package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all proxy configuration loaded from environment variables.
type Config struct {
	// Proxy listen address (e.g. "0.0.0.0:5432")
	ListenAddr string

	// Primary database DSN
	PrimaryDSN string

	// Replica DSNs (comma-separated list)
	ReplicaDSNs []string

	// Admin HTTP endpoint address (e.g. "0.0.0.0:8080")
	AdminAddr string

	// Health check interval
	HealthCheckInterval time.Duration

	// Connection pool settings
	MaxOpenConns int
	MaxIdleConns int

	// Log level: debug, info, warn, error
	LogLevel string
}

// LoadConfig reads configuration from environment variables and returns a
// Config. Missing required variables cause an error.
func LoadConfig() (*Config, error) {
	cfg := &Config{
		ListenAddr:          getEnv("PROXY_LISTEN_ADDR", "0.0.0.0:5432"),
		AdminAddr:           getEnv("PROXY_ADMIN_ADDR", "0.0.0.0:8080"),
		LogLevel:            getEnv("LOG_LEVEL", "info"),
		HealthCheckInterval: getDurationEnv("HEALTH_CHECK_INTERVAL", 10*time.Second),
		MaxOpenConns:        getIntEnv("DB_MAX_OPEN_CONNS", 10),
		MaxIdleConns:        getIntEnv("DB_MAX_IDLE_CONNS", 5),
	}

	primary := getEnv("PRIMARY_DSN", "")
	if primary == "" {
		return nil, fmt.Errorf("PRIMARY_DSN environment variable is required")
	}
	cfg.PrimaryDSN = primary

	replicaRaw := getEnv("REPLICA_DSNS", "")
	if replicaRaw != "" {
		for _, dsn := range strings.Split(replicaRaw, ",") {
			dsn = strings.TrimSpace(dsn)
			if dsn != "" {
				cfg.ReplicaDSNs = append(cfg.ReplicaDSNs, dsn)
			}
		}
	}

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getIntEnv(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

func getDurationEnv(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}
