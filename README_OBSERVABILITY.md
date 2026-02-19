# Schedulator Observability Stack (SigNoz + ClickHouse)

This stack provides industry-standard observability with **Tail-based Sampling**.

## Components

1.  **SigNoz:** Unified UI for traces, metrics, and logs.
2.  **ClickHouse:** High-performance storage.
3.  **OTel Gateway:** Implements tail-sampling logic (100% errors/slow cycles, 10% successes).

## Quick Start (Automated)

The easiest way to get everything running (Kind + SigNoz + Schedulator) is to use the master start script:

```bash
./deploy/start.sh
```

## Quick Start (Manual Components)

1.  **Start the stack:**
    ```bash
    docker compose up -d
    ```

2.  **Run Schedulator:**
    Set the following environment variables:
    ```bash
    export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
    export APP_ENV=development
    go run ./cmd/schedulator
    ```

3.  **Access SigNoz:**
    Open [http://localhost:3301](http://localhost:3301) in your browser.

## Tail-Sampling Policies

The Gateway (`deploy/observability/otel-gateway.yaml`) is configured with:
- **Keep 100% of Errors:** Any trace with an ERROR status code.
- **Keep 100% of Slow Cycles:** Any trace longer than 500ms.
- **Sample 10% of Successes:** Probabilistic sampling for healthy traffic.

## Metrics
Metrics are scraped by the OTel Gateway from Schedulator's `/metrics` endpoint and forwarded to SigNoz. If running Schedulator outside of Docker, ensure the gateway can reach your host (the default config assumes Schedulator is reachable at `schedulator:8080`).
