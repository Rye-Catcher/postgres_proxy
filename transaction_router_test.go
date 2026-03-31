package main

import "testing"

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newTR is a shorthand that builds a TransactionRouter with one healthy primary
// and one healthy replica.
func newTR(t *testing.T) (*TransactionRouter, *DBNode, *DBNode) {
	t.Helper()
	primary := makeNode("postgres://primary:5432/db", true)
	replica := makeNode("postgres://replica:5432/db", true)
	metrics := &Metrics{}
	router := NewRouter(primary, []*DBNode{replica}, metrics, testLogger())
	return NewTransactionRouter(router), primary, replica
}

// mustRoute calls tr.Route and fails the test on error.
func mustRoute(t *testing.T, tr *TransactionRouter, sql string) string {
	t.Helper()
	dsn, err := tr.Route(sql)
	if err != nil {
		t.Fatalf("Route(%q): %v", sql, err)
	}
	return dsn
}

// ---------------------------------------------------------------------------
// Single transaction routing
// ---------------------------------------------------------------------------

// TestTransactionRouter_SingleTransaction verifies the canonical transaction
// sequence: all statements inside BEGIN…COMMIT are pinned to the primary,
// while plain reads outside the transaction go to the replica.
func TestTransactionRouter_SingleTransaction(t *testing.T) {
	tr, primary, replica := newTR(t)

	// Before transaction: plain SELECT goes to replica.
	if dsn := mustRoute(t, tr, "SELECT * FROM users"); dsn != replica.DSN {
		t.Errorf("pre-tx SELECT → %q, want replica", dsn)
	}
	if tr.State() != TxStateNone {
		t.Error("expected TxStateNone before BEGIN")
	}

	// BEGIN opens the transaction and routes to primary.
	if dsn := mustRoute(t, tr, "BEGIN"); dsn != primary.DSN {
		t.Errorf("BEGIN → %q, want primary", dsn)
	}
	if tr.State() != TxStateActive {
		t.Error("expected TxStateActive after BEGIN")
	}

	// SELECT inside transaction → primary (pinned, not replica).
	if dsn := mustRoute(t, tr, "SELECT * FROM users WHERE id=1"); dsn != primary.DSN {
		t.Errorf("in-tx SELECT → %q, want primary", dsn)
	}

	// UPDATE inside transaction → primary.
	if dsn := mustRoute(t, tr, "UPDATE users SET name='x' WHERE id=1"); dsn != primary.DSN {
		t.Errorf("in-tx UPDATE → %q, want primary", dsn)
	}

	// Second SELECT inside transaction → still primary.
	if dsn := mustRoute(t, tr, "SELECT * FROM users WHERE id=1"); dsn != primary.DSN {
		t.Errorf("second in-tx SELECT → %q, want primary", dsn)
	}

	// COMMIT → primary, transaction pin released.
	if dsn := mustRoute(t, tr, "COMMIT"); dsn != primary.DSN {
		t.Errorf("COMMIT → %q, want primary", dsn)
	}
	if tr.State() != TxStateNone {
		t.Error("expected TxStateNone after COMMIT")
	}

	// After transaction: plain SELECT goes back to replica.
	if dsn := mustRoute(t, tr, "SELECT * FROM users"); dsn != replica.DSN {
		t.Errorf("post-tx SELECT → %q, want replica", dsn)
	}
}

// ---------------------------------------------------------------------------
// Read-only transaction
// ---------------------------------------------------------------------------

// TestTransactionRouter_ReadOnlyTransaction verifies that BEGIN READ ONLY is
// pinned to the primary (policy: pin to primary for simplicity and
// correctness; see README for rationale).
func TestTransactionRouter_ReadOnlyTransaction(t *testing.T) {
	tr, primary, _ := newTR(t)

	// BEGIN READ ONLY → primary.
	if dsn := mustRoute(t, tr, "BEGIN READ ONLY"); dsn != primary.DSN {
		t.Errorf("BEGIN READ ONLY → %q, want primary", dsn)
	}
	if tr.State() != TxStateActive {
		t.Error("expected TxStateActive after BEGIN READ ONLY")
	}

	// SELECTs inside the read-only transaction → primary (pinned).
	for _, sql := range []string{
		"SELECT * FROM users",
		"SELECT count(*) FROM users",
	} {
		if dsn := mustRoute(t, tr, sql); dsn != primary.DSN {
			t.Errorf("in read-only tx %q → %q, want primary", sql, dsn)
		}
	}

	// COMMIT ends the transaction.
	if dsn := mustRoute(t, tr, "COMMIT"); dsn != primary.DSN {
		t.Errorf("COMMIT → %q, want primary", dsn)
	}
	if tr.State() != TxStateNone {
		t.Error("expected TxStateNone after COMMIT")
	}
}

// ---------------------------------------------------------------------------
// Repeatable read / serializable
// ---------------------------------------------------------------------------

// TestTransactionRouter_RepeatableRead verifies that transactions opened with
// an explicit isolation level are pinned to the primary and that no backend
// switch occurs between statements.
func TestTransactionRouter_RepeatableRead(t *testing.T) {
	isolationBegins := []string{
		"BEGIN ISOLATION LEVEL REPEATABLE READ",
		"BEGIN ISOLATION LEVEL SERIALIZABLE",
		"BEGIN ISOLATION LEVEL READ COMMITTED",
		"START TRANSACTION ISOLATION LEVEL REPEATABLE READ",
	}

	for _, beginSQL := range isolationBegins {
		t.Run(beginSQL, func(t *testing.T) {
			tr, primary, _ := newTR(t)

			// Open the transaction.
			if dsn := mustRoute(t, tr, beginSQL); dsn != primary.DSN {
				t.Errorf("%q → %q, want primary", beginSQL, dsn)
			}
			if tr.State() != TxStateActive {
				t.Errorf("expected TxStateActive after %q", beginSQL)
			}

			// No backend switch during the transaction.
			if dsn := mustRoute(t, tr, "SELECT count(*) FROM users"); dsn != primary.DSN {
				t.Errorf("in-tx SELECT → %q, want primary (no backend switch)", dsn)
			}

			// Repeat the same SELECT (simulates repeatable read semantics).
			if dsn := mustRoute(t, tr, "SELECT count(*) FROM users"); dsn != primary.DSN {
				t.Errorf("repeated in-tx SELECT → %q, want primary", dsn)
			}

			if dsn := mustRoute(t, tr, "ROLLBACK"); dsn != primary.DSN {
				t.Errorf("ROLLBACK → %q, want primary", dsn)
			}
			if tr.State() != TxStateNone {
				t.Errorf("expected TxStateNone after ROLLBACK (%s)", beginSQL)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Rollback handling
// ---------------------------------------------------------------------------

// TestTransactionRouter_Rollback verifies that ROLLBACK correctly ends the
// transaction, routes the ROLLBACK to the primary, and releases the pin so
// that subsequent reads return to the replica.
func TestTransactionRouter_Rollback(t *testing.T) {
	tr, primary, replica := newTR(t)

	mustRoute(t, tr, "BEGIN")
	mustRoute(t, tr, "INSERT INTO users VALUES (999,'temp')")

	// ROLLBACK → primary, pin released.
	if dsn := mustRoute(t, tr, "ROLLBACK"); dsn != primary.DSN {
		t.Errorf("ROLLBACK → %q, want primary", dsn)
	}
	if tr.State() != TxStateNone {
		t.Error("expected TxStateNone after ROLLBACK")
	}

	// After rollback, SELECT should go to replica again.
	if dsn := mustRoute(t, tr, "SELECT * FROM users WHERE id=999"); dsn != replica.DSN {
		t.Errorf("post-ROLLBACK SELECT → %q, want replica", dsn)
	}
}

// TestTransactionRouter_RollbackWork verifies the ROLLBACK WORK variant.
func TestTransactionRouter_RollbackWork(t *testing.T) {
	tr, primary, _ := newTR(t)

	mustRoute(t, tr, "BEGIN")

	if dsn := mustRoute(t, tr, "ROLLBACK WORK"); dsn != primary.DSN {
		t.Errorf("ROLLBACK WORK → %q, want primary", dsn)
	}
	if tr.State() != TxStateNone {
		t.Error("expected TxStateNone after ROLLBACK WORK")
	}
}

// ---------------------------------------------------------------------------
// Savepoints
// ---------------------------------------------------------------------------

// TestTransactionRouter_Savepoints verifies that SAVEPOINT, ROLLBACK TO
// SAVEPOINT, and RELEASE SAVEPOINT are all routed to the primary and that
// session pinning is maintained throughout the entire savepoint lifecycle.
func TestTransactionRouter_Savepoints(t *testing.T) {
	tr, primary, _ := newTR(t)

	steps := []string{
		"BEGIN",
		"SAVEPOINT s1",
		"INSERT INTO users VALUES (1,'a')",
		"ROLLBACK TO s1",
		"SAVEPOINT s2",
		"INSERT INTO users VALUES (2,'b')",
		"RELEASE SAVEPOINT s2",
		"COMMIT",
	}

	for _, sql := range steps {
		if dsn := mustRoute(t, tr, sql); dsn != primary.DSN {
			t.Errorf("%q → %q, want primary", sql, dsn)
		}
	}

	if tr.State() != TxStateNone {
		t.Error("expected TxStateNone after final COMMIT")
	}
}

// TestTransactionRouter_SavepointOutsideTransaction verifies that SAVEPOINT
// and RELEASE SAVEPOINT go to the primary even outside an explicit transaction
// (auto-commit mode).
func TestTransactionRouter_SavepointOutsideTransaction(t *testing.T) {
	tr, primary, _ := newTR(t)

	for _, sql := range []string{"SAVEPOINT sp1", "RELEASE SAVEPOINT sp1"} {
		if dsn := mustRoute(t, tr, sql); dsn != primary.DSN {
			t.Errorf("%q outside transaction → %q, want primary", sql, dsn)
		}
		// State must remain None (no transaction opened by a savepoint alone).
		if tr.State() != TxStateNone {
			t.Errorf("expected TxStateNone after %q (no transaction)", sql)
		}
	}
}

// ---------------------------------------------------------------------------
// Error within transaction
// ---------------------------------------------------------------------------

// TestTransactionRouter_ErrorInTransaction verifies that the proxy keeps the
// session pinned to primary even after a statement that would produce a
// database-level error (e.g. duplicate key). The pin must not be released
// until the client issues an explicit COMMIT or ROLLBACK.
func TestTransactionRouter_ErrorInTransaction(t *testing.T) {
	tr, primary, _ := newTR(t)

	mustRoute(t, tr, "BEGIN")

	// Simulate a statement that would produce a DB error on the server side
	// (duplicate key, constraint violation, etc.).  The proxy cannot see the
	// DB response; it must keep the transaction open.
	if dsn := mustRoute(t, tr, "INSERT INTO users VALUES (1,'dup')"); dsn != primary.DSN {
		t.Errorf("INSERT → %q, want primary", dsn)
	}

	// State must still be TxStateActive — the proxy must not auto-close the
	// transaction based on a DB-level error it cannot observe.
	if tr.State() != TxStateActive {
		t.Error("expected TxStateActive after DB-error statement — proxy must not auto-close transaction")
	}

	// A subsequent SELECT must still go to primary (transaction still pinned).
	if dsn := mustRoute(t, tr, "SELECT 1"); dsn != primary.DSN {
		t.Errorf("SELECT after error → %q, want primary", dsn)
	}

	// Only an explicit ROLLBACK releases the pin.
	mustRoute(t, tr, "ROLLBACK")
	if tr.State() != TxStateNone {
		t.Error("expected TxStateNone after ROLLBACK")
	}
}

// ---------------------------------------------------------------------------
// START TRANSACTION syntax
// ---------------------------------------------------------------------------

// TestTransactionRouter_StartTransaction verifies that START TRANSACTION
// (the alternative PostgreSQL syntax for BEGIN) also pins to the primary.
func TestTransactionRouter_StartTransaction(t *testing.T) {
	tr, primary, replica := newTR(t)

	if dsn := mustRoute(t, tr, "START TRANSACTION"); dsn != primary.DSN {
		t.Errorf("START TRANSACTION → %q, want primary", dsn)
	}
	if tr.State() != TxStateActive {
		t.Error("expected TxStateActive after START TRANSACTION")
	}

	// SELECT inside transaction → primary (not replica).
	if dsn := mustRoute(t, tr, "SELECT * FROM users"); dsn != primary.DSN {
		t.Errorf("in-tx SELECT → %q, want primary (not replica %q)", dsn, replica.DSN)
	}

	mustRoute(t, tr, "COMMIT")
	if tr.State() != TxStateNone {
		t.Error("expected TxStateNone after COMMIT")
	}
}

// ---------------------------------------------------------------------------
// Non-transactional queries unaffected
// ---------------------------------------------------------------------------

// TestTransactionRouter_NonTransactionalQueriesUnaffected verifies that
// outside of an explicit transaction the TransactionRouter delegates to the
// underlying Router unchanged: reads go to replica, writes go to primary.
func TestTransactionRouter_NonTransactionalQueriesUnaffected(t *testing.T) {
	tr, primary, replica := newTR(t)

	readQueries := []string{
		"SELECT 1",
		"SELECT * FROM users",
		"SHOW search_path",
	}
	for _, sql := range readQueries {
		if dsn := mustRoute(t, tr, sql); dsn != replica.DSN {
			t.Errorf("non-tx read %q → %q, want replica", sql, dsn)
		}
	}

	writeQueries := []string{
		"INSERT INTO users VALUES (1,'a')",
		"UPDATE users SET name='b' WHERE id=1",
		"DELETE FROM users WHERE id=1",
	}
	for _, sql := range writeQueries {
		if dsn := mustRoute(t, tr, sql); dsn != primary.DSN {
			t.Errorf("non-tx write %q → %q, want primary", sql, dsn)
		}
	}
}

// ---------------------------------------------------------------------------
// No-replica configuration
// ---------------------------------------------------------------------------

// TestTransactionRouter_NoReplicaStillWorks verifies that the transaction
// router works correctly when no replicas are configured.
func TestTransactionRouter_NoReplicaStillWorks(t *testing.T) {
	primary := makeNode("postgres://primary:5432/db", true)
	metrics := &Metrics{}
	router := NewRouter(primary, nil, metrics, testLogger())
	tr := NewTransactionRouter(router)

	mustRoute(t, tr, "BEGIN")
	if dsn := mustRoute(t, tr, "SELECT * FROM users"); dsn != primary.DSN {
		t.Errorf("in-tx SELECT (no replicas) → %q, want primary", dsn)
	}
	mustRoute(t, tr, "COMMIT")

	// After commit, SELECT with no replicas still goes to primary.
	if dsn := mustRoute(t, tr, "SELECT * FROM users"); dsn != primary.DSN {
		t.Errorf("post-tx SELECT (no replicas) → %q, want primary", dsn)
	}
}

// ---------------------------------------------------------------------------
// Unhealthy primary
// ---------------------------------------------------------------------------

// TestTransactionRouter_UnhealthyPrimaryDuringTransaction verifies that an
// error is returned when the primary is unhealthy while a transaction is open.
func TestTransactionRouter_UnhealthyPrimaryDuringTransaction(t *testing.T) {
	primary := makeNode("postgres://primary:5432/db", true)
	replica := makeNode("postgres://replica:5432/db", true)
	metrics := &Metrics{}
	router := NewRouter(primary, []*DBNode{replica}, metrics, testLogger())
	tr := NewTransactionRouter(router)

	// Start the transaction while primary is healthy.
	if dsn := mustRoute(t, tr, "BEGIN"); dsn != primary.DSN {
		t.Errorf("BEGIN → %q, want primary", dsn)
	}

	// Primary goes down mid-transaction.
	primary.setStatus(NodeStatusUnhealthy)

	// Subsequent statement should return an error.
	_, err := tr.Route("SELECT * FROM users")
	if err == nil {
		t.Error("expected error when primary is unhealthy mid-transaction, got nil")
	}
}

// ---------------------------------------------------------------------------
// Multiple sequential transactions
// ---------------------------------------------------------------------------

// TestTransactionRouter_MultipleSequentialTransactions verifies that a single
// TransactionRouter correctly handles multiple back-to-back transactions,
// restoring replica routing between them.
func TestTransactionRouter_MultipleSequentialTransactions(t *testing.T) {
	tr, primary, replica := newTR(t)

	for i := 0; i < 3; i++ {
		// Read outside transaction goes to replica.
		if dsn := mustRoute(t, tr, "SELECT 1"); dsn != replica.DSN {
			t.Errorf("iteration %d: pre-tx SELECT → %q, want replica", i, dsn)
		}

		mustRoute(t, tr, "BEGIN")

		// Read inside transaction goes to primary.
		if dsn := mustRoute(t, tr, "SELECT 1"); dsn != primary.DSN {
			t.Errorf("iteration %d: in-tx SELECT → %q, want primary", i, dsn)
		}

		mustRoute(t, tr, "COMMIT")

		if tr.State() != TxStateNone {
			t.Errorf("iteration %d: expected TxStateNone after COMMIT", i)
		}
	}
}
