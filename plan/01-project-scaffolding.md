# Section 01 — Project Scaffolding (Pre-Implementation Doc)

## Purpose

Bootstrap the Go module, directory layout, CI, build tooling, and a minimal health endpoint. Every subsequent section depends on this.

## Types and Function Signatures

### `cmd/schedulator/main.go`

```go
package main

import (
    "context"
    "encoding/json"
    "log"
    "net/http"
    "os"
    "os/signal"
    "syscall"
)

// healthResponse is the JSON body returned by /healthz.
type healthResponse struct {
    Status string `json:"status"`
}

// newMux creates the HTTP handler with all routes.
func newMux() *http.ServeMux

// run starts the HTTP server and blocks until ctx is cancelled,
// then shuts down gracefully. Returns any fatal error.
func run(ctx context.Context, addr string) error

func main()
```

### Key Behaviors

1. **Health endpoint**: `GET /healthz` → 200 OK, `Content-Type: application/json`, body `{"status":"ok"}`.
2. **Port configuration**: Reads `PORT` env var; defaults to `8080`.
3. **Graceful shutdown**: On SIGINT or SIGTERM, calls `http.Server.Shutdown(ctx)` with a 5-second deadline.

## Test Cases

### `TestHealthEndpoint`
- Start server using `httptest.NewServer(newMux())`
- `GET /healthz`
- Assert status code == 200
- Assert `Content-Type` header contains `application/json`
- Assert body decodes to `healthResponse{Status: "ok"}`

### `TestHealthEndpoint_MethodNotAllowed` (optional, if we restrict methods)
- Not implemented in v1 — standard mux allows all methods on a path.

## Edge Cases

- `PORT` env var empty → default to `8080`
- `PORT` env var set to custom value → server listens on that port
- Graceful shutdown completes in-flight requests

## Resolved Decisions

- No observability in this section (added in Section 02).
- No leader election or external deps — just a health check.
- Using `net/http` stdlib, no third-party router.
- `newMux()` is exported for testing (package-level function, not exported type).
