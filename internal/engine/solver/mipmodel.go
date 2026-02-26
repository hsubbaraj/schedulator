//go:build solver

package solver

// mipmodel.go — CP-SAT model construction and variable mapping.
// Uses github.com/google/or-tools/ortools/sat/go/cpmodel.
//
// Decision variables (spec §5.5):
//   x[r][n]  BoolVar  — new replica r placed on node n
//   y[r][k]  BoolVar  — new replica r placed via shadow slot k (cluster-level)
//   p[v]     BoolVar  — existing replica v preempted
//   s[k]     BoolVar  — shadow slot k claimed
//   unmet[r] BoolVar  — new replica r is unmet (not placed)
//   u[a]     IntVar   — total unmet replicas for app a

import (
	"context"
	"log/slog"
	"math"

	cpmodel "github.com/google/or-tools/ortools/sat/go/cpmodel"
	cmpb "github.com/google/or-tools/ortools/sat/proto/cpmodel"
	sppb "github.com/google/or-tools/ortools/sat/proto/satparameters"
	"google.golang.org/protobuf/proto"

	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// newReplicaDef describes a single replica that needs to be placed this cycle.
type newReplicaDef struct {
	idx     int // index in newReplicas slice (used as variable key)
	appID   model.AppID
	reqGPUs int
}

// preemptVarDef describes an existing replica that may be preempted.
type preemptVarDef struct {
	replicaID model.ReplicaID
	appID     model.AppID
	clusterID model.ClusterID
	nodeID    model.NodeID
	gpus      int
	priority  int // victim app's priority
}

// nodeAssignment records the placement chosen by the solver.
type nodeAssignment struct {
	nodeID    model.NodeID
	clusterID model.ClusterID
	// slotKey is non-empty when this placement uses shadow capacity.
	slotKey string // "" means direct placement; slotID means shadow placement
}

// CPSATSolution is the result returned by BuildAndSolve.
type CPSATSolution struct {
	// Assignments maps replicaIdx → placement. Missing entry means unmet.
	Assignments map[int]*nodeAssignment
	// Preemptions is the set of victim ReplicaIDs to preempt.
	Preemptions map[model.ReplicaID]bool
	// ClaimedSlots is the set of shadow slot IDs that were claimed.
	ClaimedSlots map[string]bool
	// UnmetDemand counts unmet replicas per app.
	UnmetDemand map[model.AppID]int
	// Status is "optimal", "feasible", or "fallback".
	Status string
}

// cacheScore returns the cold-start penalty weight for placing a replica of
// appID on a specific cluster, based on the best cache tier available.
func cacheScore(appID model.AppID, clusterID model.ClusterID, snap worldstate.WorldStateSnapshot, cfg SolverConfig) int64 {
	app, ok := snap.Applications[appID]
	if !ok {
		return cfg.WColdRemote
	}
	best := cfg.WColdRemote
	for _, loc := range snap.CacheLocations[app.ModelID] {
		if loc.ClusterID != clusterID {
			continue
		}
		var w int64
		switch loc.Tier {
		case model.CacheTierGPUMemory:
			w = cfg.WColdGPUMemory
		case model.CacheTierLocalNVMe:
			w = cfg.WColdLocalNVMe
		case model.CacheTierClusterStorage:
			w = cfg.WColdClusterStorage
		default:
			w = cfg.WColdRemote
		}
		if w < best {
			best = w
		}
	}
	return best
}

// packingCoeff computes the integer packing reward for placing g GPUs on node n.
// Spec: packingBenefit = (AllocatedGPUs - g) / TotalGPUs; we drop the denominator
// to keep integer arithmetic and preserve relative ordering across nodes.
func packingCoeff(n model.Node, g int) int64 {
	return int64(n.AllocatedGPUs - g)
}

// wUnmet returns the unmet demand weight for the given app priority.
func wUnmet(priority int, cfg SolverConfig) int64 {
	switch priority {
	case 0:
		return cfg.WUnmetP0
	case 1:
		return cfg.WUnmetP1
	default:
		return 0 // P2 and above: no unmet penalty
	}
}

// wPreempt returns the preemption cost weight for preempting a victim of the
// given priority.
func wPreempt(victimPriority int, cfg SolverConfig) int64 {
	switch victimPriority {
	case 2:
		return cfg.WPreemptP2
	case 1:
		return cfg.WPreemptP1
	default:
		return 0
	}
}

// BuildAndSolve constructs the CP-SAT model and solves it.
// Returns CPSATSolution with Status="fallback" if no solution found.
func BuildAndSolve(
	ctx context.Context,
	newReplicas []newReplicaDef,
	preemptCandidates []preemptVarDef,
	shadowSlots []model.ShadowSlot,
	targets map[model.AppID]int, // appID -> needed count
	snap worldstate.WorldStateSnapshot,
	cfg SolverConfig,
) CPSATSolution {
	m := cpmodel.NewCpModelBuilder()

	// -----------------------------------------------------------------------
	// Variable maps
	// -----------------------------------------------------------------------

	// xVars[replicaIdx][nodeKey] — direct node placement
	xVars := make(map[int]map[string]cpmodel.BoolVar)
	// yVars[replicaIdx][slotIdx] — shadow slot placement
	yVars := make(map[int]map[int]cpmodel.BoolVar)
	// pVars[victimReplicaID] — preempt decision
	pVars := make(map[model.ReplicaID]cpmodel.BoolVar)
	// sVars[slotIdx] — shadow slot claim
	sVars := make([]cpmodel.BoolVar, len(shadowSlots))
	// unmetVars[replicaIdx] — unmet bit per replica
	unmetVars := make(map[int]cpmodel.BoolVar)
	// uVars[appID] — aggregate unmet count per app
	uVars := make(map[model.AppID]cpmodel.IntVar)

	// -----------------------------------------------------------------------
	// Initialise node-level capacity tracking
	// nodeKey = NodeID (unique across all clusters in snap)
	// -----------------------------------------------------------------------
	nodeFreeGPUs := make(map[model.NodeID]int)
	nodeCluster := make(map[model.NodeID]model.ClusterID)
	clusterFreeGPUs := make(map[model.ClusterID]int)

	for cid, cluster := range snap.Clusters {
		for nid, node := range cluster.Nodes {
			if node.Status != model.NodeStatusReady {
				continue
			}
			nodeFreeGPUs[nid] = node.FreeGPUs
			nodeCluster[nid] = cid
			clusterFreeGPUs[cid] += node.FreeGPUs
		}
	}

	// nodeVictimGPUs[nodeID] maps each node to the preemption vars and their GPU counts
	// for building C1 with preemption slack.
	type victimOnNode struct {
		pVar cpmodel.BoolVar
		gpus int
	}
	nodeVictims := make(map[model.NodeID][]victimOnNode)

	// -----------------------------------------------------------------------
	// Create preemption variables (p[v]) — spec §5.5, C4
	// Only for lower-priority victims, not at min_replicas.
	// -----------------------------------------------------------------------
	activeCount := make(map[model.AppID]int)
	for appID, replicas := range snap.ReplicasByApp {
		for _, r := range replicas {
			if r.Status == model.ReplicaStatusRunning || r.Status == model.ReplicaStatusPending {
				activeCount[appID]++
			}
		}
	}

	for _, vc := range preemptCandidates {
		pv := m.NewBoolVar()
		pVars[vc.replicaID] = pv
		nodeVictims[vc.nodeID] = append(nodeVictims[vc.nodeID], victimOnNode{pVar: pv, gpus: vc.gpus})
	}

	// -----------------------------------------------------------------------
	// Create shadow slot variables (s[k])
	// -----------------------------------------------------------------------
	for k := range shadowSlots {
		sVars[k] = m.NewBoolVar()
	}

	// -----------------------------------------------------------------------
	// Create placement variables (x[r][n] and y[r][k]) and unmet variables
	// -----------------------------------------------------------------------
	for ri, rep := range newReplicas {
		xVars[ri] = make(map[string]cpmodel.BoolVar)
		yVars[ri] = make(map[int]cpmodel.BoolVar)

		// x[r][n] for each ready node
		for cid, cluster := range snap.Clusters {
			for nid, node := range cluster.Nodes {
				if node.Status != model.NodeStatusReady {
					continue
				}
				_ = cid
				xv := m.NewBoolVar()
				xVars[ri][nid] = xv
			}
		}

		// y[r][k] for each shadow slot
		for k := range shadowSlots {
			yv := m.NewBoolVar()
			yVars[ri][k] = yv
		}

		// unmet bit for this replica
		unmetVars[ri] = m.NewBoolVar()
	}

	// uVars[a] = Σ unmetVars[r] for r in app a
	// Compute max unmet per app = target count
	for appID, needed := range targets {
		if needed <= 0 {
			continue
		}
		uVars[appID] = m.NewIntVar(0, int64(needed))
	}

	// -----------------------------------------------------------------------
	// Objective — Minimize J (spec §5.5)
	// -----------------------------------------------------------------------
	obj := cpmodel.NewLinearExpr()

	// Unmet demand terms: W_unmet(a) × u[a]
	for appID, uv := range uVars {
		app := snap.Applications[appID]
		w := wUnmet(app.Priority, cfg)
		if w > 0 {
			obj.AddTerm(uv, w)
		}
	}

	// Preemption terms: W_preempt(victim) × p[v]
	for _, vc := range preemptCandidates {
		pv := pVars[vc.replicaID]
		w := wPreempt(vc.priority, cfg)
		if w > 0 {
			obj.AddTerm(pv, w)
		}
	}

	// Shadow slot terms: MigrationCost × s[k]
	for k, slot := range shadowSlots {
		cost := int64(math.Round(slot.MigrationCost))
		if cost > 0 {
			obj.AddTerm(sVars[k], cost)
		}
	}

	// Placement cost terms: W_cold × x[r][n] + W_cold × y[r][k] − W_packing × pack × x[r][n]
	for ri, rep := range newReplicas {
		for nid, xv := range xVars[ri] {
			cid := nodeCluster[nid]
			node := snap.Clusters[cid].Nodes[nid]
			cold := cacheScore(rep.appID, cid, snap, cfg)
			pack := packingCoeff(node, rep.reqGPUs)
			net := cold - cfg.WPacking*pack
			if net != 0 {
				obj.AddTerm(xv, net)
			}
		}
		for k, slot := range shadowSlots {
			yv := yVars[ri][k]
			cold := cacheScore(rep.appID, slot.SourceClusterID, snap, cfg)
			// W_shadow cost is captured by s[k]; here we add the cold cost for the y placement.
			if cold != 0 {
				obj.AddTerm(yv, cold)
			}
		}
	}

	m.Minimize(obj)

	// -----------------------------------------------------------------------
	// C1 — Node-level GPU capacity (with preemption slack)
	// Σ_r x[r][n] × GPUs ≤ n.FreeGPUs + Σ_{v on n} p[v] × v.GPUs
	// -----------------------------------------------------------------------
	for _, cluster := range snap.Clusters {
		for nid, node := range cluster.Nodes {
			if node.Status != model.NodeStatusReady {
				continue
			}
			lhs := cpmodel.NewLinearExpr()
			for ri, rep := range newReplicas {
				if xv, ok := xVars[ri][nid]; ok {
					lhs.AddTerm(xv, int64(rep.reqGPUs))
				}
			}
			rhs := cpmodel.NewLinearExpr().AddConstant(int64(nodeFreeGPUs[nid]))
			for _, vn := range nodeVictims[nid] {
				rhs.AddTerm(vn.pVar, int64(vn.gpus))
			}
			m.AddLessOrEqual(lhs, rhs)
		}
	}

	// -----------------------------------------------------------------------
	// C1b — Cluster-level shadow capacity
	// Σ_{r,n∈c} x[r][n] × GPUs + Σ_{r,k:src==c} y[r][k] × GPUs
	//   ≤ clusterFree[c] + Σ_{k:src==c} s[k] × ReleasableGPUs
	// -----------------------------------------------------------------------
	for cid := range snap.Clusters {
		lhs := cpmodel.NewLinearExpr()
		for ri, rep := range newReplicas {
			for nid, xv := range xVars[ri] {
				if nodeCluster[nid] == cid {
					lhs.AddTerm(xv, int64(rep.reqGPUs))
				}
			}
			for k, slot := range shadowSlots {
				if slot.SourceClusterID == cid {
					lhs.AddTerm(yVars[ri][k], int64(rep.reqGPUs))
				}
			}
		}
		rhs := cpmodel.NewLinearExpr().AddConstant(int64(clusterFreeGPUs[cid]))
		for k, slot := range shadowSlots {
			if slot.SourceClusterID == cid {
				rhs.AddTerm(sVars[k], int64(slot.ReleasableGPUs))
			}
		}
		m.AddLessOrEqual(lhs, rhs)
	}

	// -----------------------------------------------------------------------
	// C2 — Each new replica placed exactly once or unmet
	// Σ_n x[r][n] + Σ_k y[r][k] + unmet[r] = 1
	// -----------------------------------------------------------------------
	for ri := range newReplicas {
		sum := cpmodel.NewLinearExpr()
		for _, xv := range xVars[ri] {
			sum.AddTerm(xv, 1)
		}
		for _, yv := range yVars[ri] {
			sum.AddTerm(yv, 1)
		}
		sum.AddTerm(unmetVars[ri], 1)
		m.AddEquality(sum, cpmodel.NewLinearExpr().AddConstant(1))
	}

	// uVars[a] = Σ unmetVars[r] for r in app a
	// Map replica index back to app.
	appReplicas := make(map[model.AppID][]int)
	for ri, rep := range newReplicas {
		appReplicas[rep.appID] = append(appReplicas[rep.appID], ri)
	}
	for appID, uv := range uVars {
		sumUnmet := cpmodel.NewLinearExpr()
		for _, ri := range appReplicas[appID] {
			sumUnmet.AddTerm(unmetVars[ri], 1)
		}
		m.AddEquality(sumUnmet, uv)
	}

	// -----------------------------------------------------------------------
	// C3 — Min replicas protection
	// Σ_{v∈app_a} (1 - p[v]) ≥ a.MinReplicas
	// -----------------------------------------------------------------------
	// Group preempt candidates by app.
	appVictims := make(map[model.AppID][]preemptVarDef)
	for _, vc := range preemptCandidates {
		appVictims[vc.appID] = append(appVictims[vc.appID], vc)
	}
	for appID, victims := range appVictims {
		app := snap.Applications[appID]
		if app.MinReplicas <= 0 {
			continue
		}
		// Σ (1 - p[v]) ≥ MinReplicas  ⟺  activeCount - Σ p[v] ≥ MinReplicas
		// ⟺  Σ p[v] ≤ activeCount - MinReplicas
		maxPreemptable := activeCount[appID] - app.MinReplicas
		if maxPreemptable <= 0 {
			// Cannot preempt any replicas of this app — force all p[v]=0.
			for _, vc := range victims {
				m.AddEquality(pVars[vc.replicaID], cpmodel.NewLinearExpr().AddConstant(0))
			}
			continue
		}
		sumP := cpmodel.NewLinearExpr()
		for _, vc := range victims {
			sumP.AddTerm(pVars[vc.replicaID], 1)
		}
		m.AddLessOrEqual(sumP, cpmodel.NewLinearExpr().AddConstant(int64(maxPreemptable)))
	}

	// -----------------------------------------------------------------------
	// C5 — Migration budget: Σ s[k] ≤ MaxShadowSlots
	// -----------------------------------------------------------------------
	if len(sVars) > 0 {
		sumS := cpmodel.NewLinearExpr()
		for _, sv := range sVars {
			sumS.AddTerm(sv, 1)
		}
		m.AddLessOrEqual(sumS, cpmodel.NewLinearExpr().AddConstant(int64(cfg.MaxShadowSlots)))
	}

	// -----------------------------------------------------------------------
	// Shadow link: y[r][k] = 1 → s[k] = 1
	// -----------------------------------------------------------------------
	for ri := range newReplicas {
		for k := range shadowSlots {
			m.AddImplication(yVars[ri][k], sVars[k])
		}
	}

	// -----------------------------------------------------------------------
	// C6 — Spread constraint (FailureDomainSpreadClusters)
	// -----------------------------------------------------------------------
	eligibleClusters := len(snap.Clusters)
	if eligibleClusters < 1 {
		eligibleClusters = 1
	}
	for appID, needed := range targets {
		app := snap.Applications[appID]
		if app.FailureDomainRule != model.FailureDomainSpreadClusters || needed <= 0 {
			continue
		}
		maxPerCluster := int64(math.Ceil(float64(needed) / float64(eligibleClusters)))
		for cid := range snap.Clusters {
			sum := cpmodel.NewLinearExpr()
			for ri, rep := range newReplicas {
				if rep.appID != appID {
					continue
				}
				for nid, xv := range xVars[ri] {
					if nodeCluster[nid] == cid {
						sum.AddTerm(xv, 1)
					}
				}
				for k, slot := range shadowSlots {
					if slot.SourceClusterID == cid {
						sum.AddTerm(yVars[ri][k], 1)
					}
				}
			}
			m.AddLessOrEqual(sum, cpmodel.NewLinearExpr().AddConstant(maxPerCluster))
		}
	}

	// -----------------------------------------------------------------------
	// Warm-start hint — provide heuristic assignment
	// -----------------------------------------------------------------------
	// Hint all variables to 0 (no placement). A better warm-start could be
	// computed by running the heuristic planner and mapping its result here.
	// For now, a zero-hint is valid and the solver finds feasible from scratch.

	// -----------------------------------------------------------------------
	// Solver parameters
	// -----------------------------------------------------------------------
	modelProto, err := m.Model()
	if err != nil {
		slog.ErrorContext(ctx, "solver: failed to build CP-SAT model", "error", err)
		return CPSATSolution{Status: "fallback"}
	}

	timeoutSec := float64(cfg.TimeoutMs) / 1000.0
	if timeoutSec <= 0 {
		timeoutSec = 0.2
	}
	params := &sppb.SatParameters{
		MaxTimeInSeconds:  proto.Float64(timeoutSec),
		NumSearchWorkers:  proto.Int32(1), // deterministic
		RandomSeed:        proto.Int32(int32(cfg.RandomSeed)),
	}

	resp, err := cpmodel.SolveCpModelWithParameters(modelProto, params)
	if err != nil {
		slog.ErrorContext(ctx, "solver: CP-SAT solve failed", "error", err)
		return CPSATSolution{Status: "fallback"}
	}

	status := resp.GetStatus()
	if status != cmpb.CpSolverStatus_OPTIMAL && status != cmpb.CpSolverStatus_FEASIBLE {
		slog.WarnContext(ctx, "solver: no feasible solution",
			"status", status.String(),
			"wall_time", resp.GetWallTime(),
		)
		return CPSATSolution{Status: "fallback"}
	}

	statusStr := "feasible"
	if status == cmpb.CpSolverStatus_OPTIMAL {
		statusStr = "optimal"
	}

	// -----------------------------------------------------------------------
	// Extract solution
	// -----------------------------------------------------------------------
	sol := CPSATSolution{
		Assignments:  make(map[int]*nodeAssignment),
		Preemptions:  make(map[model.ReplicaID]bool),
		ClaimedSlots: make(map[string]bool),
		UnmetDemand:  make(map[model.AppID]int),
		Status:       statusStr,
	}

	// Preemptions
	for vid, pv := range pVars {
		if cpmodel.SolutionBooleanValue(resp, pv) {
			sol.Preemptions[vid] = true
		}
	}

	// Shadow slots
	for k, sv := range sVars {
		if cpmodel.SolutionBooleanValue(resp, sv) {
			sol.ClaimedSlots[shadowSlots[k].SlotID] = true
		}
	}

	// Replica assignments
	for ri, rep := range newReplicas {
		placed := false

		// Check direct placement (x[r][n])
		for nid, xv := range xVars[ri] {
			if cpmodel.SolutionBooleanValue(resp, xv) {
				sol.Assignments[ri] = &nodeAssignment{
					nodeID:    nid,
					clusterID: nodeCluster[nid],
				}
				placed = true
				break
			}
		}

		// Check shadow placement (y[r][k])
		if !placed {
			for k, yv := range yVars[ri] {
				if cpmodel.SolutionBooleanValue(resp, yv) {
					sol.Assignments[ri] = &nodeAssignment{
						clusterID: shadowSlots[k].SourceClusterID,
						slotKey:   shadowSlots[k].SlotID,
					}
					placed = true
					break
				}
			}
		}

		if !placed {
			sol.UnmetDemand[rep.appID]++
		}
	}

	return sol
}
