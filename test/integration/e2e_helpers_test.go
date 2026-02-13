//go:build integration

package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/hsubbaraj/schedulator/internal/engine/placement"
	"github.com/hsubbaraj/schedulator/internal/engine/preemption"
	"github.com/hsubbaraj/schedulator/internal/engine/rebalancing"
	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/executor"
	"github.com/hsubbaraj/schedulator/internal/k8sclient"
	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/plangen"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

// ---------------------------------------------------------------------------
// statusOverrideClient — wraps a real ClusterClient, overrides GetReplicaStatus
// to return Running for created replicas (because KWOK pods stay Pending).
// ---------------------------------------------------------------------------

type statusOverrideClient struct {
	inner   ports.ClusterClient
	mu      sync.Mutex
	created map[model.ReplicaID]bool // true = created, false = deleted
}

func newStatusOverrideClient(inner ports.ClusterClient) *statusOverrideClient {
	return &statusOverrideClient{
		inner:   inner,
		created: make(map[model.ReplicaID]bool),
	}
}

func (c *statusOverrideClient) CreateReplica(ctx context.Context, appID model.AppID, constraints model.SchedulingConstraints) (model.ReplicaID, error) {
	id, err := c.inner.CreateReplica(ctx, appID, constraints)
	if err == nil {
		c.mu.Lock()
		c.created[id] = true
		c.mu.Unlock()
	}
	return id, err
}

func (c *statusOverrideClient) DeleteReplica(ctx context.Context, replicaID model.ReplicaID) error {
	err := c.inner.DeleteReplica(ctx, replicaID)
	if err == nil {
		c.mu.Lock()
		c.created[replicaID] = false
		c.mu.Unlock()
	}
	return err
}

func (c *statusOverrideClient) GetReplicaStatus(_ context.Context, replicaID model.ReplicaID) (model.ReplicaStatus, error) {
	c.mu.Lock()
	alive, ok := c.created[replicaID]
	c.mu.Unlock()
	if ok && alive {
		return model.ReplicaStatusRunning, nil
	}
	return model.ReplicaStatusTerminated, nil
}

func (c *statusOverrideClient) LabelReplica(ctx context.Context, replicaID model.ReplicaID, labels map[string]string) error {
	return c.inner.LabelReplica(ctx, replicaID, labels)
}

// ---------------------------------------------------------------------------
// engineStack — wires up all real engines for e2e tests.
// ---------------------------------------------------------------------------

type clusterSetup struct {
	ID        string
	Clientset kubernetes.Interface
}

type engineStack struct {
	WorldState  *worldstate.WorldState
	Scaling     *scaling.ScalingEngine
	Placement   *placement.PlacementEngine
	Preemption  *preemption.PreemptionEngine
	Rebalancing *rebalancing.RebalancingEngine
	PlanGen     *plangen.PlanGenerator
	Executor    *executor.Executor
	Clients     map[string]*statusOverrideClient
}

func newEngineStack(t *testing.T, clusters []clusterSetup) *engineStack {
	t.Helper()

	tracer := observability.NewNoopTracer()
	reg := prometheus.NewRegistry()

	ws := worldstate.New(tracer, reg)

	scalingCfg := scaling.DefaultScalingConfig()
	// Use very short cooldowns for integration tests.
	scalingCfg.ScaleUpCooldown = 0
	scalingCfg.ScaleDownCooldown = 0
	scalingEngine := scaling.NewScalingEngine(scalingCfg, tracer, reg)

	preemptionEngine := preemption.NewPreemptionEngine(
		preemption.DefaultPreemptionConfig(),
		tracer,
		reg,
	)

	placementEngine := placement.NewPlacementEngine(
		placement.DefaultPlacementConfig(),
		preemptionEngine,
		tracer,
		reg,
	)

	rebalancingEngine := rebalancing.NewRebalancingEngine(
		rebalancing.DefaultRebalancingConfig(),
		tracer,
		reg,
	)

	planGen := plangen.NewPlanGenerator(tracer, reg)

	clients := make(map[string]*statusOverrideClient)
	clusterClients := make(map[model.ClusterID]ports.ClusterClient)
	for _, cs := range clusters {
		inner := k8sclient.New(cs.Clientset, "default", cs.ID, tracer, prometheus.NewRegistry())
		wrapped := newStatusOverrideClient(inner)
		clients[cs.ID] = wrapped
		clusterClients[cs.ID] = wrapped
	}

	execCfg := executor.DefaultExecutorConfig()
	// Use short poll interval for integration tests.
	execCfg.PollInterval = 100 * time.Millisecond
	exec := executor.NewExecutor(execCfg, clusterClients, ws, tracer, reg)

	return &engineStack{
		WorldState:  ws,
		Scaling:     scalingEngine,
		Placement:   placementEngine,
		Preemption:  preemptionEngine,
		Rebalancing: rebalancingEngine,
		PlanGen:     planGen,
		Executor:    exec,
		Clients:     clients,
	}
}

// runCycle executes one full scheduling cycle and returns the execution result.
func (s *engineStack) runCycle(t *testing.T) model.ExecutionResult {
	return s.runCycleWithOverrides(t, nil)
}

// runCycleWithOverrides executes one full scheduling cycle. If overrides is
// non-nil, those scaling decisions are used instead of computing them.
func (s *engineStack) runCycleWithOverrides(t *testing.T, overrides map[model.AppID]scaling.ScalingDecision) model.ExecutionResult {
	t.Helper()
	ctx := context.Background()

	snap := s.WorldState.Snapshot(ctx)

	var scalingDecisions map[model.AppID]scaling.ScalingDecision
	if overrides != nil {
		scalingDecisions = overrides
	} else {
		scalingDecisions = s.Scaling.ComputeTargets(ctx, snap)
	}

	// Apply scaling history updates to world state (mimic control loop).
	for _, sd := range scalingDecisions {
		if sd.ScaleDownSignalObserved {
			s.WorldState.IncrementScaleDownSignal(sd.AppID)
		}
		if sd.ScaleActionApproved {
			dir := "up"
			if sd.Direction == scaling.ScaleDirectionDown {
				dir = "down"
			}
			s.WorldState.RecordScaleEvent(sd.AppID, dir, sd.ActionAt)
		}
	}

	placementDecisions := s.Placement.ComputePlacement(ctx, snap, scalingDecisions)

	// Apply reservations to world state (mimic control loop).
	for _, res := range placementDecisions.Reservations {
		_ = s.WorldState.CreateReservation(res)
	}

	// Re-snapshot after reservation updates.
	snap = s.WorldState.Snapshot(ctx)

	migrations := s.Rebalancing.FindRebalancingOpportunities(ctx, snap)
	plan := s.PlanGen.GeneratePlan(ctx, placementDecisions, migrations)
	return s.Executor.Execute(ctx, plan, snap)
}

// ---------------------------------------------------------------------------
// K8s assertion helpers
// ---------------------------------------------------------------------------

func countDeploymentsOnCluster(t *testing.T, clientset kubernetes.Interface, appID string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	deploys, err := clientset.AppsV1().Deployments("default").List(ctx, metav1.ListOptions{
		LabelSelector: "app.schedulator.io/app-id=" + appID,
	})
	require.NoError(t, err)
	return len(deploys.Items)
}

func cleanupDeploymentsOnCluster(t *testing.T, clientset kubernetes.Interface) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	deploys, err := clientset.AppsV1().Deployments("default").List(ctx, metav1.ListOptions{
		LabelSelector: "app.schedulator.io/app-id",
	})
	if err != nil {
		return
	}
	for _, d := range deploys.Items {
		_ = clientset.AppsV1().Deployments("default").Delete(ctx, d.Name, metav1.DeleteOptions{})
	}
}
