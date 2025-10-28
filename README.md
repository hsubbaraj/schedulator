# Schedulator - Multi-Cluster GPU Scheduling Simulator

A testing framework for evaluating multi-cluster LLM scheduling strategies across Kubernetes clusters with heterogeneous GPU resources.

## Day 0 Prototype Status

✅ Project structure created  
✅ Kind installation complete  
✅ Scripts created for cluster setup  
⏳ **Next: Start Docker Desktop and run cluster creation**

## Prerequisites

1. **Docker Desktop** - Must be running
   - Download: https://www.docker.com/products/docker-desktop/
   - **Start Docker Desktop before proceeding**

2. **Kind** - ✅ Installed at `~/bin/kind`
   - Version: v0.20.0

3. **kubectl** - Check with `kubectl version --client`

## Quick Start (Day 0 Prototype)

### Step 1: Start Docker Desktop

**IMPORTANT:** Open Docker Desktop application and wait for it to fully start.

Verify Docker is running:
```bash
docker ps
```

### Step 2: Create Kind Cluster with KWOK

```bash
# Set PATH to include kind
export PATH="$HOME/bin:$PATH"

# Create cluster
./scripts/create-kind-cluster.sh test-cluster

# Install KWOK controller
./scripts/install-kwok.sh test-cluster

# Create fake GPU nodes
./scripts/create-kwok-nodes.sh test-cluster
```

### Step 3: Verify Setup

```bash
# Check nodes
kubectl get nodes

# Expected output:
# NAME           STATUS   ROLES           AGE   VERSION
# kwok-node-1    Ready    agent           1m    fake
# kwok-node-2    Ready    agent           1m    fake
# test-cluster-control-plane   Ready    control-plane   2m    v1.27.3

# Check GPU capacity
kubectl get nodes -o custom-columns=NAME:.metadata.name,GPUs:.status.capacity.'nvidia\.com/gpu'

# Expected output:
# NAME           GPUs
# kwok-node-1    8
# kwok-node-2    8
```

## Project Structure

```
schedulator/
├── scripts/
│   ├── install-prerequisites.sh  # Install Kind and kubectl
│   ├── create-kind-cluster.sh    # Create Kind cluster
│   ├── install-kwok.sh           # Install KWOK controller
│   └── create-kwok-nodes.sh      # Create fake GPU nodes
├── config/
│   └── kwok-nodes.yaml           # KWOK node definitions (2x H100 nodes)
├── test/
│   └── day0/                     # Day 0 prototype tests
└── README.md                     # This file
```

## Next Steps (Day 0 Remaining Tasks)

1. ✅ Cluster setup scripts
2. ⏳ Go program to aggregate state (client-go)
3. ⏳ Manual deployment test with GPU requests
4. ⏳ Automated smoke test script

## Troubleshooting

### "Cannot connect to Docker daemon"
- **Solution:** Start Docker Desktop application and wait for it to initialize
- Verify: `docker ps` should list containers (or show empty list)

### "kind: command not found"
- **Solution:** Add Kind to PATH: `export PATH="$HOME/bin:$PATH"`
- Or add to shell profile: `echo 'export PATH="$HOME/bin:$PATH"' >> ~/.zshrc`

### Cluster already exists
- **Solution:** Delete and recreate: `kind delete cluster --name test-cluster`

### KWOK nodes not appearing
- **Solution:** Check KWOK controller is running: `kubectl get pods -n kube-system | grep kwok`

## Design Documents

- `v3.md` - Master implementation plan
- `implementation-plan-phase1.md` - Detailed 3-week plan
- `design-document.md` - Architecture analysis
- `metrics-specification.md` - Fragmentation and utilization formulas

## Phase 1 Goal

Build a working simulator with RandomCluster scheduler in ClusterOnly mode (3 weeks).

---

**Status:** Day 0 Prototype - Cluster setup ready, awaiting Docker start
