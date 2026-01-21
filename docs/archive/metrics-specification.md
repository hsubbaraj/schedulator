# Metrics Specification

**Version:** 1.0  
**Date:** October 28, 2025

This document defines the metrics collected by the simulator to measure scheduling effectiveness.

---

## Core Metrics

### 1. Utilization

**Definition**: Fraction of GPU capacity currently allocated to running workloads.

**Formula**:
```
utilization = allocated_gpus / total_gpus
```

**Granularity**:
- Global (across all clusters)
- Per-cluster
- Per-GPU-type (H100, A100, L40)

**Example**:
```json
{
  "global_utilization": 0.83,
  "cluster_utilization": {
    "cluster-1": 0.85,
    "cluster-2": 0.87,
    "cluster-3": 0.75
  },
  "gpu_type_utilization": {
    "H100": 0.89,
    "A100": 0.82,
    "L40": 0.71
  }
}
```

---

### 2. Fragmentation (Packability)

**Definition**: Fraction of free GPU capacity that cannot be used due to fragmentation.

**Key Insight**: Free GPUs ≠ Usable GPUs. Fragmentation occurs when free GPUs exist but cannot satisfy pending workload topology requirements.

#### 2.1 Packability Calculation

For each size class (1, 2, 4, 8 GPUs):

```python
# How many N-GPU replicas can we actually place right now?
packable_count[N] = sum(node.free_gpus // N for node in cluster.nodes)

# How many GPUs would those replicas consume?
packable_gpus[N] = packable_count[N] * N

# How many free GPUs are wasted (not packable)?
wasted_gpus[N] = free_gpus - packable_gpus[N]

# Fragmentation score for this size class
frag_score[N] = wasted_gpus[N] / max(1, free_gpus)
```

**Range**: 0.0 (no fragmentation) to 1.0 (maximal fragmentation)

#### 2.2 Worked Example

**Cluster State**:
```
node-1: 8 GPUs total, 7 allocated, 1 free
node-2: 8 GPUs total, 4 allocated, 4 free
node-3: 8 GPUs total, 0 allocated, 8 free

Total: 24 GPUs, 11 allocated, 13 free
Utilization: 11/24 = 45.8%
```

**Packability for 1-GPU workloads**:
```python
packable_count[1] = (1//1) + (4//1) + (8//1) = 1 + 4 + 8 = 13 replicas
packable_gpus[1] = 13 * 1 = 13 GPUs
wasted_gpus[1] = 13 - 13 = 0 GPUs
frag_score[1] = 0 / 13 = 0.0  ✅ No fragmentation
```

**Packability for 4-GPU workloads**:
```python
packable_count[4] = (1//4) + (4//4) + (8//4) = 0 + 1 + 2 = 3 replicas
packable_gpus[4] = 3 * 4 = 12 GPUs
wasted_gpus[4] = 13 - 12 = 1 GPU (the singleton on node-1)
frag_score[4] = 1 / 13 = 0.077 = 7.7%  ✅ Low fragmentation
```

**Packability for 8-GPU workloads**:
```python
packable_count[8] = (1//8) + (4//8) + (8//8) = 0 + 0 + 1 = 1 replica
packable_gpus[8] = 1 * 8 = 8 GPUs
wasted_gpus[8] = 13 - 8 = 5 GPUs (1 on node-1, 4 on node-2)
frag_score[8] = 5 / 13 = 0.385 = 38.5%  ⚠️ Higher fragmentation
```

**Interpretation**: This cluster is well-packed for small workloads but has significant fragmentation for large (8-GPU) workloads.

#### 2.3 Weighted Aggregate Fragmentation

For a single score across all size classes, use workload-weighted average:

```python
# Define workload mix (based on scenario or historical data)
weights = {
    1: 0.2,   # 20% of workloads need 1 GPU
    2: 0.3,   # 30% need 2 GPUs
    4: 0.3,   # 30% need 4 GPUs
    8: 0.2    # 20% need 8 GPUs
}

weighted_frag = sum(weights[N] * frag_score[N] for N in [1, 2, 4, 8])
```

**Example** (using scores from above):
```python
weighted_frag = 0.2*0.0 + 0.3*0.0 + 0.3*0.077 + 0.2*0.385
              = 0 + 0 + 0.023 + 0.077
              = 0.10 = 10%  ✅ Overall low fragmentation
```

#### 2.4 JSON Output Format

```json
{
  "timestamp": 1698419200,
  "cluster_id": "cluster-1",
  "total_gpus": 24,
  "allocated_gpus": 11,
  "free_gpus": 13,
  "utilization": 0.458,
  
  "packability": {
    "1gpu": {
      "packable_count": 13,
      "packable_gpus": 13,
      "wasted_gpus": 0,
      "frag_score": 0.0
    },
    "2gpu": {
      "packable_count": 6,
      "packable_gpus": 12,
      "wasted_gpus": 1,
      "frag_score": 0.077
    },
    "4gpu": {
      "packable_count": 3,
      "packable_gpus": 12,
      "wasted_gpus": 1,
      "frag_score": 0.077
    },
    "8gpu": {
      "packable_count": 1,
      "packable_gpus": 8,
      "wasted_gpus": 5,
      "frag_score": 0.385
    }
  },
  
  "weighted_fragmentation": 0.10,
  "workload_weights": {"1": 0.2, "2": 0.3, "4": 0.3, "8": 0.2}
}
```

---

### 3. Placement Metrics

**Per-Scheduler Metrics**:

```json
{
  "scheduler_name": "RandomCluster",
  "decisions_made": 42,
  "decisions_accepted": 40,
  "decisions_rejected": 2,
  "conflicts": 1,
  
  "latency": {
    "decision_time_p50_ms": 12,
    "decision_time_p95_ms": 45,
    "decision_time_p99_ms": 120,
    "decision_time_max_ms": 350
  },
  
  "placement_changes": {
    "total": 127,
    "per_hour": 3.2,
    "by_reason": {
      "scale_up": 45,
      "scale_down": 22,
      "rebalance": 8,
      "failure_recovery": 12
    }
  }
}
```

---

### 4. Workload Metrics

**Per-Workload Tracking**:

```json
{
  "workload_id": "llama-70b",
  "target_replicas": 10,
  "running_replicas": 10,
  "pending_replicas": 0,
  "failed_replicas": 0,
  
  "placement": {
    "cluster-1": 6,
    "cluster-2": 4
  },
  
  "timing": {
    "submitted_at": 1698419100,
    "first_pod_scheduled_at": 1698419115,
    "all_pods_running_at": 1698419145,
    "time_to_first_pod_sec": 15,
    "time_to_full_deployment_sec": 45
  },
  
  "stability": {
    "placement_changes": 2,
    "evictions": 0,
    "restarts": 0
  }
}
```

---

### 5. System Health Metrics

```json
{
  "simulator": {
    "uptime_sec": 3600,
    "state_version": 1234,
    "state_updates_per_min": 4.2,
    "api_requests_per_min": 12.3,
    
    "errors": {
      "state_aggregation_errors": 0,
      "enforcement_errors": 1,
      "api_errors": 0
    }
  },
  
  "clusters": {
    "cluster-1": {
      "status": "healthy",
      "nodes": 10,
      "nodes_ready": 10,
      "api_latency_ms": 12
    },
    "cluster-2": {
      "status": "healthy",
      "nodes": 8,
      "nodes_ready": 8,
      "api_latency_ms": 15
    },
    "cluster-3": {
      "status": "degraded",
      "nodes": 5,
      "nodes_ready": 4,
      "nodes_not_ready": ["node-3"],
      "api_latency_ms": 250
    }
  }
}
```

---

## Comparison Metrics

When comparing schedulers, compute deltas:

```json
{
  "comparison": {
    "baseline": "RandomCluster",
    "candidate": "GreedyScheduler",
    "scenario": "traffic-spike-test",
    
    "deltas": {
      "utilization": {
        "baseline": 0.67,
        "candidate": 0.83,
        "improvement": "+23.9%"
      },
      
      "fragmentation": {
        "baseline": 0.35,
        "candidate": 0.12,
        "improvement": "-65.7%"
      },
      
      "decision_time_p95": {
        "baseline_ms": 45,
        "candidate_ms": 120,
        "regression": "+167%"
      },
      
      "placement_stability": {
        "baseline_changes_per_hour": 8.2,
        "candidate_changes_per_hour": 3.1,
        "improvement": "-62.2%"
      }
    },
    
    "verdict": "GreedyScheduler significantly improves utilization and fragmentation at the cost of 2.7x decision latency"
  }
}
```

---

## Collection Intervals

| Metric Type | Collection Frequency | Storage |
|-------------|---------------------|---------|
| Utilization | Every 10 seconds | Time-series |
| Fragmentation | Every 10 seconds | Time-series |
| Placement decisions | On each decision | Event log |
| Workload state | Every 30 seconds | Time-series |
| System health | Every 60 seconds | Time-series |

---

## Export Formats

### Phase 1: JSON Files

```bash
output/
├── metrics-timeseries.json      # All time-series metrics
├── decision-log.json             # All placement decisions
├── workload-events.json          # Workload lifecycle events
└── summary-report.json           # Final summary statistics
```

### Phase 2: Prometheus

```
# HELP gpu_utilization GPU utilization ratio (0.0-1.0)
# TYPE gpu_utilization gauge
gpu_utilization{cluster="cluster-1",gpu_type="H100"} 0.85

# HELP gpu_fragmentation Fragmentation score per size class (0.0-1.0)
# TYPE gpu_fragmentation gauge
gpu_fragmentation{cluster="cluster-1",size="4gpu"} 0.077

# HELP scheduler_decision_latency_seconds Decision latency histogram
# TYPE scheduler_decision_latency_seconds histogram
scheduler_decision_latency_seconds_bucket{scheduler="RandomCluster",le="0.01"} 5
scheduler_decision_latency_seconds_bucket{scheduler="RandomCluster",le="0.05"} 35
scheduler_decision_latency_seconds_bucket{scheduler="RandomCluster",le="0.1"} 40
```

---

## Implementation Notes

### Go Data Structures

```go
// pkg/simulator/metrics/types.go

type ClusterMetrics struct {
    Timestamp      time.Time         `json:"timestamp"`
    ClusterID      string            `json:"cluster_id"`
    TotalGPUs      int               `json:"total_gpus"`
    AllocatedGPUs  int               `json:"allocated_gpus"`
    FreeGPUs       int               `json:"free_gpus"`
    Utilization    float64           `json:"utilization"`
    Packability    map[string]Packability `json:"packability"`
    WeightedFrag   float64           `json:"weighted_fragmentation"`
}

type Packability struct {
    SizeClass      int     `json:"size_class"`      // 1, 2, 4, or 8
    PackableCount  int     `json:"packable_count"`  // # of N-GPU replicas
    PackableGPUs   int     `json:"packable_gpus"`   // GPUs consumed
    WastedGPUs     int     `json:"wasted_gpus"`     // Free but not packable
    FragScore      float64 `json:"frag_score"`      // 0.0-1.0
}

// Helper function
func CalculateFragmentation(nodes []Node, sizeClass int) Packability {
    freeGPUs := 0
    packableCount := 0
    
    for _, node := range nodes {
        free := node.Capacity["nvidia.com/gpu"] - node.Allocated["nvidia.com/gpu"]
        freeGPUs += free
        packableCount += free / sizeClass  // Integer division
    }
    
    packableGPUs := packableCount * sizeClass
    wastedGPUs := freeGPUs - packableGPUs
    
    fragScore := 0.0
    if freeGPUs > 0 {
        fragScore = float64(wastedGPUs) / float64(freeGPUs)
    }
    
    return Packability{
        SizeClass:     sizeClass,
        PackableCount: packableCount,
        PackableGPUs:  packableGPUs,
        WastedGPUs:    wastedGPUs,
        FragScore:     fragScore,
    }
}
```

---

## Validation Criteria

**Good Scheduler Characteristics**:
- Utilization > 80%
- Fragmentation < 20% (weighted average)
- Decision latency P95 < 5 seconds
- Placement stability < 5 changes/hour (non-spike periods)

**Red Flags**:
- Fragmentation increasing over time (poor bin-packing)
- High placement churn (thrashing)
- Decision latency > 30 seconds (too slow)
- Conflicts > 10% of decisions (stale state issues)

---

**End of Metrics Specification**
