package main

import "sync"

// TransactionState tracks whether a client session is currently inside an
// explicit transaction block.
type TransactionState int

const (
	// TxStateNone means no explicit transaction is open.
	TxStateNone TransactionState = iota
	// TxStateActive means a transaction is currently in progress (BEGIN or
	// START TRANSACTION has been issued and has not yet been committed or
	// rolled back).
	TxStateActive
)

// TransactionRouter wraps a Router and adds per-session transaction-state
// tracking to guarantee that all statements within a transaction are sent to
// the same backend (the primary).
//
// Routing policy:
//   - When a transaction is active (after BEGIN / START TRANSACTION), ALL
//     statements are unconditionally routed to the primary, regardless of
//     their individual query type.
//   - COMMIT and ROLLBACK (including ROLLBACK TO and ROLLBACK TO SAVEPOINT)
//     end the transaction pin and route to the primary.
//   - SAVEPOINT and RELEASE SAVEPOINT are always sent to the primary because
//     they carry session-level state.
//   - Read-only transactions (BEGIN READ ONLY) and isolation-level variants
//     (BEGIN ISOLATION LEVEL …) are all pinned to the primary for simplicity
//     and correctness.
//   - Outside of an explicit transaction, routing is delegated unchanged to
//     the underlying Router.
//
// A TransactionRouter must NOT be shared across sessions; create one per
// client connection to ensure the state machine is per-session.
type TransactionRouter struct {
	router *Router
	mu     sync.Mutex
	state  TransactionState
}

// NewTransactionRouter creates a TransactionRouter backed by router.
func NewTransactionRouter(router *Router) *TransactionRouter {
	return &TransactionRouter{router: router}
}

// Route returns the DSN of the backend that should handle sql, taking the
// current transaction state into account.
//
// The state machine transitions:
//
//	TxStateNone  + BEGIN/START           → TxStateActive, route to primary
//	TxStateNone  + other                 → delegate to Router
//	TxStateActive + COMMIT/ROLLBACK      → TxStateNone,   route to primary
//	TxStateActive + anything else        → stay TxStateActive, route to primary
//
// SAVEPOINT and RELEASE SAVEPOINT always route to primary regardless of state.
func (tr *TransactionRouter) Route(sql string) (string, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	kw := firstKeyword(sql)

	if tr.state == TxStateActive {
		// Transaction is open: pin everything to primary.
		// COMMIT or ROLLBACK also end the transaction.
		if kw == "COMMIT" || kw == "ROLLBACK" {
			tr.state = TxStateNone
		}
		return tr.routeToPrimary()
	}

	// Not in a transaction.
	// BEGIN and START (TRANSACTION) open a new transaction block.
	if kw == "BEGIN" || kw == "START" {
		tr.state = TxStateActive
		return tr.routeToPrimary()
	}

	// SAVEPOINT and RELEASE carry session state and must go to the primary
	// even outside an explicit transaction (e.g. in auto-commit mode some
	// drivers may issue them independently).
	if kw == "SAVEPOINT" || kw == "RELEASE" {
		return tr.routeToPrimary()
	}

	// Outside a transaction, delegate to the regular Router.
	return tr.router.Route(sql)
}

// State returns the current transaction state. Primarily useful for testing.
func (tr *TransactionRouter) State() TransactionState {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.state
}

// routeToPrimary routes directly to the primary node, updating metrics.
// The caller must hold tr.mu.
func (tr *TransactionRouter) routeToPrimary() (string, error) {
	if !tr.router.primary.IsHealthy() {
		return "", ErrNoPrimaryAvailable
	}
	tr.router.metrics.WriteQueries.Add(1)
	tr.router.metrics.PrimaryRouted.Add(1)
	tr.router.logger.Debug("routing to primary (transaction pin)",
		"dsn", redactDSN(tr.router.primary.DSN))
	return tr.router.primary.DSN, nil
}
