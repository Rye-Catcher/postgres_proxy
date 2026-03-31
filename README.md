# PostgreSQL Proxy — Read/Write Splitting

[![Test](https://github.com/Rye-Catcher/postgres_proxy/actions/workflows/test.yml/badge.svg)](https://github.com/Rye-Catcher/postgres_proxy/actions/workflows/test.yml)

A lightweight TCP proxy for PostgreSQL that automatically routes read queries (SELECT) to replica nodes and write queries (INSERT, UPDATE, DELETE, DDL) to the primary node.

## Features

- **SQL Inspection** – inspects the first keyword of every incoming query to classify it as a read or write.
- **Read/Write Splitting** – SELECT queries are routed to healthy replicas; write operations always go to the primary.
- **Replica Round-Robin** – load is distributed evenly across healthy replicas.
- **Health Checks** – background goroutine pings every node at a configurable interval; unhealthy nodes are removed from rotation automatically.
- **Graceful Fallback** – if all replicas are unhealthy, reads fall back to the primary.
- **Metrics** – Prometheus-compatible metrics exposed via the admin endpoint.
- **Admin HTTP Endpoint** – `/health`, `/stats`, `/metrics`, `/nodes` endpoints for monitoring.
- **Structured JSON Logging** – all events are emitted as JSON to stdout.
- **Docker Compose** – one-command local setup with a primary and a replica.

## Architecture

```
Client → Proxy (TCP :5432)
              ├── Write Query  → Primary (PostgreSQL)
              └── Read  Query  → Replica (PostgreSQL, round-robin)
```

The proxy operates at the TCP/connection level. Each incoming client connection is associated with a backend (primary or replica) for its full lifetime, which is determined by the first query inspected during the connection startup phase.

## Quick Start

### With Docker Compose

```bash
# Build and start all services
docker compose up --build

# Connect via the proxy
psql "postgres://pguser:pgpassword@localhost:5432/appdb"

# Check admin stats
curl http://localhost:8080/stats | jq .

# Check Prometheus metrics
curl http://localhost:8080/metrics

# Check node status
curl http://localhost:8080/nodes | jq .
```

### Without Docker

```bash
# Set environment variables
export PRIMARY_DSN="postgres://user:pass@localhost:5433/mydb?sslmode=disable"
export REPLICA_DSNS="postgres://user:pass@localhost:5434/mydb?sslmode=disable"
export PROXY_LISTEN_ADDR="0.0.0.0:5432"
export PROXY_ADMIN_ADDR="0.0.0.0:8080"

# Run the proxy
go run .
```

## Configuration

All configuration is via environment variables:

| Variable | Default | Description |
|---|---|---|
| `PRIMARY_DSN` | *(required)* | PostgreSQL DSN for the primary node |
| `REPLICA_DSNS` | `` | Comma-separated list of replica DSNs |
| `PROXY_LISTEN_ADDR` | `0.0.0.0:5432` | Address the proxy listens on |
| `PROXY_ADMIN_ADDR` | `0.0.0.0:8080` | Address for the admin HTTP server |
| `HEALTH_CHECK_INTERVAL` | `10s` | How often to ping each node (Go duration) |
| `DB_MAX_OPEN_CONNS` | `10` | Max open DB connections per node |
| `DB_MAX_IDLE_CONNS` | `5` | Max idle DB connections per node |
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |

### DSN Formats

Both URL-style and keyword=value formats are supported:

```
# URL-style
postgres://user:password@host:5432/dbname?sslmode=disable

# Keyword=value style
host=localhost port=5432 user=pguser password=pgpassword dbname=appdb sslmode=disable
```

## Admin Endpoints

| Endpoint | Method | Description |
|---|---|---|
| `/health` | GET | 200 OK if primary healthy; 503 otherwise |
| `/stats` | GET | JSON object with all metrics |
| `/metrics` | GET | Prometheus text format metrics |
| `/nodes` | GET | JSON array of all node statuses |

### Example `/stats` response

```json
{
  "total_connections": 42,
  "active_connections": 3,
  "read_queries": 120,
  "write_queries": 18,
  "primary_routed": 20,
  "replica_routed": 118,
  "health_check_failures": 0,
  "goroutines": 12
}
```

## Development

### Running Tests Locally

The test suite covers SQL parsing, query routing, and all edge cases described below.
No running PostgreSQL instance is required — all tests use in-memory stubs.

```bash
# Run all tests (quiet)
go test ./...

# Run all tests with verbose output
go test -v ./...

# Run tests with the race detector enabled (recommended before submitting a PR)
go test -race ./...

# Run a specific test function
go test -v -run TestParseQueryType_CTETraps ./...

# Run tests matching a pattern
go test -v -run TestRouter ./...
```

#### Test coverage

| Test group | File | What it covers |
|---|---|---|
| `TestParseQueryType_Reads` | `parser_test.go` | Basic read statements (SELECT, SHOW, EXPLAIN, DESCRIBE, WITH) |
| `TestParseQueryType_Writes` | `parser_test.go` | Basic write statements (INSERT, UPDATE, DELETE, DDL, TCL) |
| `TestParseQueryType_Empty` | `parser_test.go` | Empty / comment-only input defaults to write |
| `TestParseQueryType_SelectToPrimary` | `parser_test.go` | `SELECT … FOR UPDATE/SHARE`, `nextval`, `setval`, `pg_advisory_lock/unlock`, `txid_current` → primary |
| `TestParseQueryType_CTETraps` | `parser_test.go` | CTEs with `INSERT`/`UPDATE`/`DELETE` → primary; read-only CTEs → replica |
| `TestParseQueryType_CommentsAndWhitespace` | `parser_test.go` | Leading `--` and `/* */` comments; misleading comments containing write keywords |
| `TestParseQueryType_CaseSensitivity` | `parser_test.go` | Mixed-case keywords (`SeLeCt`, `InSeRt`, …) |
| `TestParseQueryType_MultiStatement` | `parser_test.go` | Semicolon-separated multi-statement queries → primary |
| `TestParseQueryType_Explain` | `parser_test.go` | `EXPLAIN` → replica; `EXPLAIN ANALYZE` → primary |
| `TestParseQueryType_SpecialStatements` | `parser_test.go` | COPY, VACUUM, ANALYZE, REINDEX, LISTEN, NOTIFY, SET, RESET, DISCARD, PREPARE, EXECUTE |
| `TestFirstKeyword` / `TestStripComments` / helpers | `parser_test.go` | Internal parser helpers |
| `TestRouterWritesToPrimary` | `router_test.go` | Write queries always route to primary |
| `TestRouterReadsToReplica` | `router_test.go` | Read queries route to a healthy replica |
| `TestRouterReadsFallBackToPrimary` | `router_test.go` | Fallback to primary when all replicas are unhealthy |
| `TestRouterNoReplicas` | `router_test.go` | Reads go to primary when no replicas are configured |
| `TestRouterUnhealthyPrimary` | `router_test.go` | Error returned when primary is unhealthy |
| `TestRouterRoundRobin` | `router_test.go` | Load is spread evenly across multiple replicas |
| `TestRouterSelectForUpdateToPrimary` | `router_test.go` | Locking SELECTs routed to primary end-to-end |
| `TestRouterSideEffectSelectToPrimary` | `router_test.go` | Side-effect SELECTs routed to primary end-to-end |
| `TestRouterWriteCTEToPrimary` | `router_test.go` | Write CTEs routed to primary end-to-end |
| `TestRouterMultiStatementToPrimary` | `router_test.go` | Multi-statement queries routed to primary end-to-end |
| `TestParseQueryType_TransactionStatements` | `parser_test.go` | All transaction control syntax classified as writes |
| `TestTransactionRouter_SingleTransaction` | `transaction_router_test.go` | BEGIN→SELECT→UPDATE→SELECT→COMMIT all pinned to primary |
| `TestTransactionRouter_ReadOnlyTransaction` | `transaction_router_test.go` | `BEGIN READ ONLY` pinned to primary |
| `TestTransactionRouter_RepeatableRead` | `transaction_router_test.go` | `BEGIN ISOLATION LEVEL …` variants pinned to primary |
| `TestTransactionRouter_Rollback` | `transaction_router_test.go` | ROLLBACK ends pin; subsequent SELECTs return to replica |
| `TestTransactionRouter_Savepoints` | `transaction_router_test.go` | SAVEPOINT / ROLLBACK TO / RELEASE SAVEPOINT lifecycle on primary |
| `TestTransactionRouter_ErrorInTransaction` | `transaction_router_test.go` | Pin held through DB-level error until explicit ROLLBACK |
| `TestTransactionRouter_StartTransaction` | `transaction_router_test.go` | `START TRANSACTION` syntax pins to primary |
| `TestTransactionRouter_MultipleSequentialTransactions` | `transaction_router_test.go` | Pin/unpin correctly over multiple back-to-back transactions |

### Building

```bash
go build -o postgres_proxy .
```

### Transaction Routing Policy

`TransactionRouter` (`transaction_router.go`) is a per-session wrapper around `Router` that tracks whether a transaction is open. The policy is:

| Situation | Backend |
|---|---|
| No active transaction, read query | Replica (round-robin) |
| No active transaction, write query | Primary |
| `BEGIN` / `START TRANSACTION` | Primary — opens the transaction pin |
| Any statement while transaction is active | Primary — pin remains until COMMIT/ROLLBACK |
| `COMMIT` / `ROLLBACK` | Primary — releases the pin |
| `BEGIN READ ONLY` | Primary (pinned for simplicity and correctness) |
| `BEGIN ISOLATION LEVEL …` | Primary (pinned — isolation semantics require a single backend) |
| `SAVEPOINT` / `RELEASE SAVEPOINT` | Primary — session-state operations |

**Rationale:** Routing read-only or repeatable-read transactions to a replica would require the proxy to understand replication lag and snapshot visibility guarantees. Pinning all transactions to the primary is the safe and simple default; it can be revisited once the proxy implements lag-aware replica selection.

### Project Structure

| File | Description |
|---|---|
| `main.go` | Entry point; wires up all components |
| `config.go` | Configuration loading from env vars |
| `parser.go` | SQL query type detection |
| `router.go` | Query routing logic (primary vs. replica) |
| `transaction_router.go` | Per-session transaction-aware routing wrapper |
| `proxy.go` | TCP proxy and connection handling |
| `health.go` | Background health checker, DBNode |
| `metrics.go` | Atomic counters and Prometheus exposition |
| `admin.go` | Admin HTTP server |
| `logger.go` | Structured JSON logger setup |
| `docker-compose.yml` | Local development environment |
| `Dockerfile` | Multi-stage container image |

## Limitations

- The proxy routes at **connection level**, not at statement level. Each client connection is bound to one backend for its lifetime. This is intentional for a starter implementation; a full statement-level router would require a complete PostgreSQL wire protocol parser.
- SSL/TLS passthrough is not currently supported.
- Transaction-aware routing (ensuring all statements within a `BEGIN`/`COMMIT` block go to the same node) is left as a future enhancement.

## License

MIT
