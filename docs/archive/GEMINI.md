# Schedulator - Multi-Cluster GPU Scheduling Simulator

## Project Overview
**Schedulator** is a testing framework designed to evaluate multi-cluster LLM scheduling strategies across Kubernetes clusters with heterogeneous GPU resources. It uses **Kind** (Kubernetes in Docker) to run clusters and **KWOK** (Kubernetes WithOut Kubelet) to simulate fake GPU nodes efficiently.

The goal is to compare different scheduling algorithms (Random, Greedy, CP-SAT) by running them against a simulated environment that mirrors real-world conditions without the heavy resource cost.

## Current Status: Day 0 Prototype
**Date:** November 19, 2025
**State:** Infrastructure Ready, Code Pending.

*   ✅ **Infrastructure Scripts:** Scripts to create Kind clusters and populate them with KWOK nodes are ready in `scripts/`.
*   ✅ **Design:** Complete architecture, API specs, and implementation plans are available in `.md` files.
*   ⏳ **Go Code:** The Go project (`go.mod`, `main.go`, etc.) has **NOT** been initialized yet. This is the immediate next step.

## Directory Structure
*   `config/`: Configuration files (e.g., `kwok-nodes.yaml` defines the fake GPU nodes).
*   `scripts/`: Bash scripts for infrastructure management.
    *   `create-kind-cluster.sh`: Spins up a Kind cluster.
    *   `install-kwok.sh`: Installs the KWOK controller.
    *   `create-kwok-nodes.sh`: Creates fake nodes using the config.
*   `test/`: Testing scripts.
    *   `day0/`: Contains manual verification and smoke tests.
*   `*.md` & `*.mermaid`: Extensive documentation and diagrams.

## Key Documentation
*   **`implementation-plan-phase1.md`**: **CRITICAL**. This is the step-by-step guide for the current phase. Follow this strictly.
*   **`v3.md`**: The master design document and package summary.
*   **`metrics-specification.md`**: Formulas for calculating GPU fragmentation and utilization.
*   **`README.md`**: General project entry point.

## Development Conventions
*   **Language:** Go (1.21+).
*   **Infrastructure:** Kind + KWOK.
*   **Testing:** "Test-as-you-go" philosophy. Unit tests for components, integration tests for the API.
*   **Architecture:**
    *   **Simulator:** Neutral testbed, state manager, decision enforcer.
    *   **Scheduler:** Pluggable decision engine (talks to Simulator via HTTP API).

## Immediate Action Plan (Week 1)
Reference `implementation-plan-phase1.md` for details.

1.  **Initialize Go Module:** `go mod init github.com/yourorg/scheduler-simulator` (Confirm org name with user or use placeholder).
2.  **Create Directory Structure:** `pkg/`, `cmd/` as per standard Go layout.
3.  **State Aggregation:** Implement `pkg/k8s/client` to connect to Kind clusters and `pkg/simulator/state` to aggregate node/pod data.
4.  **API:** Build the HTTP API server (`pkg/simulator/api`).

## Operational Commands (Shell)
*   **Create Test Cluster:** `./scripts/create-kind-cluster.sh test-cluster`
*   **Install KWOK:** `./scripts/install-kwok.sh test-cluster`
*   **Add Nodes:** `./scripts/create-kwok-nodes.sh test-cluster`
*   **Run Smoke Test:** `./test/smoke-test.sh` (Verifies the Day 0 setup).
