# Phase 1 Implementation Plan: MVP Baseline

**Goal**: Implement a working simulator with RandomCluster scheduler in ClusterOnly mode

**Timeline**: 3 weeks (15 working days)
**Team Size**: 1-2 engineers
**Target Completion**: November 18, 2025

---

## Testing Philosophy

**Test-as-you-go**: Write tests alongside implementation, not after.

- **Day 0**: Manual verification scripts
- **Days 1-5**: Unit tests for each component
- **Days 6-10**: Integration tests as components connect
- **Days 11-15**: End-to-end scenario tests

**Coverage targets**:
- Core logic: 80%+
- API handlers: 70%+
- Scripts/glue: Smoke tests only

---

## Phase 1 Scope

### What We're Building

✅ **Core Infrastructure**:
- 3 Kind clusters with KWOK nodes
- State manager that aggregates cluster state
- HTTP API server (GET /v1/state, POST /v1/placement)
- Decision enforcer (ClusterOnly mode only)
- Metrics collector (basic utilization)

✅ **Scheduler**:
- RandomCluster implementation
- Client library to talk to simulator API

✅ **Test Harness**:
- Scenario loader (YAML)
- Workload driver (queue management)
- Simple scenario runner

✅ **Observability**:
- JSON structured logs
- Metrics export to JSON file
- Decision audit trail

### What We're NOT Building (Deferred to Phase 2+)

❌ Advanced schedulers (Greedy, CP-SAT)
❌ NodePlacement or Gang modes
❌ Failure injection
❌ Time acceleration
❌ Prometheus integration
❌ Comparison reports

---

## Day 0: Prototype & Risk Reduction (1 day)

**Goal**: De-risk the core architecture with a minimal prototype before full implementation.

### Tasks

1. Single-cluster prototype (3 hours)
   - Create 1 Kind cluster ✅
   - Install KWOK controller ✅
   - Create 2 KWOK nodes with GPU capacity ✅
   - Manually verify nodes appear with `kubectl get nodes` ✅

2. Basic state aggregation (2 hours)
   - Write simple Go program using client-go ✅
   - List nodes and their GPU capacity ✅
   - Print JSON to stdout ✅
   - Verify it matches `kubectl describe node` ✅

3. Manual deployment test (2 hours)
   - Create a Deployment with GPU requests ✅
   - Watch pod get scheduled by default kube-scheduler ✅
   - Query pod's node placement ✅
   - Verify GPU capacity decreases ✅

4. Smoke test script (1 hour)
   - Bash script that automates above steps ✅
   - Creates cluster → lists nodes → creates deployment → verifies ✅
   - Forms basis for CI tests later ✅

### Deliverables

- ✅ Proof that Kind + KWOK + client-go works
- ✅ Understanding of KWOK node creation
- ✅ Confidence in GPU resource simulation
- ✅ Smoke test script (`test/smoke-test.sh`)

### Validation

```bash
# Run smoke test
./test/smoke-test.sh

# Should output:
# ✓ Cluster created
# ✓ KWOK installed
# ✓ 2 nodes with 8 GPUs each (16 total)
# ✓ Deployment created with 4 GPU request
# ✓ Pod scheduled to node-1
# ✓ Node-1 now shows 4/8 GPUs allocated
```

**Decision Gate**: If Day 0 reveals blockers (KWOK issues, client-go complexity), adjust plan before Week 1.

**Time Estimate**: 8 hours

---

## Week 1: Foundation (Days 1-5)

### Day 1: Project Setup & Multi-Cluster

**Tasks**:
1. Initialize Go project structure ✅
   - `go mod init github.com/yourorg/scheduler-simulator` ✅
   - Create directory structure (see design doc §5.2) ✅
   - Setup Makefile with common targets ✅

2. Setup Kind cluster creation script ✅
   - Install Kind CLI ✅
   - Create `scripts/setup-kind.sh` ✅
   - Bootstrap 3 clusters: cluster-1, cluster-2, cluster-3 ✅
   - Configure kubeconfigs ✅

3. Setup KWOK in each cluster ✅
   - Install KWOK controller ✅
   - Create KWOK node template ✅
   - Bootstrap 3-4 fake nodes per cluster with GPU capacity ✅

**Deliverables**:
- ✅ 3 Kind clusters running locally
- ✅ 9-12 KWOK nodes total (3-4 per cluster)
- ✅ Nodes have `nvidia.com/gpu` capacity defined
- ✅ Script to tear down and recreate clusters

**Validation**:
```bash
kubectl --context kind-cluster-1 get nodes
kubectl --context kind-cluster-1 describe node <node-name>
# Verify capacity shows nvidia.com/gpu: 8
```

**Time Estimate**: 6 hours

---

### Day 2: State Manager Foundation

**Tasks**:
1. Implement `pkg/k8s/client/` ✅
   - Wrapper around client-go ✅
   - Multi-cluster client manager ✅
   - Connection pooling ✅

2. Implement `pkg/simulator/state/types.go` ✅
   - Define Go structs for ClusterState ✅
   - JSON serialization tags ✅
   - Validation methods ✅

3. Implement basic state aggregation ✅
   - Watch nodes in all clusters (client-go informers) ✅
   - Aggregate into in-memory ClusterState ✅
   - Version incrementing on changes ✅

**Deliverables**:
- ✅ Can connect to 3 Kind clusters via Go code
- ✅ ClusterState struct with JSON marshaling
- ✅ Basic aggregator that lists nodes from all clusters

**Validation**:
```bash
go run cmd/simulator/main.go --mode=state-test
# Should print JSON of aggregated cluster state
```

**Time Estimate**: 8 hours

---

### Day 3: State Manager - Pod Tracking & Metrics

**Tasks**:
1. Add pod watching to state aggregator ✅
   - Track running pods per cluster ✅
   - Calculate allocated resources ✅

2. Implement capacity calculations (see `metrics-specification.md`) ✅
   - Total GPUs per cluster ✅
   - Allocated GPUs per cluster ✅
   - Free GPUs ✅
   - Utilization: `allocated / total` ✅

3. Implement fragmentation calculation ✅
   - For each size class (1, 2, 4, 8 GPUs) ✅
   - Calculate packable count: `sum(node.free // N)` ✅
   - Calculate wasted GPUs: `free - (packable * N)` ✅
   - Fragmentation score: `wasted / free` ✅

4. Implement state versioning ✅
   - Monotonic version counter ✅
   - ETag generation ✅
   - Change detection and debouncing (100ms window) ✅

5. **Write unit tests** ✅
   - Test utilization calculation ✅
   - Test fragmentation for known node layouts ✅
   - Test version incrementing ✅

**Deliverables**:
- ✅ State includes pods and allocations
- ✅ Versions increment on changes
- ✅ Accurate capacity calculations
- ✅ Fragmentation metrics per size class
- ✅ Unit tests with >80% coverage

**Validation**:
```bash
# Run unit tests
go test ./pkg/simulator/state -v

# Create a test deployment manually
kubectl --context kind-cluster-1 create deployment test --image=pause --replicas=2
kubectl --context kind-cluster-1 set resources deployment test --requests=nvidia.com/gpu=2

# Check aggregated state shows 2 GPUs allocated
go run cmd/simulator/main.go --mode=state-test | jq '.clusters[] | select(.id=="cluster-1")'

# Should show:
# "total_gpus": 24,
# "allocated_gpus": 4,
# "free_gpus": 20,
# "utilization": 0.167,
# "packability": {
#   "4gpu": {"packable_count": 5, "frag_score": 0.0}
# }
```

**Time Estimate**: 8 hours (includes testing)

---

### Day 4: HTTP API Server

**Tasks**:
1. Implement `pkg/simulator/api/server.go` ✅
   - HTTP server with graceful shutdown ✅
   - Request logging middleware ✅
   - CORS headers (for debugging) ✅

2. Implement GET /v1/state handler ✅
   - Fetch current state from StateManager ✅
   - Return with ETag header ✅
   - Support If-None-Match (304 responses) ✅

3. Implement POST /v1/placement handler (stub) ✅
   - Parse PlacementDecision JSON ✅
   - Validate stateVersion ✅
   - Return 202 Accepted (enforcement comes later) ✅

4. Implement GET /v1/metrics handler (stub) ✅
   - Return current metrics JSON ✅

5. Implement POST /v1/workloads handler (Added for testing) ✅
   - Parse Workload JSON ✅
   - Add to PendingQueue ✅

**Deliverables**:
- ✅ API server running on localhost:8080
- ✅ GET /v1/state returns ClusterState JSON with ETag
- ✅ POST /v1/placement accepts decisions (stub)
- ✅ POST /v1/workloads accepts new jobs

**Validation**:
```bash
# Start simulator
go run cmd/simulator/main.go

# In another terminal
curl http://localhost:8080/v1/state | jq .
curl -i http://localhost:8080/v1/state -H "If-None-Match: "1""
# Should return 304 if state hasn't changed

# Test placement endpoint
curl -X POST http://localhost:8080/v1/placement \
  -H "If-Match: "1"" \
  -H "Content-Type: application/json" \
  -d '{"stateVersion":"1","placementMode":"ClusterOnly","decisions":[]}'
```

**Time Estimate**: 6 hours

---

### Day 5: Scheduler Client Library & RandomScheduler

**Tasks**:
1. Implement `pkg/scheduler/client/client.go` ✅
   - HTTP client for simulator API ✅
   - GetState() method ✅
   - SubmitPlacement() method ✅
   - Retry logic for 409 Conflicts ✅

2. Implement RandomCluster scheduler ✅
   - `cmd/scheduler/random/main.go` ✅
   - Decision loop (poll every 5s) ✅
   - Random cluster selection from candidates with capacity ✅
   - Generate explain field ✅

3. Add shared types package ✅
   - `pkg/scheduler/types/types.go` ✅
   - PlacementDecision, ApplyPlacementResult structs ✅

4. Manual end-to-end testing (Added) ✅
   - Validate Infrastructure (3 Kind clusters) ✅
   - Validate Simulator State Aggregation ✅
   - Validate Scheduler Connection ✅
   - Validate Submit Workload -> Schedule Loop ✅

**Deliverables**:
- ✅ Scheduler client library
- ✅ RandomCluster scheduler binary
- ✅ Scheduler can fetch state and submit decisions
- ✅ Verified E2E loop (Workload -> Queue -> Scheduler -> Decision)

**Validation**:
```bash
# Terminal 1: Start simulator
go run cmd/simulator/main.go

# Terminal 2: Start random scheduler
go run cmd/scheduler/random/main.go --simulator-url=http://localhost:8080

# Check logs show scheduler polling and making decisions
```

**Time Estimate**: 6 hours

---

## Week 2: Integration & Testing (Days 6-10)

### Day 6: Decision Enforcer (ClusterOnly) ✅

**Tasks**:
1. Implement `pkg/simulator/enforcer/enforcer.go` ✅
   - Interface for different enforcement modes ✅
   - Conflict detection ✅
   - Transition tracking ✅

2. Implement ClusterOnly enforcement ✅
   - Create Deployment in target cluster ✅
   - Set replica count ✅
   - Configure resource requests ✅
   - Set labels for tracking ✅

3. Wire up POST /v1/placement → enforcer ✅
   - Validate decision ✅
   - Apply to clusters ✅
   - Update state ✅
   - Return result ✅

4. Implemented comprehensive test suite: ✅
   - Unit tests for `pkg/simulator/enforcer` ✅
   - E2E test in `test/e2e` for full system validation ✅
   - `make test-unit`, `make test-e2e`, `make test` commands added to Makefile ✅

**Deliverables**:
- ✅ Enforcer creates Deployments in target clusters
- ✅ POST /v1/placement actually applies decisions
- ✅ Can see pods appearing in clusters
- ✅ Unit and E2E tests passing

**Validation**:
```bash
make test

# Manual verification of deployed workloads
kubectl --context kind-cluster-1 get deployments
# etc.
```

**Time Estimate**: 8 hours

---

### Day 7: Workload Driver & Scenario Runner ✅

**Tasks**:
1. Implement scenario YAML parser ✅
   - `pkg/simulator/workload/scenario.go` ✅
   - Load clusters, workloads, timing ✅

2. Implement workload driver ✅
   - `pkg/simulator/workload/driver.go` ✅
   - Queue workloads at specified times ✅
   - Add to pending queue in StateManager ✅
   - Remove from queue when placed ✅

3. Create example scenarios ✅
   - `scenarios/baseline-gradual.yaml` ✅
   - `scenarios/baseline-burst.yaml` ✅

4. Wire up scenario runner to simulator main ✅

**Deliverables**:
- ✅ Can load scenario from YAML
- ✅ Workloads arrive at specified times
- ✅ End-to-end flow: scenario → queue → scheduler → enforcer → pods

**Validation**:
```bash
# Run complete scenario
go run cmd/simulator/main.go --scenario=scenarios/baseline-gradual.yaml

# Watch workloads being placed over time
# Verify deployments created in clusters
# Verify pods scheduled by default K8s schedulers
```

**Time Estimate**: 8 hours

---

### Day 8: Metrics Collection & Export

**Tasks**:
1. Implement metrics collector
   - `pkg/simulator/metrics/collector.go`
   - Compute utilization from state
   - Track decision latency
   - Track pending queue depth

2. Implement JSON exporter
   - `pkg/simulator/metrics/exporter.go`
   - Write metrics snapshots to file
   - Timestamped entries

3. Implement decision audit trail
   - Append-only log file
   - Record all decisions with explain field

4. Update GET /v1/metrics endpoint

**Deliverables**:
- ✅ Metrics computed continuously
- ✅ Metrics exported to `output/metrics.jsonl`
- ✅ Decision log exported to `output/decisions.jsonl`
- ✅ GET /v1/metrics returns current values

**Validation**:
```bash
# Run scenario
go run cmd/simulator/main.go --scenario=scenarios/baseline-gradual.yaml

# Check output files
cat output/metrics.jsonl | jq .
cat output/decisions.jsonl | jq .

# Verify utilization increases as workloads are placed
```

**Time Estimate**: 6 hours

---

### Day 9: Integration Testing & Bug Fixes

**Tasks**:
1. Write integration tests
   - Test state aggregation
   - Test API endpoints
   - Test decision enforcement

2. End-to-end scenario testing
   - Run multiple scenarios
   - Verify results
   - Check for race conditions

3. Bug fixes and polish
   - Handle edge cases
   - Improve error messages
   - Add more logging

**Deliverables**:
- ✅ Integration test suite passing
- ✅ 3+ scenarios run successfully
- ✅ No crashes or hangs

**Validation**:
```bash
make test-integration
make test-e2e

# Run overnight soak test
go run cmd/simulator/main.go --scenario=scenarios/soak-test.yaml --duration=8h
```

**Time Estimate**: 8 hours

---

### Day 10: Documentation & Demo

**Tasks**:
1. Write README.md
   - Quick start guide
   - How to run scenarios
   - How to add schedulers

2. Write API documentation
   - OpenAPI spec
   - Example requests/responses

3. Create demo scenario
   - Shows all features
   - Produces interesting results

4. Record demo video or create demo script

**Deliverables**:
- ✅ Complete README
- ✅ API documentation
- ✅ Demo that can be shown to stakeholders

**Validation**:
- Fresh checkout works: `git clone ... && make setup && make demo`
- Someone unfamiliar with project can run it

**Time Estimate**: 6 hours

---

## Task Dependencies

```
Day 1 (Kind clusters)
  └── Day 2 (State manager)
      └── Day 3 (Pod tracking)
          └── Day 4 (API server)
              ├── Day 5 (Scheduler)
              │   └── Day 6 (Enforcer)
              │       └── Day 7 (Workload driver)
              │           └── Day 8 (Metrics)
              │               └── Day 9 (Testing)
              │                   └── Day 10 (Docs)
```

---

## Detailed Task Breakdown

### Sprint Breakdown (Agile-style)

**Sprint 1 (Days 1-5)**: Foundation
- **Goal**: State aggregation + API + Random scheduler working independently
- **Demo**: Can manually trigger scheduler decisions via API

**Sprint 2 (Days 6-10)**: Integration
- **Goal**: End-to-end scenario runs successfully
- **Demo**: Run complete scenario, show metrics

---

## Daily Standup Template

**What did I complete yesterday?**
- Task X from Day N

**What will I work on today?**
- Task Y from Day N+1

**Any blockers?**
- Issue with KWOK setup
- Need clarification on metric calculation

---

## Definition of Done (Per Task)

- ✅ Code written and compiles
- ✅ Unit tests pass (if applicable)
- ✅ Manual validation completed
- ✅ Code reviewed (if team > 1)
- ✅ Committed to main branch
- ✅ Documentation updated

---

## Definition of Done (Phase 1 MVP)

- ✅ All 10 days of tasks completed
- ✅ Can run `make demo` and see working system
- ✅ 3+ scenarios run successfully
- ✅ Metrics show reasonable utilization (>50%)
- ✅ Decision logs show scheduler making random choices
- ✅ No crashes or hangs in 1-hour scenario run
- ✅ README accurate and complete
- ✅ Ready to demo to team/stakeholders

---

## Risk Mitigation

### High-Risk Items

1. **KWOK setup complexity**
   - Mitigation: Allocate full Day 1, have backup Kind-only plan
   - Fallback: Use real Kind nodes instead of KWOK

2. **client-go learning curve**
   - Mitigation: Review examples first, keep scope minimal
   - Fallback: Use kubectl exec in scripts (hacky but works)

3. **State aggregation performance**
   - Mitigation: Start with 3 clusters, 3 nodes each (small)
   - Fallback: Reduce cluster count if too slow

4. **Race conditions in state manager**
   - Mitigation: Use single writer goroutine, immutable reads
   - Fallback: Add mutex if needed (performance hit)

### Medium-Risk Items

1. **JSON schema mismatches**
   - Mitigation: Use Go structs with tags, validate early

2. **API versioning complexity**
   - Mitigation: Use simple integer versions for Phase 1

3. **Scenario timing accuracy**
   - Mitigation: Use time.Ticker for periodic tasks

---

## Testing Strategy

### Unit Tests
- State aggregation functions
- Metric calculations
- Scenario parsing
- Decision validation

**Target**: 60% code coverage

### Integration Tests
- API contract validation
- State manager ↔ K8s cluster
- Enforcer ↔ K8s API

**Target**: Major paths covered

### End-to-End Tests
- Complete scenario runs
- Multiple scheduler instances
- Conflict handling

**Target**: 3 scenarios pass

---

## Performance Targets (Phase 1)

| Metric | Target | Stretch Goal |
|--------|--------|--------------|
| State aggregation | < 500ms | < 200ms |
| API response time | < 50ms | < 20ms |
| Decision latency (Random) | < 100ms | < 50ms |
| Scenario runtime (10 workloads) | < 5 min | < 2 min |
| Concurrent workloads | 5 | 10 |

---

## Deliverables Checklist

### Code
- [ ] `pkg/simulator/state/` - State manager
- [ ] `pkg/simulator/api/` - HTTP API server
- [ ] `pkg/simulator/enforcer/` - Decision enforcer
- [ ] `pkg/simulator/metrics/` - Metrics collector
- [ ] `pkg/simulator/workload/` - Workload driver
- [ ] `pkg/k8s/client/` - K8s client wrapper
- [ ] `cmd/simulator/main.go` - Simulator binary
- [ ] `cmd/scheduler/random/main.go` - Random scheduler
- [ ] `pkg/scheduler/client/` - Scheduler client library

### Scripts
- [ ] `scripts/setup-kind.sh` - Setup Kind clusters
- [ ] `scripts/install-kwok.sh` - Install KWOK
- [ ] `scripts/run-scenario.sh` - Run scenario helper
- [ ] `scripts/teardown.sh` - Cleanup script
- [ ] `Makefile` - Common targets

### Scenarios
- [ ] `scenarios/baseline-gradual.yaml`
- [ ] `scenarios/baseline-burst.yaml`
- [ ] `scenarios/demo.yaml`

### Documentation
- [ ] `README.md` - Quick start guide
- [ ] `docs/design-document.md` - Design doc (already done)
- [ ] `docs/api-reference.md` - API documentation
- [ ] `docs/development.md` - Development guide
- [ ] `docs/troubleshooting.md` - Common issues

### Tests
- [ ] Unit tests for core packages
- [ ] Integration tests
- [ ] E2E test scenarios

### Outputs (from test runs)
- [ ] `output/metrics.jsonl` - Example metrics
- [ ] `output/decisions.jsonl` - Example decisions
- [ ] `output/demo-results.json` - Demo run results

---

## Success Criteria Validation

### Functional Requirements
1. ✅ System bootstraps 3 Kind clusters with KWOK
2. ✅ State manager aggregates nodes and pods
3. ✅ API serves current state via GET /v1/state
4. ✅ Random scheduler makes decisions
5. ✅ Decisions submitted via POST /v1/placement
6. ✅ Enforcer creates Deployments in clusters
7. ✅ Default K8s schedulers place pods
8. ✅ Metrics show utilization and latency
9. ✅ Complete scenario runs end-to-end
10. ✅ Logs and metrics exported to files

### Non-Functional Requirements
1. ✅ State aggregation < 500ms
2. ✅ API response < 50ms
3. ✅ System handles 5 concurrent workloads
4. ✅ No crashes in 1-hour run
5. ✅ Clear error messages on failures

---

## Post-MVP (Phase 2 Preview)

After Phase 1 is complete and validated, we'll move to Phase 2:

**Week 3-4**: Greedy/BestFit Scheduler
- Implement fragmentation scoring
- Compare against RandomCluster baseline

**Week 5-6**: NodePlacement Mode
- Custom scheduler plugin
- Node-level bin-packing

**Week 7-8**: Failure Injection
- Cordon/drain/delete nodes
- Test resilience and recovery

**Week 9-10**: Advanced Features
- Time acceleration
- Comparison reports
- Production trace replay

---

## Resources Needed

### Hardware
- Development machine: 16GB RAM, 4+ cores (for running 3 Kind clusters)
- Optional: Cloud VM for longer test runs

### Software
- Go 1.21+
- Docker
- Kind
- kubectl
- (Optional) VSCode with Go extension

### Knowledge
- Go programming
- Kubernetes basics (pods, deployments, scheduling)
- HTTP APIs and REST
- Basic testing practices

---

## Contact & Support

**Project Lead**: [Your Name]
**Slack Channel**: #scheduler-simulator
**Documentation**: https://github.com/yourorg/scheduler-simulator/docs
**Issues**: https://github.com/yourorg/scheduler-simulator/issues

---

## Appendix: Example Commands

### Setup
```bash
# Clone repo
git clone https://github.com/yourorg/scheduler-simulator
cd scheduler-simulator

# Install dependencies
make install-deps

# Setup Kind clusters
make setup-clusters

# Build binaries
make build
```

### Run Demo
```bash
# Terminal 1: Start simulator
./bin/simulator --scenario=scenarios/demo.yaml

# Terminal 2: Start random scheduler
./bin/random-scheduler --simulator-url=http://localhost:8080

# Watch logs and metrics
tail -f output/simulator.log
tail -f output/metrics.jsonl
```

### Cleanup
```bash
# Teardown clusters
make teardown

# Clean build artifacts
make clean
```

---

**Implementation Plan Status**: Ready to start ✅
**Estimated Effort**: 80 hours (2 weeks @ 1 engineer, or 1 week @ 2 engineers)
**Risk Level**: Low (well-scoped, clear tasks)
**Go/No-Go**: ✅ **GO** - Start on Day 1!
