package model

// PlacementDecisions is the output of the placement engine, containing all
// scale-up, scale-down, and preemption decisions plus GPU reservations to
// prevent double-booking.
type PlacementDecisions struct {
	ScaleUps           []ScaleUpDecision
	ScaleDowns         []ScaleDownDecision
	Preemptions        []PreemptionDecision
	Reservations       []GPUReservation
	ClaimedShadowSlots []ShadowSlot // migrations committed by the solver; nil for HeuristicPlanner
}

// ScaleUpDecision instructs the executor to create a new single-replica
// Deployment in the specified cluster with the given scheduling constraints.
type ScaleUpDecision struct {
	AppID                 AppID
	ClusterID             ClusterID
	ReservationID         ReservationID
	SchedulingConstraints SchedulingConstraints
}

// ScaleDownDecision instructs the executor to delete a specific replica's
// Deployment.
type ScaleDownDecision struct {
	AppID     AppID
	ReplicaID ReplicaID
}

// PreemptionDecision instructs the executor to gracefully terminate a victim
// replica to free capacity for a higher-priority workload.
type PreemptionDecision struct {
	VictimReplicaID    ReplicaID
	VictimAppID        AppID
	NodeID             NodeID
	ClusterID          ClusterID
	GracePeriodSeconds int
	Reason             string
}

// PreemptionPlan groups preemption decisions with the target cluster and
// scheduling constraints for the requesting application's new replica.
type PreemptionPlan struct {
	Victims       []PreemptionDecision
	TargetCluster ClusterID
	Constraints   SchedulingConstraints
}
