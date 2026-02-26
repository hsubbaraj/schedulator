package engineutil

import (
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

func ReplicasForApp(snap worldstate.WorldStateSnapshot, appID model.AppID) []model.Replica {
	if snap.ReplicasByApp != nil {
		return snap.ReplicasByApp[appID]
	}

	replicas := make([]model.Replica, 0)
	for _, r := range snap.Replicas {
		if r.AppID == appID {
			replicas = append(replicas, r)
		}
	}
	return replicas
}

func CountActiveReplicas(snap worldstate.WorldStateSnapshot, appID model.AppID) int {
	count := 0
	for _, r := range ReplicasForApp(snap, appID) {
		if r.Status == model.ReplicaStatusRunning || r.Status == model.ReplicaStatusPending {
			count++
		}
	}
	return count
}
