# Schedulator - Multi-Cluster GPU Scheduling Simulator

**Schedulator** is a testing framework designed to evaluate multi-cluster LLM scheduling strategies across Kubernetes clusters with heterogeneous GPU resources. It uses **Kind** (Kubernetes in Docker) to run clusters and **KWOK** (Kubernetes WithOut Kubelet) to simulate fake GPU nodes efficiently.

The goal is to compare different scheduling algorithms (Random, Greedy, CP-SAT) by running them against a simulated environment that mirrors real-world conditions without the heavy resource cost.

## Documentation

*   **[Design Document](docs/DESIGN.md)**: Master implementation package, including architecture overview, project roadmap, and entry points to all other documentation.
*   **[API Reference](docs/API_REFERENCE.md)**: Detailed specification of the Simulator <-> Scheduler API.
*   **[Progress & Plan](docs/PROGRESS.md)**: Implementation status and roadmap.
*   **[Diagrams](docs/diagrams/)**: Architecture and sequence diagrams.

## Quick Start

### Prerequisites

1. **Docker** (must be running)
2. **Kind**
3. **kubectl**
4. **Go 1.21+**

### Setup

```bash
# 1. Create clusters and install KWOK
make setup-clusters

# 2. Build binaries
make build
```

### Running the Simulator

```bash
# Start the simulator
./bin/simulator
```

### Running the Scheduler

```bash
# Start the random scheduler (in a separate terminal)
./bin/random-scheduler --simulator-url=http://localhost:8080
```

## Project Structure

```
schedulator/
├── cmd/
│   ├── simulator/        # Main simulator entry point
│   └── scheduler/        # Scheduler implementations
├── pkg/
│   ├── simulator/        # Core simulator logic (state, api, enforcer)
│   ├── scheduler/        # Scheduler client and logic
│   └── k8s/              # Kubernetes client wrappers
├── docs/                 # Design and planning documentation
├── scripts/              # Infrastructure scripts (Kind, KWOK)
├── scenarios/            # Test scenarios (YAML)
└── Makefile              # Build and management commands
```

## Status

**Phase 1: ClusterOnly Random Scheduler**
*   Infrastructure: Ready (Kind + KWOK)
*   Simulator: Implemented (State, API, Enforcer)
*   Scheduler: Implemented (RandomCluster)
