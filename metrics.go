package main

import (
	"sync/atomic"
)

// Metrics holds counters for proxy operations.
type Metrics struct {
	TotalConnections  atomic.Int64
	ActiveConnections atomic.Int64

	ReadQueries  atomic.Int64
	WriteQueries atomic.Int64

	PrimaryRouted atomic.Int64
	ReplicaRouted atomic.Int64

	HealthCheckFailures atomic.Int64
}

// Snapshot returns a point-in-time copy of all metrics as a plain map so it
// can be serialised to JSON without races.
func (m *Metrics) Snapshot() map[string]int64 {
	return map[string]int64{
		"total_connections":    m.TotalConnections.Load(),
		"active_connections":   m.ActiveConnections.Load(),
		"read_queries":         m.ReadQueries.Load(),
		"write_queries":        m.WriteQueries.Load(),
		"primary_routed":       m.PrimaryRouted.Load(),
		"replica_routed":       m.ReplicaRouted.Load(),
		"health_check_failures": m.HealthCheckFailures.Load(),
	}
}

// PrometheusText returns a minimal Prometheus-compatible text exposition of
// all counters.
func (m *Metrics) PrometheusText() string {
	snap := m.Snapshot()
	lines := []string{
		"# HELP pgproxy_total_connections Total client connections accepted",
		"# TYPE pgproxy_total_connections counter",
		itoa("pgproxy_total_connections", snap["total_connections"]),

		"# HELP pgproxy_active_connections Currently active client connections",
		"# TYPE pgproxy_active_connections gauge",
		itoa("pgproxy_active_connections", snap["active_connections"]),

		"# HELP pgproxy_read_queries Total read (SELECT) queries routed",
		"# TYPE pgproxy_read_queries counter",
		itoa("pgproxy_read_queries", snap["read_queries"]),

		"# HELP pgproxy_write_queries Total write queries routed",
		"# TYPE pgproxy_write_queries counter",
		itoa("pgproxy_write_queries", snap["write_queries"]),

		"# HELP pgproxy_primary_routed Queries sent to primary",
		"# TYPE pgproxy_primary_routed counter",
		itoa("pgproxy_primary_routed", snap["primary_routed"]),

		"# HELP pgproxy_replica_routed Queries sent to a replica",
		"# TYPE pgproxy_replica_routed counter",
		itoa("pgproxy_replica_routed", snap["replica_routed"]),

		"# HELP pgproxy_health_check_failures Total health check failures",
		"# TYPE pgproxy_health_check_failures counter",
		itoa("pgproxy_health_check_failures", snap["health_check_failures"]),
	}

	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func itoa(name string, val int64) string {
	return name + " " + int64ToStr(val)
}

func int64ToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
