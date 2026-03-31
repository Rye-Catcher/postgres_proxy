package main

import (
	"log/slog"
	"os"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func makeNode(dsn string, healthy bool) *DBNode {
	node := &DBNode{DSN: dsn}
	if healthy {
		node.status = NodeStatusHealthy
	} else {
		node.status = NodeStatusUnhealthy
	}
	return node
}

// TestRouterWritesToPrimary verifies that write queries always go to primary.
func TestRouterWritesToPrimary(t *testing.T) {
	primary := makeNode("postgres://primary:5432/db", true)
	replica := makeNode("postgres://replica:5432/db", true)
	metrics := &Metrics{}
	router := NewRouter(primary, []*DBNode{replica}, metrics, testLogger())

	dsn, err := router.Route("INSERT INTO t VALUES (1)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dsn != primary.DSN {
		t.Errorf("write query routed to %q, want primary %q", dsn, primary.DSN)
	}
	if metrics.WriteQueries.Load() != 1 {
		t.Errorf("write_queries = %d, want 1", metrics.WriteQueries.Load())
	}
	if metrics.PrimaryRouted.Load() != 1 {
		t.Errorf("primary_routed = %d, want 1", metrics.PrimaryRouted.Load())
	}
}

// TestRouterReadsToReplica verifies that read queries go to a healthy replica.
func TestRouterReadsToReplica(t *testing.T) {
	primary := makeNode("postgres://primary:5432/db", true)
	replica := makeNode("postgres://replica:5432/db", true)
	metrics := &Metrics{}
	router := NewRouter(primary, []*DBNode{replica}, metrics, testLogger())

	dsn, err := router.Route("SELECT * FROM users")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dsn != replica.DSN {
		t.Errorf("read query routed to %q, want replica %q", dsn, replica.DSN)
	}
	if metrics.ReadQueries.Load() != 1 {
		t.Errorf("read_queries = %d, want 1", metrics.ReadQueries.Load())
	}
	if metrics.ReplicaRouted.Load() != 1 {
		t.Errorf("replica_routed = %d, want 1", metrics.ReplicaRouted.Load())
	}
}

// TestRouterReadsFallBackToPrimary verifies fallback when all replicas are unhealthy.
func TestRouterReadsFallBackToPrimary(t *testing.T) {
	primary := makeNode("postgres://primary:5432/db", true)
	replica := makeNode("postgres://replica:5432/db", false) // unhealthy
	metrics := &Metrics{}
	router := NewRouter(primary, []*DBNode{replica}, metrics, testLogger())

	dsn, err := router.Route("SELECT 1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dsn != primary.DSN {
		t.Errorf("fallback read routed to %q, want primary %q", dsn, primary.DSN)
	}
}

// TestRouterNoReplicas verifies that reads go to primary when no replicas are
// configured.
func TestRouterNoReplicas(t *testing.T) {
	primary := makeNode("postgres://primary:5432/db", true)
	metrics := &Metrics{}
	router := NewRouter(primary, nil, metrics, testLogger())

	dsn, err := router.Route("SELECT 1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dsn != primary.DSN {
		t.Errorf("read with no replicas routed to %q, want primary %q", dsn, primary.DSN)
	}
}

// TestRouterUnhealthyPrimary verifies an error is returned when the primary is
// down.
func TestRouterUnhealthyPrimary(t *testing.T) {
	primary := makeNode("postgres://primary:5432/db", false) // unhealthy
	metrics := &Metrics{}
	router := NewRouter(primary, nil, metrics, testLogger())

	_, err := router.Route("INSERT INTO t VALUES (1)")
	if err == nil {
		t.Error("expected error for unhealthy primary, got nil")
	}
}

// TestRouterRoundRobin verifies that reads are distributed across replicas.
func TestRouterRoundRobin(t *testing.T) {
	primary := makeNode("postgres://primary:5432/db", true)
	rep1 := makeNode("postgres://replica1:5432/db", true)
	rep2 := makeNode("postgres://replica2:5432/db", true)
	metrics := &Metrics{}
	router := NewRouter(primary, []*DBNode{rep1, rep2}, metrics, testLogger())

	seen := map[string]int{}
	for i := 0; i < 10; i++ {
		dsn, err := router.Route("SELECT 1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		seen[dsn]++
	}
	if seen[rep1.DSN] == 0 || seen[rep2.DSN] == 0 {
		t.Errorf("round-robin not distributing: %v", seen)
	}
}
