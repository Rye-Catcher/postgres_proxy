package main

import (
	"errors"
	"log/slog"
	"sync/atomic"
)

// Router decides which database node should serve a given query.
type Router struct {
	primary     *DBNode
	replicas    []*DBNode
	replicaIdx  atomic.Uint64 // round-robin index
	metrics     *Metrics
	logger      *slog.Logger
}

// NewRouter creates a Router. replicas may be empty, in which case all queries
// are sent to the primary.
func NewRouter(primary *DBNode, replicas []*DBNode, metrics *Metrics, logger *slog.Logger) *Router {
	return &Router{
		primary:  primary,
		replicas: replicas,
		metrics:  metrics,
		logger:   logger,
	}
}

// ErrNoPrimaryAvailable is returned when the primary node is unhealthy.
var ErrNoPrimaryAvailable = errors.New("no healthy primary node available")

// ErrNoReplicaAvailable is returned when all replica nodes are unhealthy.
var ErrNoReplicaAvailable = errors.New("no healthy replica node available")

// Route returns the DSN of the database node that should handle sql.
// Read queries (SELECT, EXPLAIN, …) are sent to a healthy replica using
// round-robin; all other queries go to the primary.  If no replicas are
// configured or all are unhealthy, reads fall back to the primary.
func (r *Router) Route(sql string) (string, error) {
	qt := ParseQueryType(sql)

	switch qt {
	case QueryTypeRead:
		r.metrics.ReadQueries.Add(1)
		if node := r.pickReplica(); node != nil {
			r.metrics.ReplicaRouted.Add(1)
			r.logger.Debug("routing query to replica", "dsn", redactDSN(node.DSN))
			return node.DSN, nil
		}
		// Fall back to primary for reads when no replica is available.
		r.logger.Warn("no healthy replica available, falling back to primary for read query")
		fallthrough

	case QueryTypeWrite:
		r.metrics.WriteQueries.Add(1)
		if !r.primary.IsHealthy() {
			return "", ErrNoPrimaryAvailable
		}
		r.metrics.PrimaryRouted.Add(1)
		r.logger.Debug("routing query to primary", "dsn", redactDSN(r.primary.DSN))
		return r.primary.DSN, nil
	}

	return "", ErrNoPrimaryAvailable
}

// RouteNode is like Route but returns the *DBNode instead of the DSN string.
func (r *Router) RouteNode(sql string) (*DBNode, error) {
	qt := ParseQueryType(sql)

	switch qt {
	case QueryTypeRead:
		r.metrics.ReadQueries.Add(1)
		if node := r.pickReplica(); node != nil {
			r.metrics.ReplicaRouted.Add(1)
			r.logger.Debug("routing query to replica", "dsn", redactDSN(node.DSN))
			return node, nil
		}
		r.logger.Warn("no healthy replica available, falling back to primary for read query")
		fallthrough

	case QueryTypeWrite:
		r.metrics.WriteQueries.Add(1)
		if !r.primary.IsHealthy() {
			return nil, ErrNoPrimaryAvailable
		}
		r.metrics.PrimaryRouted.Add(1)
		r.logger.Debug("routing query to primary", "dsn", redactDSN(r.primary.DSN))
		return r.primary, nil
	}

	return nil, ErrNoPrimaryAvailable
}

// pickReplica returns a healthy replica using round-robin, or nil if none is
// available.
func (r *Router) pickReplica() *DBNode {
	if len(r.replicas) == 0 {
		return nil
	}
	n := uint64(len(r.replicas))
	for i := uint64(0); i < n; i++ {
		idx := r.replicaIdx.Add(1) % n
		node := r.replicas[idx]
		if node.IsHealthy() {
			return node
		}
	}
	return nil
}
