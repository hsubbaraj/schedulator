package worldstate

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

func TestBuildReplicasByApp(t *testing.T) {
	tests := []struct {
		name     string
		replicas map[model.ReplicaID]model.Replica
		wantApps map[model.AppID]int
	}{
		{name: "empty", replicas: map[model.ReplicaID]model.Replica{}, wantApps: map[model.AppID]int{}},
		{name: "single app single replica", replicas: map[model.ReplicaID]model.Replica{"r1": {ReplicaID: "r1", AppID: "a"}}, wantApps: map[model.AppID]int{"a": 1}},
		{name: "single app multiple replicas", replicas: map[model.ReplicaID]model.Replica{"r1": {ReplicaID: "r1", AppID: "a"}, "r2": {ReplicaID: "r2", AppID: "a"}, "r3": {ReplicaID: "r3", AppID: "a"}}, wantApps: map[model.AppID]int{"a": 3}},
		{name: "multiple apps", replicas: map[model.ReplicaID]model.Replica{"r1": {ReplicaID: "r1", AppID: "a"}, "r2": {ReplicaID: "r2", AppID: "b"}, "r3": {ReplicaID: "r3", AppID: "a"}}, wantApps: map[model.AppID]int{"a": 2, "b": 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildReplicasByApp(tt.replicas)
			for appID, n := range tt.wantApps {
				assert.Len(t, got[appID], n)
			}
			for appID, reps := range got {
				assert.Equal(t, tt.wantApps[appID], len(reps))
				for _, r := range reps {
					assert.Equal(t, appID, r.AppID)
				}
			}
			assert.Nil(t, got["c"])
		})
	}
}

func TestSnapshot_ReplicasByApp(t *testing.T) {
	ws := New(observability.NewNoopTracer(), prometheus.NewRegistry())
	ws.UpsertApplication(model.Application{AppID: "a"})
	ws.UpsertApplication(model.Application{AppID: "b"})
	ws.UpsertReplica(model.Replica{ReplicaID: "ra1", AppID: "a", Status: model.ReplicaStatusRunning})
	ws.UpsertReplica(model.Replica{ReplicaID: "ra2", AppID: "a", Status: model.ReplicaStatusPending})
	ws.UpsertReplica(model.Replica{ReplicaID: "rb1", AppID: "b", Status: model.ReplicaStatusRunning})

	snap := ws.Snapshot(context.Background())

	seen := map[model.ReplicaID]int{}
	for appID, reps := range snap.ReplicasByApp {
		for _, r := range reps {
			assert.Equal(t, appID, r.AppID)
			seen[r.ReplicaID]++
			_, ok := snap.Replicas[r.ReplicaID]
			assert.True(t, ok)
		}
	}
	for rid, r := range snap.Replicas {
		assert.Equal(t, 1, seen[rid])
		bucket := snap.ReplicasByApp[r.AppID]
		assert.NotEmpty(t, bucket)
	}
}
