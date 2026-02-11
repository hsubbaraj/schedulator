package model

// SchedulingConstraints defines placement preferences and requirements for a
// replica. Used by ClusterClient.CreateReplica to configure the pod template.
type SchedulingConstraints struct {
	RequiredGPUs        int
	PreferredNodes      []NodeID
	RequiredNodeLabels  map[string]string
	PreferredNodeLabels map[string]string
	CacheAffinityModel  ModelID
}
