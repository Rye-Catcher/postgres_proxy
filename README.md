# PostgreSQL Proxy — Read/Write Splitting

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

### Running Tests

```bash
go test ./...
```

### Building

```bash
go build -o postgres_proxy .
```

### Project Structure

| File | Description |
|---|---|
| `main.go` | Entry point; wires up all components |
| `config.go` | Configuration loading from env vars |
| `parser.go` | SQL query type detection |
| `router.go` | Query routing logic (primary vs. replica) |
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
