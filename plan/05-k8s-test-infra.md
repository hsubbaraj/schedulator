# Section 05 — K8s Test Infrastructure

## Overview

Concrete `ClusterClient` implementation wrapping `client-go`, plus Kind/KWOK integration test infrastructure. This section implements `pkg/ports.ClusterClient` and provides the test scaffolding needed by the Plan Executor (Section 11) and end-to-end tests (Section 14).

## Types

### `internal/k8sclient/client.go`

```go
type Client struct {
    clientset      kubernetes.Interface
    namespace      string
    clusterID      model.ClusterID
    containerImage string
    tracer         trace.Tracer
    metrics        *Metrics
}
```

### `internal/k8sclient/metrics.go`

```go
type Metrics struct {
    requestDuration *prometheus.HistogramVec // labels: operation, cluster
    errorsTotal     *prometheus.CounterVec   // labels: operation, cluster
}
```

## Function Signatures

### Client Constructor

```go
func New(clientset kubernetes.Interface, namespace, clusterID string, tracer trace.Tracer, reg prometheus.Registerer) *Client
```

Default `containerImage`: `registry.k8s.io/pause:3.9`.

### ClusterClient Methods

```go
func (c *Client) CreateReplica(ctx context.Context, appID model.AppID, constraints model.SchedulingConstraints) (model.ReplicaID, error)
func (c *Client) DeleteReplica(ctx context.Context, replicaID model.ReplicaID) error
func (c *Client) GetReplicaStatus(ctx context.Context, replicaID model.ReplicaID) (model.ReplicaStatus, error)
func (c *Client) LabelReplica(ctx context.Context, replicaID model.ReplicaID, labels map[string]string) error
```

### Metrics Constructor

```go
func NewMetrics(reg prometheus.Registerer, clusterID string) *Metrics
```

Metric names:
- `schedulator_k8sclient_request_duration_seconds` (histogram, labels: operation, cluster)
- `schedulator_k8sclient_errors_total` (counter, labels: operation, cluster)

### Integration Helpers (`test/integration/helpers.go`)

```go
func SetupKindCluster(name string) (*rest.Config, error)
func TeardownKindCluster(name string) error
func CreateKWOKNodes(clientset kubernetes.Interface, count int) ([]string, error)
func WaitForNodeReady(ctx context.Context, clientset kubernetes.Interface, nodeName string) error
func InstallKWOK(ctx context.Context, clientset kubernetes.Interface) error
```

## Implementation Details

### CreateReplica
- Replica ID format: `<appID>-replica-<4-char-hex-suffix>`
- Deployment labels: `app.schedulator.io/app-id`, `app.schedulator.io/replica-id`
- GPU resource request: `nvidia.com/gpu: <constraints.RequiredGPUs>`
- Node affinity: `PreferredNodes` → `preferredDuringSchedulingIgnoredDuringExecution`
- NodeSelector: `RequiredNodeLabels` map
- Annotation: `schedulator.io/cache-affinity-model` if `CacheAffinityModel` is set

### GetReplicaStatus Mapping
- Deployment not found → `ReplicaStatusTerminated`
- `AvailableReplicas >= 1` → `ReplicaStatusRunning`
- Otherwise → `ReplicaStatusPending`

### DeleteReplica
- Idempotent: `errors.IsNotFound(err)` → return nil

## Test Cases

### Unit Tests (`internal/k8sclient/client_test.go`)

Uses `k8s.io/client-go/kubernetes/fake`.

| Test | Description |
|------|-------------|
| `TestCreateReplica_CreatesDeployment` | Verify Deployment created with correct name, labels, GPU requests |
| `TestCreateReplica_SetsNodeAffinity` | PreferredNodes → preferredDuringSchedulingIgnoredDuringExecution |
| `TestCreateReplica_RequiredNodeLabels` | RequiredNodeLabels → nodeSelector on pod |
| `TestDeleteReplica_DeletesDeployment` | Verify Deployment deleted |
| `TestDeleteReplica_NotFound` | Not found → returns nil (idempotent) |
| `TestGetReplicaStatus_Running` | Deployment with available replica → Running |
| `TestGetReplicaStatus_Pending` | Deployment with 0 available → Pending |
| `TestGetReplicaStatus_NotFound` | Missing Deployment → Terminated |
| `TestLabelReplica_AddsLabels` | Verify labels patched onto Deployment |
| `TestCreateReplica_TracesSpan` | Verify `k8sclient.create_replica` span emitted |
| `TestCreateReplica_RecordsMetrics` | Verify duration histogram recorded |

### Integration Tests (`test/integration/k8sclient_test.go`)

Build tag: `//go:build integration`

| Test | Description |
|------|-------------|
| `TestKWOKNodes_ReportGPUCapacity` | Create KWOK nodes, verify `nvidia.com/gpu: 8` |
| `TestClusterClient_CreateAndDeleteReplica` | Full lifecycle on kind cluster |

## Edge Cases

- `CreateReplica` with empty `PreferredNodes` → no affinity term set
- `CreateReplica` with empty `RequiredNodeLabels` → no nodeSelector set
- `DeleteReplica` when Deployment already deleted → nil error
- `GetReplicaStatus` for non-existent Deployment → `Terminated` (not error)
- `LabelReplica` with empty labels map → no-op patch
