package controlloop

import (
	"errors"
	"fmt"

	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// validatePlan performs sanity checks on an execution plan before execution.
// All violations are collected and returned via errors.Join.
func validatePlan(plan model.ExecutionPlan, snap worldstate.WorldStateSnapshot) error {
	var errs []error

	if err := checkNoSelfPreemption(plan); err != nil {
		errs = append(errs, err)
	}
	if err := checkReservationCapacity(plan, snap); err != nil {
		errs = append(errs, err)
	}
	if err := checkNoTerminatedScaleDown(plan, snap); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// checkNoSelfPreemption ensures no app is both scaled up and has its replicas
// preempted in the same plan.
func checkNoSelfPreemption(plan model.ExecutionPlan) error {
	scaleUpApps := make(map[model.AppID]struct{})
	for _, op := range plan.Operations {
		if op.Type == model.OperationScaleUp {
			if su, ok := op.Payload.(model.ScaleUpDecision); ok {
				scaleUpApps[su.AppID] = struct{}{}
			}
		}
	}

	var errs []error
	for _, op := range plan.Operations {
		if op.Type == model.OperationPreempt {
			if pd, ok := op.Payload.(model.PreemptionDecision); ok {
				if _, found := scaleUpApps[pd.VictimAppID]; found {
					errs = append(errs, fmt.Errorf(
						"self-preemption: app %s is both scaled up and preempted", pd.VictimAppID))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// checkReservationCapacity ensures that GPU reservations from scale-up
// operations don't exceed the cluster's total GPU capacity.
func checkReservationCapacity(plan model.ExecutionPlan, snap worldstate.WorldStateSnapshot) error {
	gpusPerCluster := make(map[model.ClusterID]int)
	for _, op := range plan.Operations {
		if op.Type == model.OperationScaleUp {
			if su, ok := op.Payload.(model.ScaleUpDecision); ok {
				if r, found := snap.Reservations[su.ReservationID]; found {
					gpusPerCluster[r.ClusterID] += r.GPUs
				}
			}
		}
	}

	var errs []error
	for clusterID, reserved := range gpusPerCluster {
		cluster, ok := snap.Clusters[clusterID]
		if !ok {
			continue
		}
		totalGPUs := 0
		for _, n := range cluster.Nodes {
			totalGPUs += n.TotalGPUs
		}
		if reserved > totalGPUs {
			errs = append(errs, fmt.Errorf(
				"reservations exceed capacity in cluster %s: %d reserved > %d total",
				clusterID, reserved, totalGPUs))
		}
	}
	return errors.Join(errs...)
}

// checkNoTerminatedScaleDown ensures we don't attempt to scale down replicas
// that are already terminated.
func checkNoTerminatedScaleDown(plan model.ExecutionPlan, snap worldstate.WorldStateSnapshot) error {
	var errs []error
	for _, op := range plan.Operations {
		if op.Type == model.OperationScaleDown {
			if sd, ok := op.Payload.(model.ScaleDownDecision); ok {
				if r, found := snap.Replicas[sd.ReplicaID]; found {
					if r.Status == model.ReplicaStatusTerminated {
						errs = append(errs, fmt.Errorf(
							"scale-down targets terminated replica %s of app %s",
							sd.ReplicaID, sd.AppID))
					}
				}
			}
		}
	}
	return errors.Join(errs...)
}
