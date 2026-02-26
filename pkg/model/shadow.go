package model

// ShadowSlot represents a pre-computed migration opportunity offered to the
// solver as a costed capacity resource. Claiming a slot commits the plan
// generator to performing a blue-green migration of VictimReplicaID to
// DestClusterID, freeing ReleasableGPUs on SourceClusterID for new placement.
type ShadowSlot struct {
	SlotID          string
	SourceClusterID ClusterID  // cluster where GPUs are freed
	ReleasableGPUs  int        // GPUs freed on SourceClusterID
	VictimReplicaID ReplicaID  // replica to migrate away
	VictimAppID     AppID      // owning app (destination is same app)
	DestClusterID   ClusterID  // where the victim is migrated to
	DestConstraints SchedulingConstraints
	MigrationCost   float64 // W_shadow applied in objective
}
